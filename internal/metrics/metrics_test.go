package metrics

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func scrape(t *testing.T, m *Metrics) string {
	t.Helper()
	rec := httptest.NewRecorder()
	m.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/metrics", nil))
	if rec.Code != 200 {
		t.Fatalf("status %d", rec.Code)
	}
	return rec.Body.String()
}

func TestMetricsExposeTheServiceSeriesOnly(t *testing.T) {
	m := New(Sources{EventsDropped: func() int64 { return 7 }})
	m.Request("POST /api/translate", 200, 3*time.Second, "")
	m.Request("POST /api/translate", 502, 30*time.Second, "")
	m.Request("POST /bot/webhook", 500, time.Millisecond, "")
	m.RateLimited("POST /api/translate")
	m.AIError()

	out := scrape(t, m)
	for _, want := range []string{
		`http_requests_total{code="200",route="POST /api/translate"} 1`,
		`http_requests_total{code="502",route="POST /api/translate"} 1`,
		`http_request_duration_seconds_bucket{route="POST /api/translate",le="2.5"} 0`,
		`http_request_duration_seconds_bucket{route="POST /api/translate",le="5"} 1`,
		`http_request_duration_seconds_bucket{route="POST /api/translate",le="45"} 2`,
		`http_requests_total{code="500",route="POST /bot/webhook"} 1`,
		`http_rate_limited_total{route="POST /api/translate"} 1`,
		`bestregards_ai_errors_total 1`,
		`bestregards_events_dropped_total 7`,
	} {
		if !strings.Contains(out, want+"\n") {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	for _, unwanted := range []string{"go_goroutines", "process_cpu", "service=", "bestregards_http_"} {
		if strings.Contains(out, unwanted) {
			t.Errorf("runtime metrics and the service label come from the platform, found %q", unwanted)
		}
	}
}

func TestLatencyCarriesTheTraceAsAnExemplar(t *testing.T) {
	m := New(Sources{})
	m.Request("GET /api/me", 200, 30*time.Millisecond, "4bf92f3577b34da6a3ce929d0e0e4736")
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/metrics", nil)
	req.Header.Set("Accept", "application/openmetrics-text; version=1.0.0")
	m.Handler().ServeHTTP(rec, req)
	if !strings.Contains(rec.Body.String(), `le="0.05"} 1 # {trace_id="4bf92f3577b34da6a3ce929d0e0e4736"} 0.03`) {
		t.Errorf("no exemplar in:\n%s", rec.Body)
	}
}

func TestNilMetricsAreSafe(t *testing.T) {
	var m *Metrics
	m.Request("x", 500, time.Second, "t")
	m.RateLimited("x")
	m.AIError()
}

func TestRequireBearer(t *testing.T) {
	ok := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.Write([]byte("metrics")) })
	call := func(h http.Handler, auth string) int {
		r := httptest.NewRequest("GET", "/metrics", nil)
		if auth != "" {
			r.Header.Set("Authorization", auth)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, r)
		return rec.Code
	}
	guarded := RequireBearer("s3cret-token-value", ok)
	for auth, want := range map[string]int{"": 401, "Bearer wrong": 401, "s3cret-token-value": 401, "Bearer s3cret-token-value": 200} {
		if got := call(guarded, auth); got != want {
			t.Errorf("%q: %d, want %d", auth, got, want)
		}
	}
	if got := call(RequireBearer("", ok), ""); got != 200 {
		t.Errorf("an empty token leaves the port open for the monitoring network: %d", got)
	}
}

func TestServerServesOnlyMetrics(t *testing.T) {
	m := New(Sources{})
	srv := m.Server("127.0.0.1:0", "")
	for path, want := range map[string]int{"/metrics": 200, "/": 404, "/debug/pprof/": 404} {
		rec := httptest.NewRecorder()
		srv.Handler.ServeHTTP(rec, httptest.NewRequest("GET", path, nil))
		if rec.Code != want {
			t.Errorf("%s: %d, want %d", path, rec.Code, want)
		}
	}
	rec := httptest.NewRecorder()
	srv.Handler.ServeHTTP(rec, httptest.NewRequest("POST", "/metrics", nil))
	if rec.Code == 200 {
		t.Error("metrics are read-only")
	}
}
