package metrics

import (
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

func TestVoiceMetrics(t *testing.T) {
	reg := prometheus.NewRegistry()
	v := NewVoice(reg)
	v.TurnFirstAudio("hi", "voice", 900*time.Millisecond)
	v.TurnStage("asr", "hi", 300*time.Millisecond)
	v.ClientFirstPlayback("hi", time.Second)
	v.TurnDone("complete")
	v.TurnDone("interrupted")
	v.BargeIn("live")
	v.BargeIn("late")
	v.Continuation()
	v.FillerPlayed()
	v.PersistFailure()
	v.OutboxDropped()
	v.Connections(2)
	v.Connections(-1)

	// 3 histograms with one child each, 2 turn statuses, 2 barge-in kinds, 4 plain counters and the gauge.
	if n := testutil.CollectAndCount(reg); n != 12 {
		t.Fatalf("collected %d series, want 12", n)
	}
	if got := testutil.ToFloat64(v.connections); got != 1 {
		t.Fatalf("connections = %v", got)
	}
	if got := testutil.ToFloat64(v.turns.WithLabelValues("complete")); got != 1 {
		t.Fatalf("turns complete = %v", got)
	}
}
