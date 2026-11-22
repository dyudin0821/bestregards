package telemetry

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"

	"go.opentelemetry.io/otel/attribute"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
)

func TestLogHandlerTiesRecordsToTheRequestAndTrace(t *testing.T) {
	var buf bytes.Buffer
	log := slog.New(LogHandler(slog.NewJSONHandler(&buf, nil))).With("component", "x")
	sc := trace.NewSpanContext(trace.SpanContextConfig{
		TraceID: trace.TraceID{1}, SpanID: trace.SpanID{2}, TraceFlags: trace.FlagsSampled,
	})
	ctx := WithRequestID(trace.ContextWithSpanContext(context.Background(), sc), NewRequestID(sc))

	log.InfoContext(ctx, "traced")
	log.InfoContext(context.Background(), "plain")

	dec := json.NewDecoder(&buf)
	var traced, plain map[string]any
	if dec.Decode(&traced) != nil || dec.Decode(&plain) != nil {
		t.Fatalf("two JSON lines expected:\n%s", buf.String())
	}
	if traced["request_id"] != sc.TraceID().String() || traced["trace_id"] != sc.TraceID().String() || traced["span_id"] != sc.SpanID().String() || traced["component"] != "x" {
		t.Errorf("traced: %v", traced)
	}
	if _, ok := plain["request_id"]; ok || plain["trace_id"] != nil {
		t.Errorf("plain: %v", plain)
	}
}

func TestUntracedRequestsGetDistinctIDs(t *testing.T) {
	a, b := NewRequestID(trace.SpanContext{}), NewRequestID(trace.SpanContext{})
	if len(a) != 32 || a == b {
		t.Errorf("ids %q %q", a, b)
	}
}

func TestExportedSpansCarryNoVisitorData(t *testing.T) {
	mem := tracetest.NewInMemoryExporter()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSyncer(Redact(mem)))
	_, span := tp.Tracer("t").Start(context.Background(), "GET /api/me")
	span.SetAttributes(
		attribute.String("client.address", "203.0.113.9"),
		attribute.String("network.peer.address", "10.0.0.1"),
		attribute.String("user_agent.original", "Mozilla/5.0"),
		attribute.String("http.route", "GET /api/me"),
	)
	span.End()

	got := mem.GetSpans()
	if len(got) != 1 {
		t.Fatalf("%d spans", len(got))
	}
	var keys []string
	for _, a := range got[0].Attributes {
		keys = append(keys, string(a.Key))
	}
	if strings.Join(keys, ",") != "http.route" {
		t.Errorf("exported attributes %v, want only http.route", keys)
	}
}
