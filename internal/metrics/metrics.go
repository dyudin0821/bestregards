// Package metrics exposes the service's health to Prometheus: traffic, latency and errors per
// route, and the failures of the parts behind it. The HTTP metrics have generic names shared with
// other services; which service a series comes from is told by the target labels (job, service)
// that Prometheus attaches at scrape time, never by a label set here. Metrics only this service
// has carry the bestregards_ prefix. Product numbers live in the admin panel and the
// events table; what is here answers "is something broken or slow?" so that the operator's
// monitoring can alert on it. Every method is safe on a nil *Metrics, so code that reports needs
// no checks and tests need no setup.
package metrics

import (
	"crypto/subtle"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// Sources are the numbers that are read from the system when Prometheus scrapes, not counted.
type Sources struct {
	EventsDropped func() int64 // events lost to a full queue or a database failure
}

// Metrics holds the collectors.
type Metrics struct {
	reg         *prometheus.Registry
	requests    *prometheus.CounterVec
	duration    *prometheus.HistogramVec
	rateLimited *prometheus.CounterVec
	aiErrors    prometheus.Counter
}

// durationBuckets reach past the AI call timeout: a translation may legitimately take tens of seconds.
var durationBuckets = []float64{.005, .01, .025, .05, .1, .25, .5, 1, 2.5, 5, 10, 20, 45}

// New registers the metrics on a private registry: the Go runtime and process collectors are left
// out on purpose, the platform already provides them.
func New(src Sources) *Metrics {
	m := &Metrics{
		reg: prometheus.NewRegistry(),

		// Error ratio per route:
		//   sum by (route) (rate(http_requests_total{job="bestregards",code=~"5.."}[5m]))
		//     / sum by (route) (rate(http_requests_total{job="bestregards"}[5m]))
		// Alert: the ratio above 0.05 for 10m, with at least some traffic.
		requests: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "http_requests_total", Help: "Served requests, by route and status code.",
		}, []string{"route", "code"}),

		// P99 latency per route:
		//   histogram_quantile(0.99, sum by (route, le) (rate(http_request_duration_seconds_bucket{job="bestregards"}[5m])))
		// Alert: P99 of "POST /api/translate" above 20s, of any other route above 1s, for 10m.
		duration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name: "http_request_duration_seconds", Help: "Time to serve a request, by route.",
			Buckets: durationBuckets,
		}, []string{"route"}),

		// Flood guard refusals per route; a burst means abuse, a steady stream a limit set too low:
		//   sum by (route) (rate(http_rate_limited_total{job="bestregards"}[5m]))
		rateLimited: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "http_rate_limited_total", Help: "Requests refused by the per-IP flood guard, by route.",
		}, []string{"route"}),

		// Alert: increase(bestregards_ai_errors_total[10m]) >= 5
		aiErrors: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "bestregards_ai_errors_total", Help: "Translation requests that failed because the AI provider failed.",
		}),
	}
	m.reg.MustRegister(m.requests, m.duration, m.rateLimited, m.aiErrors)
	if src.EventsDropped != nil {
		// Alert: increase(bestregards_events_dropped_total[15m]) > 0
		m.reg.MustRegister(prometheus.NewCounterFunc(prometheus.CounterOpts{
			Name: "bestregards_events_dropped_total", Help: "Usage events lost before they were stored.",
		}, func() float64 { return float64(src.EventsDropped()) }))
	}
	return m
}

// Request records a served request. route is the mux pattern, so its values stay few. A non-empty
// traceID is attached to the latency as an exemplar, linking a slow bucket to a trace.
func (m *Metrics) Request(route string, status int, took time.Duration, traceID string) {
	if m == nil {
		return
	}
	m.requests.WithLabelValues(route, strconv.Itoa(status)).Inc()
	obs := m.duration.WithLabelValues(route)
	if eo, ok := obs.(prometheus.ExemplarObserver); ok && traceID != "" {
		eo.ObserveWithExemplar(took.Seconds(), prometheus.Labels{"trace_id": traceID})
		return
	}
	obs.Observe(took.Seconds())
}

// RateLimited counts a request the flood guard refused.
func (m *Metrics) RateLimited(route string) {
	if m != nil {
		m.rateLimited.WithLabelValues(route).Inc()
	}
}

// AIError counts a request that failed because the AI provider did.
func (m *Metrics) AIError() {
	if m != nil {
		m.aiErrors.Inc()
	}
}

// Handler serves the metrics in the Prometheus text format.
func (m *Metrics) Handler() http.Handler {
	return promhttp.HandlerFor(m.reg, promhttp.HandlerOpts{EnableOpenMetrics: true}) // OpenMetrics carries the exemplars
}

// RequireBearer protects next with a static token. An empty token leaves it open, for a port that is
// only reachable from the monitoring system.
func RequireBearer(token string, next http.Handler) http.Handler {
	if token == "" {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got, hasScheme := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		if !hasScheme || subtle.ConstantTimeCompare([]byte(got), []byte(token)) != 1 {
			w.Header().Set("WWW-Authenticate", "Bearer")
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// Server returns the listener that serves /metrics on addr, behind a bearer token when one is set.
// It is separate from the public server so the metrics never share its port.
func (m *Metrics) Server(addr, token string) *http.Server {
	mux := http.NewServeMux()
	mux.Handle("GET /metrics", RequireBearer(token, m.Handler()))
	return &http.Server{
		Addr: addr, Handler: mux, MaxHeaderBytes: 8 << 10,
		ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 10 * time.Second, IdleTimeout: 30 * time.Second,
	}
}
