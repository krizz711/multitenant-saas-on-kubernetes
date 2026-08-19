// Package model is the boundary between the workload and whatever answers its
// questions. Everything above it — the API handler, the cost meter, the
// experiments — depends on this interface and not on any particular vendor.
//
// That boundary is the reason Module 7 can slot a budget-checking, caching
// gateway underneath without touching a single handler.
package model

import (
	"context"
	"fmt"
	"math/rand"
	"strings"
	"sync"
	"time"
)

// Answer is what every model call returns, real or stubbed. The token counts
// are not decoration: the cost meter converts them to rupees with a published
// price list, so a call that does not report them cannot be billed.
type Answer struct {
	Text         string `json:"answer"`
	PromptTokens int    `json:"prompt_tokens"`
	OutputTokens int    `json:"output_tokens"`
	Model        string `json:"model"`
	Cached       bool   `json:"cached"`
}

// Client is the only thing a handler is allowed to know about the model.
type Client interface {
	Ask(ctx context.Context, tenant, question string) (Answer, error)
}

// Stub answers without a network call, without an API key and without
// spending money, while still costing realistic wall-clock time and reporting
// realistic token counts.
//
// This is deliberate, not a placeholder to replace later. The project measures
// token *counts* at the gateway and converts them with the published price
// list rather than actually buying tokens, because the free tier is
// rate-limited and a rate-limited dependency in the request path would
// contaminate every latency number in the results.
type Stub struct {
	// Base and Jitter shape the latency this call contributes. Some spread is
	// necessary — a constant latency makes a p95 meaningless — but it must be
	// reproducible, hence the seeded source rather than the global rand.
	Base   time.Duration
	Jitter time.Duration

	mu  sync.Mutex
	rng *rand.Rand
}

// NewStub seeds the generator explicitly so that two runs of the same
// experiment with the same seed produce the same latency profile. Reproducible
// results are worth more marks than realistic ones.
func NewStub(seed int64, base, jitter time.Duration) *Stub {
	return &Stub{Base: base, Jitter: jitter, rng: rand.New(rand.NewSource(seed))}
}

func (s *Stub) Ask(ctx context.Context, tenant, question string) (Answer, error) {
	delay := s.Base
	if s.Jitter > 0 {
		s.mu.Lock()
		delay += time.Duration(s.rng.Int63n(int64(s.Jitter)))
		s.mu.Unlock()
	}

	// Respect cancellation. A client that has given up should stop consuming
	// the pod's time, which matters once these pods are the thing being
	// scaled to zero.
	select {
	case <-time.After(delay):
	case <-ctx.Done():
		return Answer{}, ctx.Err()
	}

	out := fmt.Sprintf("Stubbed answer for %s: %s", tenant, summarise(question))
	return Answer{
		Text:         out,
		PromptTokens: EstimateTokens(question),
		OutputTokens: EstimateTokens(out),
		Model:        "stub-v1",
	}, nil
}

// EstimateTokens is the standard four-characters-per-token approximation.
//
// It is an approximation and the paper must say so. It is used consistently on
// both sides of every comparison, so it cannot flatter the cache result: an
// error in the estimator moves the cached and uncached numbers together.
func EstimateTokens(s string) int {
	n := len(strings.TrimSpace(s))
	if n == 0 {
		return 0
	}
	return (n + 3) / 4
}

func summarise(q string) string {
	q = strings.TrimSpace(q)
	if len(q) <= 60 {
		return q
	}
	return q[:60] + "..."
}
