package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/pprof"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/yugrinovD/gogate/internal/adminapi"
	"github.com/yugrinovD/gogate/internal/cache"
	"github.com/yugrinovD/gogate/internal/config"
	"github.com/yugrinovD/gogate/internal/control"
	"github.com/yugrinovD/gogate/internal/health"
	"github.com/yugrinovD/gogate/internal/httpapi"
	"github.com/yugrinovD/gogate/internal/logging"
	"github.com/yugrinovD/gogate/internal/middleware"
	"github.com/yugrinovD/gogate/internal/observability"
	"github.com/yugrinovD/gogate/internal/orchestrator"
	"github.com/yugrinovD/gogate/internal/provider"
	"github.com/yugrinovD/gogate/internal/ratelimit"
	"github.com/yugrinovD/gogate/internal/rediscli"
	"github.com/yugrinovD/gogate/internal/reqctx"
	"github.com/yugrinovD/gogate/internal/security"
	"github.com/yugrinovD/gogate/internal/store"
	"github.com/yugrinovD/gogate/internal/usage"
	"github.com/yugrinovD/gogate/internal/version"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "healthcheck" {
		os.Exit(healthcheck(os.Args[2:]))
	}
	versionFlag := flag.Bool("version", false, "print version and exit")
	flag.Parse()
	if *versionFlag {
		fmt.Println(version.String())
		return
	}
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "fatal:", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("configuration: %w", err)
	}
	log := logging.New(cfg.LogLevel)
	slog.SetDefault(log)
	log.Info("starting gogate", "version", version.String(), "config", cfg.Redacted())

	rootCtx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	shutdownTracing, err := observability.SetupTracing(rootCtx, observability.TracingOptions{
		Enabled: cfg.TracingEnabled, Endpoint: cfg.OTLPEndpoint, SampleRatio: cfg.TraceSample,
	})
	if err != nil {
		log.Warn("tracing disabled", "error", err)
		shutdownTracing = func(context.Context) error { return nil }
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = shutdownTracing(ctx)
	}()

	metrics := observability.NewMetrics()

	st, err := store.Open(rootCtx, cfg.DatabaseURL, cfg.DatabaseMaxConns, log)
	if err != nil {
		return err
	}
	defer st.Close()
	if err := st.Migrate(rootCtx); err != nil {
		return fmt.Errorf("migrate: %w", err)
	}
	if err := st.EnsureSemanticSchema(rootCtx); err != nil {
		return fmt.Errorf("ensure semantic schema: %w", err)
	}
	if cfg.BootstrapProviders {
		bootstrap := []store.BootstrapProvider{
			{Name: "mock", Kind: "mock", BaseURL: "internal://mock", Models: cfg.MockModels, Priority: 100, MaxRetries: 1},
		}
		if cfg.OllamaBaseURL != "" {
			if err := validateBootstrapURL(cfg.OllamaBaseURL, cfg.AllowPrivateUpstream); err != nil {
				log.Warn("ollama bootstrap skipped: invalid base url", "error", err)
			} else {
				bootstrap = append(bootstrap, store.BootstrapProvider{
					Name: "ollama", Kind: "ollama", BaseURL: cfg.OllamaBaseURL,
					Models: cfg.OllamaModels, Priority: 10, MaxRetries: 1,
				})
			}
		}
		if cfg.OpenAIBaseURL != "" && len(cfg.OpenAIModels) > 0 {
			if err := security.ValidateEnvName(cfg.OpenAIKeyEnv, cfg.UpstreamKeyEnvPrefix); err != nil {
				log.Warn("openai bootstrap skipped: invalid api key env", "error", err)
			} else if err := validateBootstrapURL(cfg.OpenAIBaseURL, cfg.AllowPrivateUpstream); err != nil {
				log.Warn("openai bootstrap skipped: invalid base url", "error", err)
			} else {
				bootstrap = append(bootstrap, store.BootstrapProvider{
					Name: "openai", Kind: "openai_compatible", BaseURL: cfg.OpenAIBaseURL,
					APIKeyEnv: cfg.OpenAIKeyEnv, Models: cfg.OpenAIModels, Priority: 5, MaxRetries: 1,
				})
			}
		}
		if err := st.BootstrapProviders(rootCtx, bootstrap); err != nil {
			return fmt.Errorf("bootstrap providers: %w", err)
		}
	}

	if providers, err := st.ListProviders(rootCtx); err == nil {
		for _, p := range providers {
			if p.Kind != "mock" && p.Kind != "ollama" {
				continue
			}
			for _, model := range p.Models {
				if err := st.EnsureZeroPrice(rootCtx, p.Name, model); err != nil {
					log.Warn("failed to seed zero price", "provider", p.Name, "model", model, "error", err)
				}
			}
		}
	}

	rdb, err := rediscli.New(rootCtx, rediscli.Options{
		Addr: cfg.RedisAddr, Password: cfg.RedisPassword, DB: cfg.RedisDB, PoolSize: cfg.RedisPoolSize,
		RequirePing: cfg.RateLimitEnabled && !cfg.RateLimitFailOpen,
	})
	if err != nil {
		if rdb == nil {
			return err
		}
		log.Warn("redis is unavailable; continuing in degraded mode", "error", err, "fail_open", cfg.RateLimitFailOpen)
	}
	defer func() { _ = rdb.Close() }()

	httpClient := provider.NewHTTPClient()
	controlManager := control.NewManager(st, log, cfg.AllowPrivateUpstream, httpClient, cfg.UpstreamKeyEnvPrefix)
	if err := controlManager.Reload(rootCtx); err != nil {
		return fmt.Errorf("initial control plane reload: %w", err)
	}

	writer := usage.NewWriter(st, log, usage.Options{
		OnDrop: func(kind string) {
			if kind == "usage" {
				metrics.IncUsageDropped()
			} else {
				metrics.IncAuditDropped()
			}
		},
	})
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = writer.Close(ctx)
	}()

	orch := orchestrator.New(controlManager, orchestrator.Options{
		FirstByteTimeout:  cfg.FirstByteTimeout,
		StreamIdleTimeout: cfg.StreamIdleTimeout,
		Logger:            log,
		Observer:          &orchestratorObserver{metrics: metrics},
	})
	limiter := ratelimit.New(rdb)
	var exact *cache.Exact
	if cfg.CacheEnabled {
		exact = cache.NewExact(rdb, cfg.CacheTTL)
	}
	var semantic *cache.Semantic
	if cfg.SemanticCacheEnabled {
		hasVector, err := st.HasVector(rootCtx)
		if err != nil || !hasVector {
			log.Warn("semantic cache disabled: pgvector extension is not available", "error", err)
		} else {
			var embedder cache.Embedder
			switch cfg.EmbeddingProvider {
			case "mock":
				embedder = cache.NewMockEmbedder(cfg.EmbeddingModel)
				log.Info("semantic cache using mock embedder", "model", cfg.EmbeddingModel)
			case "ollama":
				embedder = cache.NewOllamaEmbedder(cfg.OllamaBaseURL, cfg.EmbeddingModel, httpClient)
			case "openai":
				embedder = cache.NewOpenAIEmbedder(cfg.OpenAIBaseURL, os.Getenv(cfg.OpenAIKeyEnv), cfg.EmbeddingModel, httpClient)
			default:
				log.Error("unsupported embedding provider; semantic cache disabled", "provider", cfg.EmbeddingProvider)
			}
			if embedder == nil {
				// Error already logged; keep semantic cache disabled.
			} else if embedder.Dimensions() != 768 {
				log.Warn("semantic cache disabled: embedder dimension is not 768", "embedder", cfg.EmbeddingProvider, "dimensions", embedder.Dimensions())
			} else {
				semantic = cache.NewSemantic(st, embedder, cfg.SemanticThreshold, cfg.CacheTTL)
			}
		}
	}

	chatHandler := httpapi.New(httpapi.Options{
		Config: cfg, Control: controlManager, Orch: orch, Limiter: limiter,
		Exact: exact, Semantic: semantic, Usage: writer, Metrics: metrics, Logger: log,
	})
	healthHandler := health.New(st, rdb, cfg.RateLimitEnabled && !cfg.RateLimitFailOpen)
	adminHandler := adminapi.New(adminapi.Options{
		Config: cfg, Store: st, Control: controlManager, Orch: orch, Redis: rdb,
		Exact: exact, Semantic: semantic, Logger: log,
	})

	snapshotFn := func() *control.Snapshot { return controlManager.Snapshot() }
	chatAuth := middleware.APIKey(snapshotFn, cfg.Pepper, cfg.MaxAPIKeyBytes, func(id reqctx.Identity) {
		writer.TouchKey(id.KeyID)
	})

	systemMux := http.NewServeMux()
	systemMux.HandleFunc("GET /healthz", healthHandler.Live)
	systemMux.HandleFunc("GET /readyz", healthHandler.Ready)
	if cfg.MetricsEnabled {
		systemMux.Handle("GET /metrics", metrics.Handler())
	}
	systemHandler := metrics.Middleware(
		observability.TraceMiddleware(
			middleware.AccessLog(log, cfg.AccessLogRate)(systemMux)))

	apiMux := http.NewServeMux()
	apiMux.Handle("POST /v1/chat/completions", chatAuth(http.HandlerFunc(chatHandler.ChatCompletions)))
	apiMux.Handle("GET /v1/models", chatAuth(http.HandlerFunc(chatHandler.Models)))
	apiHandler := middleware.InflightLimit(cfg.MaxInflight)(
		metrics.Middleware(
			observability.TraceMiddleware(
				middleware.AccessLog(log, cfg.AccessLogRate)(
					middleware.BodyLimit(cfg.MaxBodyBytes)(apiMux)))))

	publicRouter := newPublicHandler(cfg.MetricsEnabled, systemHandler, apiHandler)
	publicHandler := middleware.RequestID(log)(
		middleware.Recover(log)(
			middleware.CORS(cfg.CORSOrigins)(publicRouter)))

	publicServer := &http.Server{
		Addr:              cfg.PublicAddr,
		Handler:           publicHandler,
		ReadHeaderTimeout: cfg.ReadHeaderTimeout,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      0,
		IdleTimeout:       cfg.IdleTimeout,
		MaxHeaderBytes:    1 << 20,
	}

	adminMux := http.NewServeMux()
	adminHandler.Register(adminMux)
	if cfg.PprofEnabled {
		adminMux.HandleFunc("/debug/pprof/", pprof.Index)
		adminMux.HandleFunc("/debug/pprof/cmdline", pprof.Cmdline)
		adminMux.HandleFunc("/debug/pprof/profile", pprof.Profile)
		adminMux.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
		adminMux.HandleFunc("/debug/pprof/trace", pprof.Trace)
	}
	adminHandlerHTTP := middleware.RequestID(log)(
		middleware.Recover(log)(
			middleware.AccessLog(log, 1)(
				middleware.AdminToken(cfg.AdminToken)(
					middleware.BodyLimit(1 << 20)(adminMux)))))
	adminServer := &http.Server{
		Addr:              cfg.AdminAddr,
		Handler:           adminHandlerHTTP,
		ReadHeaderTimeout: cfg.ReadHeaderTimeout,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       cfg.IdleTimeout,
		MaxHeaderBytes:    1 << 20,
	}

	go breakerLoop(rootCtx, metrics, orch)
	go maintenanceLoop(rootCtx, log, st, cfg)
	if cfg.ControlReloadInterval == 0 {
		log.Warn("periodic control-plane reload is disabled; key revocation depends on Redis pub/sub")
	} else {
		go control.PeriodicReload(rootCtx, controlManager, cfg.ControlReloadInterval, log)
	}
	go control.SubscribeReload(rootCtx, rdb, controlManager, log)

	errCh := make(chan error, 2)
	go func() {
		log.Info("public listener started", "addr", cfg.PublicAddr)
		if err := publicServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()
	if cfg.AdminEnabled() {
		go func() {
			log.Info("admin listener started", "addr", cfg.AdminAddr)
			if err := adminServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
				errCh <- err
			}
		}()
	} else {
		log.Warn("admin API disabled because GOGATE_ADMIN_TOKEN is empty")
	}

	var runErr error
	select {
	case <-rootCtx.Done():
	case runErr = <-errCh:
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_ = publicServer.Shutdown(shutdownCtx)
	if cfg.AdminEnabled() {
		_ = adminServer.Shutdown(shutdownCtx)
	}
	closeCtx, closeCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer closeCancel()
	_ = writer.Close(closeCtx)
	log.Info("shutdown complete")
	return runErr
}

