// Package telemetry sets up OpenTelemetry tracing and ties the logs to it. Tracing is opt-in: it
// is on only when an OTLP endpoint is configured through the standard OTEL_* environment, and every
// other OTEL_* variable (headers, sampler, resource attributes) is read by the SDK itself.
package telemetry

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log/slog"
	"net/http"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
)

// ServiceName is the service.name resource attribute unless OTEL_SERVICE_NAME overrides it.
const ServiceName = "bestregards"

// Setup installs the global tracer provider and propagator, exporting spans over OTLP/HTTP. The
// returned function flushes pending spans and must be called on shutdown.
func Setup(ctx context.Context, log *slog.Logger) (shutdown func(context.Context) error, err error) {
	exporter, err := otlptracehttp.New(ctx)
	if err != nil {
		return nil, fmt.Errorf("otlp exporter: %w", err)
	}
	res, err := resource.New(ctx,
		resource.WithAttributes(attribute.String("service.name", ServiceName)),
		resource.WithFromEnv(), // OTEL_SERVICE_NAME and OTEL_RESOURCE_ATTRIBUTES win over the default
		resource.WithTelemetrySDK(),
	)
	if err != nil {
		return nil, fmt.Errorf("otel resource: %w", err)
	}
	// The sampler is not set here so that OTEL_TRACES_SAMPLER and OTEL_TRACES_SAMPLER_ARG apply.
	tp := sdktrace.NewTracerProvider(sdktrace.WithBatcher(Redact(exporter)), sdktrace.WithResource(res))
	otel.SetTracerProvider(tp)
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(propagation.TraceContext{}, propagation.Baggage{}))
	otel.SetErrorHandler(otel.ErrorHandlerFunc(func(err error) { log.Warn("opentelemetry", "err", err) }))
	return tp.Shutdown, nil
}

// personal lists the span attributes that identify a visitor. The docs (docs/security.md) promise that IP addresses
// are not kept; HTTP instrumentation records them (and the User-Agent) by default.
var personal = map[attribute.Key]bool{
	"client.address":       true,
	"client.port":          true,
	"network.peer.address": true,
	"network.peer.port":    true,
	"http.client_ip":       true,
	"user_agent.original":  true,
	"http.user_agent":      true,
}

// Redact wraps an exporter so that no span leaves the process with an attribute from personal.
func Redact(e sdktrace.SpanExporter) sdktrace.SpanExporter { return redactingExporter{e} }

type redactingExporter struct{ sdktrace.SpanExporter }

func (e redactingExporter) ExportSpans(ctx context.Context, spans []sdktrace.ReadOnlySpan) error {
	out := make([]sdktrace.ReadOnlySpan, len(spans))
	for i, s := range spans {
		out[i] = redactedSpan{s}
	}
	return e.SpanExporter.ExportSpans(ctx, out)
}

type redactedSpan struct{ sdktrace.ReadOnlySpan }

func (s redactedSpan) Attributes() []attribute.KeyValue {
	attrs := s.ReadOnlySpan.Attributes()
	kept := make([]attribute.KeyValue, 0, len(attrs))
	for _, a := range attrs {
		if !personal[a.Key] {
			kept = append(kept, a)
		}
	}
	return kept
}

// Transport traces outgoing requests to third parties (the AI provider, Cloudflare). It does not
// send trace headers: our trace ids are none of their business. The span records the full URL, so
// it must not carry a secret: the Bot API, whose URL holds the token, is traced by package bot.
func Transport(base http.RoundTripper) http.RoundTripper {
	return otelhttp.NewTransport(base, otelhttp.WithPropagators(propagation.NewCompositeTextMapPropagator()))
}

type requestIDKey struct{}

// WithRequestID returns ctx carrying the id of the request it serves.
func WithRequestID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, requestIDKey{}, id)
}

// RequestID returns the id stored by WithRequestID, or "".
func RequestID(ctx context.Context) string {
	id, _ := ctx.Value(requestIDKey{}).(string)
	return id
}

// NewRequestID names a request. A traced request uses its trace id, so that the id a user reports
// leads straight to the trace; an untraced one gets a random id of the same shape.
func NewRequestID(sc trace.SpanContext) string {
	if sc.IsValid() {
		return sc.TraceID().String()
	}
	var b [16]byte
	rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// LogHandler adds request_id, trace_id and span_id from the context to every record logged with
// one of the ...Context methods, so a log line leads to its request and its trace.
func LogHandler(h slog.Handler) slog.Handler { return logHandler{h} }

type logHandler struct{ slog.Handler }

func (h logHandler) Handle(ctx context.Context, r slog.Record) error {
	id := RequestID(ctx)
	sc := trace.SpanContextFromContext(ctx)
	if id != "" || sc.IsValid() {
		r = r.Clone() // a record's attributes may share memory with the caller's copy
		if id != "" {
			r.AddAttrs(slog.String("request_id", id))
		}
		if sc.IsValid() {
			r.AddAttrs(slog.String("trace_id", sc.TraceID().String()), slog.String("span_id", sc.SpanID().String()))
		}
	}
	return h.Handler.Handle(ctx, r)
}

func (h logHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return logHandler{h.Handler.WithAttrs(attrs)}
}

func (h logHandler) WithGroup(name string) slog.Handler { return logHandler{h.Handler.WithGroup(name)} }
