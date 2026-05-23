// Package metrics declares the Prometheus collectors the Track server
// publishes at /metrics. Keep label cardinality bounded — workspace ID
// is fine (one workspace = one tenant), but never use issue ID or
// arbitrary user-supplied values.
package metrics

import (
	"net/http"

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

// Handler returns the /metrics HTTP handler for the default registry.
func Handler() http.Handler {
	return promhttp.Handler()
}
