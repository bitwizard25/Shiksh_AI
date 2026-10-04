// Package metrics exposes the realtime Prometheus collectors (spec §14).
package metrics

import (
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/bitwizard25/Shiksh_AI/internal/usecase/conversation"
)

// Buckets are tuned around the 1.5 s p50 / 2.5 s p95 first-audio objective.
var latencyBuckets = []float64{0.1, 0.25, 0.5, 0.75, 1, 1.25, 1.5, 2, 2.5, 3, 4, 6, 10}

// Voice implements conversation.Metrics and the outbox's drop counter.
type Voice struct {
	firstAudio     *prometheus.HistogramVec
	clientPlayback *prometheus.HistogramVec
	stage          *prometheus.HistogramVec
	turns          *prometheus.CounterVec
	bargeIns       *prometheus.CounterVec
	continuations  prometheus.Counter
	fillers        prometheus.Counter
	persistFails   prometheus.Counter
	connections    prometheus.Gauge
	outboxDropped  prometheus.Counter
}

var _ conversation.Metrics = (*Voice)(nil)

// NewVoice creates the collectors and registers them with reg. Registering twice on one registry panics, as usual.
func NewVoice(reg prometheus.Registerer) *Voice {
	v := &Voice{
		firstAudio: prometheus.NewHistogramVec(prometheus.HistogramOpts{Name: "shiksha_turn_first_audio_seconds",
			Help: "Time from the end of the learner's speech to the first tutor audio queued (the SLO).", Buckets: latencyBuckets}, []string{"lang", "input_mode"}),
		clientPlayback: prometheus.NewHistogramVec(prometheus.HistogramOpts{Name: "shiksha_client_first_playback_seconds",
			Help: "Time from the end of speech to the client starting playback.", Buckets: latencyBuckets}, []string{"lang"}),
		stage: prometheus.NewHistogramVec(prometheus.HistogramOpts{Name: "shiksha_turn_stage_seconds",
			Help: "Time from the end of speech until a stage finished.", Buckets: latencyBuckets}, []string{"stage", "lang"}),
		turns:         prometheus.NewCounterVec(prometheus.CounterOpts{Name: "shiksha_turns_total", Help: "Finished turns by status."}, []string{"status"}),
		bargeIns:      prometheus.NewCounterVec(prometheus.CounterOpts{Name: "shiksha_bargeins_total", Help: "Learner interruptions by kind (live or late)."}, []string{"kind"}),
		continuations: prometheus.NewCounter(prometheus.CounterOpts{Name: "shiksha_continuations_total", Help: "Turns merged because the learner only paused."}),
		fillers:       prometheus.NewCounter(prometheus.CounterOpts{Name: "shiksha_filler_played_total", Help: "Filler clips played."}),
		persistFails:  prometheus.NewCounter(prometheus.CounterOpts{Name: "shiksha_persist_failures_total", Help: "Turn writes that failed after retries."}),
		connections:   prometheus.NewGauge(prometheus.GaugeOpts{Name: "shiksha_ws_connections", Help: "Open WebSocket sessions."}),
		outboxDropped: prometheus.NewCounter(prometheus.CounterOpts{Name: "shiksha_outbox_dropped_total", Help: "Stale frames dropped before being written."}),
	}
	reg.MustRegister(v.firstAudio, v.clientPlayback, v.stage, v.turns, v.bargeIns, v.continuations, v.fillers, v.persistFails, v.connections, v.outboxDropped)
	return v
}

func (v *Voice) TurnFirstAudio(lang, mode string, d time.Duration) {
	v.firstAudio.WithLabelValues(lang, mode).Observe(d.Seconds())
}
func (v *Voice) TurnStage(stage, lang string, d time.Duration) {
	v.stage.WithLabelValues(stage, lang).Observe(d.Seconds())
}
func (v *Voice) TurnDone(status string) { v.turns.WithLabelValues(status).Inc() }
func (v *Voice) ClientFirstPlayback(lang string, d time.Duration) {
	v.clientPlayback.WithLabelValues(lang).Observe(d.Seconds())
}
func (v *Voice) BargeIn(kind string)   { v.bargeIns.WithLabelValues(kind).Inc() }
func (v *Voice) Continuation()         { v.continuations.Inc() }
func (v *Voice) FillerPlayed()         { v.fillers.Inc() }
func (v *Voice) PersistFailure()       { v.persistFails.Inc() }
func (v *Voice) Connections(delta int) { v.connections.Add(float64(delta)) }

// OutboxDropped counts a stale frame the outbox dropped.
func (v *Voice) OutboxDropped() { v.outboxDropped.Inc() }
