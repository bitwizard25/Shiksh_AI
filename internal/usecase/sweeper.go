package usecase

import (
	"context"
	"log/slog"
	"time"
)

const sweepBatch = 100

// SweeperDeps are the ports the Sweeper needs.
type SweeperDeps struct {
	Sessions  SessionRepository
	Tickets   TicketRepository
	Tokens    TokenRepository
	Summaries *Summaries // may be nil
	Log       *slog.Logger
}

// Sweeper ends abandoned sessions and deletes expired tokens. Every step is a single conditional
// statement in the database, so several instances can run it at once without doubling the work.
type Sweeper struct {
	d     SweeperDeps
	grace time.Duration
	now   func() time.Time
}

// NewSweeper builds the sweeper. Sessions idle for longer than grace are ended. now defaults to time.Now.
func NewSweeper(d SweeperDeps, grace time.Duration, now func() time.Time) *Sweeper {
	if now == nil {
		now = time.Now
	}
	if d.Log == nil {
		d.Log = slog.Default()
	}
	return &Sweeper{d: d, grace: grace, now: now}
}

// SweepResult counts what one pass did.
type SweepResult struct {
	SessionsEnded int
	TicketsPurged int64
	TokensPurged  int64
}

// Sweep runs one pass. It ends stale sessions (queueing their summaries), then purges expired
// tickets and tokens. It stops at the first error.
func (s *Sweeper) Sweep(ctx context.Context) (SweepResult, error) {
	var res SweepResult
	now := s.now()
	for {
		ids, err := s.d.Sessions.ClaimStale(ctx, now.Add(-s.grace), sweepBatch, now)
		if err != nil {
			return res, err
		}
		for _, id := range ids {
			s.d.Summaries.Submit(id)
		}
		res.SessionsEnded += len(ids)
		if len(ids) < sweepBatch {
			break
		}
	}
	var err error
	if res.TicketsPurged, err = s.d.Tickets.PurgeExpired(ctx, now); err != nil {
		return res, err
	}
	if res.TokensPurged, err = s.d.Tokens.PurgeExpired(ctx, now); err != nil {
		return res, err
	}
	return res, nil
}

// Run sweeps every interval until ctx ends. Errors are logged and the loop carries on.
func (s *Sweeper) Run(ctx context.Context, interval time.Duration) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			res, err := s.Sweep(ctx)
			if err != nil {
				s.d.Log.Error("sweep failed", "err", err)
				continue
			}
			if res.SessionsEnded+int(res.TicketsPurged+res.TokensPurged) > 0 {
				s.d.Log.Info("sweep", "sessions_ended", res.SessionsEnded, "tickets_purged", res.TicketsPurged, "tokens_purged", res.TokensPurged)
			}
		}
	}
}
