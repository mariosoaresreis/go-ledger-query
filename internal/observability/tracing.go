// Package observability — OpenTelemetry tracer setup.
// By default the global OTEL tracer is a no-op. Call SetupTracing in main to
// activate span export.
//
// Export destinations:
//   - OTEL_TRACES=stdout → human-readable spans printed to stdout (dev/debug)
//   - OTEL_TRACES=<anything else / unset> → no-op (default, zero overhead)
//
// To wire an OTLP gRPC exporter in production, replace the stdouttrace exporter
// here with go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc.
package observability

import (
	"context"
	"os"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/stdout/stdouttrace"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
)

// SetupTracing initialises the OTEL global trace provider.
// The returned shutdown function must be called on service exit to flush spans.
//
//	shutdown, err := observability.SetupTracing(ctx, "ledger-query")
//	defer shutdown()
func SetupTracing(ctx context.Context, serviceName string) (shutdown func(), err error) {
	mode := os.Getenv("OTEL_TRACES")
	if mode == "" {
		// No exporter configured — keep the default no-op provider.
		return func() {}, nil
	}

	exp, err := stdouttrace.New(stdouttrace.WithPrettyPrint())
	if err != nil {
		return func() {}, err
	}

	res, err := resource.New(ctx,
		resource.WithAttributes(semconv.ServiceName(serviceName)),
		resource.WithAttributes(semconv.ServiceVersion("1.0.0")),
	)
	if err != nil {
		return func() {}, err
	}

	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exp),
		sdktrace.WithResource(res),
		sdktrace.WithSampler(sdktrace.AlwaysSample()),
	)
	otel.SetTracerProvider(tp)

	return func() {
		_ = tp.Shutdown(ctx)
	}, nil
}

