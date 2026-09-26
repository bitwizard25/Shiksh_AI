// Package gateway holds the provider gateways (in subpackages) and what every provider gets
// wrapped in: a concurrency limit, a circuit breaker, Prometheus metrics and connection warming.
package gateway

import (
	"errors"

	"github.com/prometheus/client_golang/prometheus"
)

// Metrics are the provider metrics shared by every Guard.
type Metrics struct {
	requests *prometheus.CounterVec
	latency  *prometheus.HistogramVec
	breaker  *prometheus.GaugeVec
}

// NewMetrics creates the provider metrics and registers them on reg (nil: unregistered). When
// they are already registered there, the existing collectors are reused.
func NewMetrics(reg prometheus.Registerer) *Metrics {
	return &Metrics{
		requests: register(reg, prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "shiksha_provider_requests_total",
			Help: "Provider calls by outcome: ok, canceled, client_error, error, rejected.",
		}, []string{"provider", "op", "outcome"})),
		latency: register(reg, prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "shiksha_provider_latency_seconds",
			Help:    "Provider call latency; for streams, the time to the first delta.",
			Buckets: []float64{0.05, 0.1, 0.2, 0.3, 0.45, 0.6, 0.8, 1, 1.5, 2, 3, 5, 8},
		}, []string{"provider", "op"})),
		breaker: register(reg, prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "shiksha_breaker_state",
			Help: "Circuit breaker state per provider: 0 closed, 1 half-open, 2 open.",
		}, []string{"provider"})),
	}
}

func register[C prometheus.Collector](reg prometheus.Registerer, c C) C {
	if reg == nil {
		return c
	}
	if err := reg.Register(c); err != nil {
		var are prometheus.AlreadyRegisteredError
		if errors.As(err, &are) {
			if existing, ok := are.ExistingCollector.(C); ok {
				return existing
			}
		}
		panic(err)
	}
	return c
}
