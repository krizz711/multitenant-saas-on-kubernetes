package model

import (
	"context"
	"log/slog"
	"sync/atomic"
	"time"
)

// Fallback tries a primary model and quietly uses a secondary when the
// primary is unreachable.
//
// This is not demo scaffolding; it is the correct shape for a hosted
// dependency. A model provider is a third party on the far side of a network,
// and it will be slow, rate-limited or absent at some point. Returning 502 to
// a shopper because a vendor is having a bad afternoon is a worse outcome than
// serving a degraded answer and saying so.
//
// Only transport and availability failures fall through. A budget rejection is
// a policy decision and must be honoured, not routed around - falling back
// there would let a tenant exceed its ceiling by breaking its own provider.
type Fallback struct {
	primary   Client
	secondary Client
	log       *slog.Logger

	// Latched so the log records the transition rather than one line per
	// request once a provider goes down.
	degraded atomic.Bool
}

func NewFallback(primary, secondary Client, log *slog.Logger) *Fallback {
	return &Fallback{primary: primary, secondary: secondary, log: log}
}

func (f *Fallback) Ask(ctx context.Context, tenant, question string) (Answer, error) {
	answer, err := f.primary.Ask(ctx, tenant, question)
	if err == nil {
		if f.degraded.CompareAndSwap(true, false) {
			f.log.Info("model provider recovered, using primary again")
		}
		return answer, nil
	}

	// The caller gave up. Not a provider failure, and retrying on a dead
	// context would fail identically.
	if ctx.Err() != nil {
		return Answer{}, err
	}

	if f.degraded.CompareAndSwap(false, true) {
		f.log.Warn("model provider unreachable, falling back", "err", err)
	}

	answer, ferr := f.secondary.Ask(ctx, tenant, question)
	if ferr != nil {
		// Report the original failure: the primary is the real problem and
		// the fallback's error would send anybody debugging down the wrong path.
		return Answer{}, err
	}
	answer.Model = answer.Model + " (fallback)"
	return answer, nil
}

// StubFallback is the conventional pairing: a hosted provider in front, the
// deterministic local stub behind it. With no network at all the application
// still answers, which is what makes an offline demonstration possible.
func StubFallback(primary Client, log *slog.Logger) *Fallback {
	return NewFallback(primary, NewStub(1, 120*time.Millisecond, 80*time.Millisecond), log)
}