type orchestratorObserver struct{ metrics *observability.Metrics }

func (o *orchestratorObserver) ProviderRetry(name string, _ int) { o.metrics.IncRetry(name) }
func (o *orchestratorObserver) ProviderFailure(name string, _ error) {
	o.metrics.IncUpstreamError(name, "precommit")
}

func breakerLoop(ctx context.Context, metrics *observability.Metrics, orch *orchestrator.Orchestrator) {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			for name, state := range orch.Breakers().Snapshot() {
				v := 0.0
				switch state {
				case "half_open":
					v = 1
				case "open":
					v = 2
				}
				metrics.SetBreaker(name, v)
			}
		}
	}
}

func maintenanceLoop(ctx context.Context, log *slog.Logger, st *store.Store, cfg config.Config) {
	ticker := time.NewTicker(time.Hour)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			runCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
			if err := st.PurgeExpired(runCtx, cfg.AuditRetention); err != nil {
				log.Warn("retention purge failed", "error", err)
			}
			cancel()
		}
	}
}

func healthcheck(args []string) int {
	fs := flag.NewFlagSet("healthcheck", flag.ContinueOnError)
	url := fs.String("url", "http://127.0.0.1:8080/readyz", "health check URL")
	timeout := fs.Duration("timeout", 3*time.Second, "timeout")
	if err := fs.Parse(args); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	client := &http.Client{Timeout: *timeout}
	resp, err := client.Get(*url)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		fmt.Fprintf(os.Stderr, "health check failed: %s\n", resp.Status)
		return 1
	}
	return 0
}

func validateBootstrapURL(raw string, allowPrivate bool) error {
	if allowPrivate {
		return security.ValidateUpstreamURLStatic(raw, true)
	}
	return security.ValidateUpstreamURL(raw, false)
}
