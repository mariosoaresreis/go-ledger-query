// Package observability centralises Prometheus metric definitions for the
// ledger-query service. All metrics are registered against a custom registry
// so the process-level default registry is not polluted in tests.
package observability

import (
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// Registry is the module-level Prometheus registry.
var Registry = prometheus.NewRegistry()

// ── HTTP metrics ──────────────────────────────────────────────────────────────

var (
	httpRequestsTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: "ledger_query",
		Name:      "http_requests_total",
		Help:      "Total number of HTTP requests by method, path and status code.",
	}, []string{"method", "path", "status"})

	httpRequestDuration = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Namespace: "ledger_query",
		Name:      "http_request_duration_seconds",
		Help:      "HTTP request latency distributions.",
		Buckets:   prometheus.DefBuckets,
	}, []string{"method", "path"})
)

// ── Kafka metrics ─────────────────────────────────────────────────────────────

var (
	// KafkaEventsProcessed counts events that were dispatched successfully.
	KafkaEventsProcessed = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: "ledger_query",
		Name:      "kafka_events_processed_total",
		Help:      "Total Kafka events successfully projected, by event type.",
	}, []string{"event_type"})

	// KafkaEventsFailed counts dispatch failures (before retry / DLQ).
	KafkaEventsFailed = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: "ledger_query",
		Name:      "kafka_events_failed_total",
		Help:      "Total Kafka event dispatch failures (all retry attempts), by event type.",
	}, []string{"event_type"})

	// KafkaEventsDLQ counts messages routed to the dead-letter topic.
	KafkaEventsDLQ = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: "ledger_query",
		Name:      "kafka_events_dlq_total",
		Help:      "Total messages forwarded to the DLQ after exceeding max retries.",
	}, []string{"event_type"})
)

func init() {
	Registry.MustRegister(
		// Go runtime metrics
		collectors.NewGoCollector(),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),

		// HTTP
		httpRequestsTotal,
		httpRequestDuration,

		// Kafka
		KafkaEventsProcessed,
		KafkaEventsFailed,
		KafkaEventsDLQ,
	)
}

// MetricsHandler returns an HTTP handler that exposes the custom registry
// in the Prometheus text exposition format.
func MetricsHandler() http.Handler {
	return promhttp.HandlerFor(Registry, promhttp.HandlerOpts{EnableOpenMetrics: true})
}

// GinMetricsMiddleware records HTTP request counts and latencies.
// Register it on the gin engine before mounting routes.
func GinMetricsMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()
		c.Next()
		path := c.FullPath()
		if path == "" {
			path = "unknown"
		}
		status := strconv.Itoa(c.Writer.Status())
		httpRequestsTotal.WithLabelValues(c.Request.Method, path, status).Inc()
		httpRequestDuration.WithLabelValues(c.Request.Method, path).
			Observe(time.Since(start).Seconds())
	}
}

