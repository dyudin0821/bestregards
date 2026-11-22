package server

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"github.com/dyudin0821/bestregards/internal/metrics"
	"github.com/dyudin0821/bestregards/internal/telemetry"
)

type observed struct {
	s     *Server
	logs  *bytes.Buffer
	spans *tracetest.SpanRecorder
	m     *metrics.Metrics
}

// newObservedServer is the test server with metrics, a recording tracer and the production log
// handler writing JSON into a buffer.
func newObservedServer(t *testing.T) observed {
	t.Helper()
	s, _ := newTestServerWith(t, true)
	o := observed{logs: &bytes.Buffer{}, spans: tracetest.NewSpanRecorder(), m: metrics.New(metrics.Sources{})}
	opts := s.Options
	opts.Metrics = o.m
	opts.Logger = slog.New(telemetry.LogHandler(slog.NewJSONHandler(o.logs, nil)))
	opts.TracerProvider = sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(o.spans))
	o.s = New(opts)
	return o
}

func (o observed) scrape(t *testing.T) string {
	t.Helper()
	rec := httptest.NewRecorder()
	o.m.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/metrics", nil))
	return rec.Body.String()
}

func TestRequestIsTracedCountedAndLoggedUnderItsRoute(t *testing.T) {
	o := newObservedServer(t)
	rec := do(o.s, "POST", "/api/translate", `{}`)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status %d", rec.Code)
	}

	spans := o.spans.Ended()
	if len(spans) != 1 || spans[0].Name() != "POST /api/translate" {
		t.Fatalf("spans: %v", spans)
	}
	traceID := spans[0].SpanContext().TraceID().String()
	if got := rec.Header().Get("X-Request-Id"); got != traceID {
		t.Errorf("X-Request-Id %q, want the trace id %q", got, traceID)
	}

	var line map[string]any
	if err := json.Unmarshal(o.logs.Bytes(), &line); err != nil {
		t.Fatalf("one JSON access log line expected: %v\n%s", err, o.logs)
	}
	if line["route"] != "POST /api/translate" || line["request_id"] != traceID || line["trace_id"] != traceID {
		t.Errorf("access log is not tied to the request: %v", line)
	}

	out := o.scrape(t)
	for _, want := range []string{
		`http_requests_total{code="401",route="POST /api/translate"} 1`,
		`http_request_duration_seconds_count{route="POST /api/translate"} 1`,
	} {
		if !strings.Contains(out, want+"\n") {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
}

func TestProbesAreNotTraced(t *testing.T) {
	o := newObservedServer(t)
	do(o.s, "GET", "/healthz", "")
	do(o.s, "GET", "/readyz", "")
	if n := len(o.spans.Ended()); n != 0 {
		t.Errorf("%d spans for probes", n)
	}
}

func TestPanicBecomesA500WithAStack(t *testing.T) {
	o := newObservedServer(t)
	o.s.mux.HandleFunc("GET /boom", func(http.ResponseWriter, *http.Request) { panic("kaboom") })

	rec := do(o.s, "GET", "/boom", "")
	if rec.Code != http.StatusInternalServerError || !strings.Contains(rec.Body.String(), `"error"`) {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	if logs := o.logs.String(); !strings.Contains(logs, `"msg":"panic"`) || !strings.Contains(logs, "kaboom") || !strings.Contains(logs, "goroutine") || !strings.Contains(logs, `"request_id"`) {
		t.Errorf("panic log lacks the value, the stack or the request id:\n%s", logs)
	}
	if out := o.scrape(t); !strings.Contains(out, `http_requests_total{code="500",route="GET /boom"} 1`) {
		t.Errorf("panic not counted:\n%s", out)
	}
	if ev := o.spans.Ended()[0].Events(); len(ev) == 0 || ev[0].Name != "exception" {
		t.Errorf("panic not recorded on the span: %v", ev)
	}
}

func TestPanicAfterTheResponseBeganDoesNotWriteAgain(t *testing.T) {
	o := newObservedServer(t)
	o.s.mux.HandleFunc("GET /half", func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte("partial"))
		panic("late")
	})
	rec := do(o.s, "GET", "/half", "")
	if rec.Body.String() != "partial" {
		t.Errorf("body %q: the error must not be appended to a started response", rec.Body)
	}
	if out := o.scrape(t); !strings.Contains(out, `http_requests_total{code="500",route="GET /half"} 1`) {
		t.Errorf("a broken response must count as a 500:\n%s", out)
	}
}

func TestAbortHandlerPanicReachesNetHTTP(t *testing.T) {
	o := newObservedServer(t)
	o.s.mux.HandleFunc("GET /abort", func(http.ResponseWriter, *http.Request) { panic(http.ErrAbortHandler) })
	defer func() {
		if p := recover(); p != http.ErrAbortHandler {
			t.Errorf("recovered %v, want http.ErrAbortHandler", p)
		}
		if strings.Contains(o.logs.String(), `"msg":"panic"`) {
			t.Error("an aborted response is not a bug and must not be logged as a panic")
		}
	}()
	do(o.s, "GET", "/abort", "")
}

func TestRateLimitRefusalsAreCountedByRoute(t *testing.T) {
	o := newObservedServer(t)
	for range 15 {
		do(o.s, "POST", "/api/auth/telegram", "x")
	}
	if out := o.scrape(t); !strings.Contains(out, `http_rate_limited_total{route="POST /api/auth/telegram"} 5`) {
		t.Errorf("refusals not counted:\n%s", out)
	}
}

func TestRouteOfKeepsLabelsBounded(t *testing.T) {
	o := newObservedServer(t)
	do(o.s, "GET", "/api/nope/123", "")
	do(o.s, "POST", "/healthz", "")
	out := o.scrape(t)
	for _, want := range []string{
		`http_requests_total{code="404",route="GET /api/"} 1`, // the catch-all, not the path
		`http_requests_total{code="405",route="unmatched"} 1`,
	} {
		if !strings.Contains(out, want+"\n") {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
}

func TestTranslateSpanTellsTheOutcomeButNotTheText(t *testing.T) {
	o := newObservedServer(t)
	token := bearer(t, o.s, 42)
	postTranslate(o.s, token, `{"text":"секретный текст","mode":"`+o.s.Catalog.Modes[0].ID+`","level":2}`)

	var span sdktrace.ReadOnlySpan
	for _, s := range o.spans.Ended() {
		if s.Name() == "POST /api/translate" {
			span = s
		}
	}
	if span == nil {
		t.Fatal("no request span")
	}
	attrs := map[string]string{}
	for _, a := range span.Attributes() {
		attrs[string(a.Key)] = a.Value.String()
		if strings.Contains(a.Value.String(), "секретный") {
			t.Errorf("%s carries the text", a.Key)
		}
	}
	if attrs["translate.outcome"] != "ai" || attrs["translate.level"] != "2" {
		t.Errorf("attributes %v", attrs)
	}
}
