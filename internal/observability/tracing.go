package observability

import (
	"context"
	"fmt"
	"net/url"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
	"go.opentelemetry.io/otel/trace"

	"github.com/yugrinovD/gogate/internal/version"
)

var tracer = otel.Tracer("gogate")

// Tracer returns the gateway tracer.
func Tracer() trace.Tracer { return tracer }

// TracingOptions configures the OTLP exporter.
type TracingOptions struct {
	Enabled     bool
	Endpoint    string
	SampleRatio float64
	ServiceName string
}

// SetupTracing initializes OTel and returns a shutdown function.
func SetupTracing(ctx context.Context, opts TracingOptions) (func(context.Context) error, error) {
	if !opts.Enabled || opts.Endpoint == "" {
		return func(context.Context) error { return nil }, nil
	}
	u, err := url.Parse(opts.Endpoint)
	if err != nil {
		return nil, fmt.Errorf("parse otlp endpoint: %w", err)
	}
	if u.Host == "" {
		return nil, fmt.Errorf("otlp endpoint must include a host")
	}
	exporterOpts := []otlptracehttp.Option{otlptracehttp.WithEndpoint(u.Host)}
	if u.Scheme != "https" {
		exporterOpts = append(exporterOpts, otlptracehttp.WithInsecure())
	}
	if u.Path != "" && u.Path != "/" {
		exporterOpts = append(exporterOpts, otlptracehttp.WithURLPath(u.Path))
	}
	exporterOpts = append(exporterOpts, otlptracehttp.WithTimeout(5*time.Second))
	exporter, err := otlptracehttp.New(ctx, exporterOpts...)
	if err != nil {
		return nil, fmt.Errorf("create otlp exporter: %w", err)
	}
	if opts.SampleRatio <= 0 {
		opts.SampleRatio = 1
	}
	if opts.ServiceName == "" {
		opts.ServiceName = "gogate"
	}
	res, err := resource.New(ctx,
		resource.WithAttributes(
			semconv.ServiceName(opts.ServiceName),
			semconv.ServiceVersion(version.Version),
		),
		resource.WithProcessRuntimeDescription(),
		resource.WithHost(),
	)
	if err != nil {
		return nil, fmt.Errorf("create otel resource: %w", err)
	}
	provider := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exporter),
		sdktrace.WithResource(res),
		sdktrace.WithSampler(sdktrace.ParentBased(sdktrace.TraceIDRatioBased(opts.SampleRatio))),
	)
	otel.SetTracerProvider(provider)
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(propagation.TraceContext{}, propagation.Baggage{}))
	tracer = provider.Tracer("gogate")
	return provider.Shutdown, nil
}
