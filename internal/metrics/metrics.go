// Package metrics declares the Prometheus collectors the Track server
// publishes at /metrics. Keep label cardinality bounded — workspace ID
// is fine (one workspace = one tenant), but never use issue ID or
// arbitrary user-supplied values.
//
// Because the issue counters carry workspace and team ids, /metrics is
// never public: Handler serves it only to a scraper holding the bearer
// token (TRACK_METRICS_TOKEN).
package metrics

import (
	"crypto/sha256"
	"crypto/subtle"
	"net/http"
	"strings"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

var (
	IssuesCreated = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "track_issues_created_total",
			Help: "Total number of issues created, labelled by workspace, team, and creation status.",
		},
		[]string{"workspace", "team", "status"},
	)

	IssuesUpdated = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "track_issues_updated_total",
			Help: "Total number of issue updates, labelled by workspace, team, and new status.",
		},
		[]string{"workspace", "team", "status"},
	)

	APIRequests = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "track_api_requests_total",
			Help: "Total number of API requests, labelled by method, route, and HTTP status.",
		},
		[]string{"method", "path", "status"},
	)

	APILatency = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "track_api_latency_seconds",
			Help:    "API request latency histogram.",
			Buckets: prometheus.DefBuckets,
		},
		[]string{"method", "path"},
	)
)

func init() {
	prometheus.MustRegister(IssuesCreated, IssuesUpdated, APIRequests, APILatency)
}

// Handler returns the /metrics HTTP handler for the default registry, served only to a
// request carrying `Authorization: Bearer <token>`. Anything else is 401. An empty token
// means no scraper is configured, so every request is 401 — without that explicit check
// ConstantTimeCompare("", "") would admit a request that sends no token at all.
func Handler(token string) http.Handler {
	inner := promhttp.Handler()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !authorized(r, token) {
			w.Header().Set("WWW-Authenticate", `Bearer realm="metrics"`)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		inner.ServeHTTP(w, r)
	})
}

func authorized(r *http.Request, token string) bool {
	if token == "" {
		return false
	}
	got, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !ok {
		return false
	}
	g := sha256.Sum256([]byte(got))
	want := sha256.Sum256([]byte(token))
	return subtle.ConstantTimeCompare(g[:], want[:]) == 1
}
