package registry

import (
	"fmt"
	"log/slog"
	"net/http"
	"sort"
	"time"

	"github.com/yugrinovD/gogate/internal/provider"
	"github.com/yugrinovD/gogate/internal/provider/mock"
	"github.com/yugrinovD/gogate/internal/provider/ollama"
	"github.com/yugrinovD/gogate/internal/provider/openaicompat"
	"github.com/yugrinovD/gogate/internal/security"
)

// Options contains dependencies shared by provider adapters.
type Options struct {
	Logger       *slog.Logger
	HTTPClient   *http.Client
	AllowPrivate bool
}

// Registry is an immutable snapshot of configured providers and model routes.
type Registry struct {
	byName   map[string]provider.Provider
	chains   map[string][]provider.Provider
	models   []provider.ModelInfo
	configs  map[string]provider.Config
	problems []string
}

// NewRegistry constructs provider adapters. Invalid providers are skipped and
// recorded in Problems so one bad admin-managed row cannot take down the gateway.
func NewRegistry(configs []provider.Config, opts Options) *Registry {
	r := &Registry{
		byName:  make(map[string]provider.Provider),
		chains:  make(map[string][]provider.Provider),
		configs: make(map[string]provider.Config),
	}
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	if opts.HTTPClient == nil {
		opts.HTTPClient = http.DefaultClient
	}
	sorted := append([]provider.Config(nil), configs...)
	sort.Slice(sorted, func(i, j int) bool {
		if sorted[i].Priority == sorted[j].Priority {
			return sorted[i].Name < sorted[j].Name
		}
		return sorted[i].Priority < sorted[j].Priority
	})

	modelOwners := make(map[string]string)
	for _, c := range sorted {
		if c.Timeout <= 0 {
			c.Timeout = 30 * time.Second
		}
		var p provider.Provider
		var err error
		switch c.Kind {
		case "mock":
			p, err = mock.New(c)
		case "ollama":
			if err = security.ValidateUpstreamURLStatic(c.BaseURL, opts.AllowPrivate); err == nil {
				p, err = ollama.New(c, opts.HTTPClient)
			}
		case "openai_compatible":
			if err = security.ValidateUpstreamURLStatic(c.BaseURL, opts.AllowPrivate); err == nil {
				p, err = openaicompat.New(c, opts.HTTPClient, opts.Logger)
			}
		default:
			err = fmt.Errorf("unsupported provider kind %q", c.Kind)
		}
		if err != nil {
			r.problems = append(r.problems, fmt.Sprintf("%s: %v", c.Name, err))
			opts.Logger.Warn("provider skipped", "provider", c.Name, "kind", c.Kind, "error", err)
			continue
		}
		r.byName[c.Name] = p
		r.configs[c.Name] = c
		for _, model := range c.Models {
			r.chains[model] = append(r.chains[model], p)
			if _, ok := modelOwners[model]; !ok {
				modelOwners[model] = c.Name
			}
		}
	}
	for model, owner := range modelOwners {
		r.models = append(r.models, provider.ModelInfo{ID: model, OwnedBy: owner})
	}
	sort.Slice(r.models, func(i, j int) bool { return r.models[i].ID < r.models[j].ID })
	return r
}

// ForModel returns the ordered fallback chain for a model.
func (r *Registry) ForModel(model string) []provider.Provider {
	if r == nil {
		return nil
	}
	return r.chains[model]
}

// ByName returns a provider by name.
func (r *Registry) ByName(name string) (provider.Provider, bool) {
	if r == nil {
		return nil, false
	}
	p, ok := r.byName[name]
	return p, ok
}

// Models returns all exposed models.
func (r *Registry) Models() []provider.ModelInfo {
	if r == nil {
		return nil
	}
	return append([]provider.ModelInfo(nil), r.models...)
}

// Configs returns provider configs keyed by name.
func (r *Registry) Configs() map[string]provider.Config {
	if r == nil {
		return nil
	}
	out := make(map[string]provider.Config, len(r.configs))
	for k, v := range r.configs {
		out[k] = v
	}
	return out
}

// Providers returns all active providers ordered by name.
func (r *Registry) Providers() []provider.Provider {
	if r == nil {
		return nil
	}
	out := make([]provider.Provider, 0, len(r.byName))
	for _, p := range r.byName {
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name() < out[j].Name() })
	return out
}

// Problems returns providers that could not be constructed.
func (r *Registry) Problems() []string {
	if r == nil {
		return nil
	}
	return append([]string(nil), r.problems...)
}
