package gateway_test

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"

	"github.com/krizz711/multitenant-saas-on-kubernetes/controlplane/gateway"
	"github.com/krizz711/multitenant-saas-on-kubernetes/controlplane/model"
)

// countingClient records how many times the gateway actually reached the
// model. Every claim in C2 is a statement about that number.
type countingClient struct {
	calls atomic.Int64
	delay time.Duration
}

func (c *countingClient) Ask(ctx context.Context, tenant, question string) (model.Answer, error) {
	c.calls.Add(1)
	if c.delay > 0 {
		select {
		case <-time.After(c.delay):
		case <-ctx.Done():
			return model.Answer{}, ctx.Err()
		}
	}
	return model.Answer{
		Text:         "answer to " + question,
		PromptTokens: model.EstimateTokens(question),
		OutputTokens: 20,
		Model:        "counting-fake",
	}, nil
}

func newGateway(t *testing.T, inner model.Client, limit int64, threshold float64) *gateway.Gateway {
	t.Helper()
	s := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: s.Addr()})
	return gateway.New(
		inner,
		gateway.NewBudget(rdb, limit, nil),
		gateway.NewCache(rdb, gateway.LexicalEmbedder{}, threshold, 200, time.Hour),
	)
}

func TestRepeatedQuestionIsServedFromCache(t *testing.T) {
	inner := &countingClient{}
	g := newGateway(t, inner, 1_000_000, 0.9)
	ctx := context.Background()

	q := "What does a %GR&R of 18% mean for this gauge?"
	first, err := g.Ask(ctx, "tenant-a", q)
	if err != nil {
		t.Fatal(err)
	}
	if first.Cached {
		t.Error("first answer should not be marked cached")
	}

	second, err := g.Ask(ctx, "tenant-a", q)
	if err != nil {
		t.Fatal(err)
	}
	if !second.Cached {
		t.Error("second identical question should have been served from cache")
	}
	if got := inner.calls.Load(); got != 1 {
		t.Fatalf("model was called %d times; the cache saved nothing", got)
	}
}

// The most important test in this package. A cached answer can contain one
// customer's measurements; serving it to another customer is a data breach
// wearing an optimisation's clothes.
func TestCacheNeverCrossesTenants(t *testing.T) {
	inner := &countingClient{}
	g := newGateway(t, inner, 1_000_000, 0.9)
	ctx := context.Background()

	q := "Why did line 3 drift out of control at 14:20 today?"
	if _, err := g.Ask(ctx, "tenant-a", q); err != nil {
		t.Fatal(err)
	}

	answer, err := g.Ask(ctx, "tenant-b", q)
	if err != nil {
		t.Fatal(err)
	}
	if answer.Cached {
		t.Fatal("tenant-b was served tenant-a's cached answer")
	}
	if got := inner.calls.Load(); got != 2 {
		t.Fatalf("model called %d times; each tenant must cause its own call", got)
	}
}

func TestBudgetStopsATenantAndOnlyThatTenant(t *testing.T) {
	inner := &countingClient{}
	// A tiny allowance: the first call spends well over it.
	g := newGateway(t, inner, 5, 0.99)
	ctx := context.Background()

	if _, err := g.Ask(ctx, "tenant-a", "first question about gauge studies"); err != nil {
		t.Fatalf("first call should succeed - the ceiling is checked before, not during: %v", err)
	}

	_, err := g.Ask(ctx, "tenant-a", "a completely different second question")
	if err == nil {
		t.Fatal("second call should have been refused: the budget is spent")
	}

	// A different tenant must be unaffected. A shared ceiling would recreate
	// the noisy-neighbour problem in the billing dimension.
	if _, err := g.Ask(ctx, "tenant-b", "an unrelated question from another customer"); err != nil {
		t.Fatalf("tenant-b was blocked by tenant-a's spending: %v", err)
	}
}

func TestCacheHitDoesNotConsumeBudget(t *testing.T) {
	inner := &countingClient{}
	g := newGateway(t, inner, 1_000_000, 0.9)
	ctx := context.Background()

	q := "Is 18 percent gauge R and R acceptable?"
	if _, err := g.Ask(ctx, "tenant-a", q); err != nil {
		t.Fatal(err)
	}
	// Ten repeats, all cache hits. If hits were billed, the saving the cache
	// exists to produce would be erased in the accounting.
	for i := 0; i < 10; i++ {
		a, err := g.Ask(ctx, "tenant-a", q)
		if err != nil {
			t.Fatal(err)
		}
		if !a.Cached {
			t.Fatalf("repeat %d missed the cache", i)
		}
	}
	if got := inner.calls.Load(); got != 1 {
		t.Fatalf("model called %d times for 11 identical questions", got)
	}
}

// The case a cache alone cannot solve: many identical prompts in flight at the
// same instant, which is exactly what periodic monitoring agents produce.
func TestConcurrentIdenticalPromptsCollapseToOneCall(t *testing.T) {
	inner := &countingClient{delay: 150 * time.Millisecond}
	g := newGateway(t, inner, 1_000_000, 0.9)
	ctx := context.Background()

	const n = 20
	var wg sync.WaitGroup
	errs := make(chan error, n)

	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := g.Ask(ctx, "tenant-a", "the same scan question every agent asks"); err != nil {
				errs <- err
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}

	if got := inner.calls.Load(); got != 1 {
		t.Fatalf("%d concurrent identical prompts caused %d model calls; want 1", n, got)
	}
}

func TestDistinctQuestionsAreNotCollapsed(t *testing.T) {
	inner := &countingClient{}
	g := newGateway(t, inner, 1_000_000, 0.9)
	ctx := context.Background()

	// Superficially similar, materially different - the benchmark's "distinct"
	// group. A cache that merges these answers the wrong question, which is
	// worse than not caching at all.
	if _, err := g.Ask(ctx, "tenant-a", "What does a %GR&R of 18% mean for this gauge?"); err != nil {
		t.Fatal(err)
	}
	answer, err := g.Ask(ctx, "tenant-a", "What does an ndc of 18 mean for this gauge?")
	if err != nil {
		t.Fatal(err)
	}
	if answer.Cached {
		t.Fatal("a materially different question was served a cached answer")
	}
}
