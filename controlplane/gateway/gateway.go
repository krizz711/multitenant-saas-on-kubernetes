package gateway

import (
	"context"
	"errors"
	"strings"
	"time"

	"golang.org/x/sync/singleflight"

	"github.com/krizz711/multitenant-saas-on-kubernetes/controlplane/model"
	"github.com/krizz711/multitenant-saas-on-kubernetes/controlplane/obs"
)

// Gateway is the only route to the model. It implements model.Client, so the
// API handlers are unchanged from Module 2 - the whole point of putting an
// interface at that boundary.
//
// Three mechanisms, applied in a deliberate order:
//
//  1. Budget check. Cheapest, and a tenant over its ceiling should not consume
//     a cache lookup either.
//  2. Cache lookup. Avoids the call entirely when a near-duplicate was asked
//     recently.
//  3. Coalescing. Collapses identical prompts that are in flight AT THE SAME
//     MOMENT into one upstream call. The cache cannot do this: a cache only
//     helps once the first answer has come back, and the monitoring agents in
//     the motivating system fire many identical scans simultaneously - the
//     exact case where every one of them misses the cache and every one of
//     them is billed.
type Gateway struct {
	inner  model.Client
	budget *Budget
	cache  *Cache
	group  singleflight.Group
}

func New(inner model.Client, budget *Budget, cache *Cache) *Gateway {
	return &Gateway{inner: inner, budget: budget, cache: cache}
}

func (g *Gateway) Ask(ctx context.Context, tenant, question string) (model.Answer, error) {
	if err := g.budget.Check(ctx, tenant); err != nil {
		if errors.Is(err, ErrBudgetExceeded) {
			obs.BudgetRejections.WithLabelValues(tenant).Inc()
		}
		return model.Answer{}, err
	}

	if answer, score, ok := g.cache.Lookup(ctx, tenant, question); ok {
		obs.CacheLookups.WithLabelValues(tenant, "hit").Inc()
		obs.CacheSimilarity.WithLabelValues(tenant).Observe(score)
		obs.TokensAvoided.WithLabelValues(tenant, "cache").
			Add(float64(answer.PromptTokens + answer.OutputTokens))
		return answer, nil
	} else {
		obs.CacheLookups.WithLabelValues(tenant, "miss").Inc()
		obs.CacheSimilarity.WithLabelValues(tenant).Observe(score)
	}

	// Keyed by tenant as well as prompt. Sharing an in-flight call between two
	// tenants would leak one customer's answer to another, which is the same
	// breach the per-tenant cache exists to prevent - and far easier to do by
	// accident here.
	key := tenant + "\x00" + normalise(question)

	result, err, shared := g.group.Do(key, func() (any, error) {
		answer, err := g.inner.Ask(ctx, tenant, question)
		if err != nil {
			return nil, err
		}

		obs.ModelTokens.WithLabelValues(tenant, "prompt").Add(float64(answer.PromptTokens))
		obs.ModelTokens.WithLabelValues(tenant, "output").Add(float64(answer.OutputTokens))

		// Record spend and populate the cache with a context that survives the
		// caller giving up. If the original request is cancelled mid-flight,
		// the tokens were still bought and must still be billed - otherwise a
		// tenant could escape its budget by cancelling.
		bg, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
		defer cancel()

		_ = g.budget.Record(bg, tenant, int64(answer.PromptTokens+answer.OutputTokens))
		_ = g.cache.Store(bg, tenant, question, answer)

		return answer, nil
	})
	if err != nil {
		return model.Answer{}, err
	}

	answer := result.(model.Answer)
	if shared {
		// This caller rode along on somebody else's upstream call.
		obs.TokensAvoided.WithLabelValues(tenant, "coalesced").
			Add(float64(answer.PromptTokens + answer.OutputTokens))
	}
	return answer, nil
}

// normalise makes coalescing insensitive to whitespace and case, which are the
// differences that carry no meaning. It is deliberately NOT the same as the
// embedding used for the cache: coalescing must only ever merge prompts that
// are genuinely identical, because it returns one answer to several callers.
func normalise(s string) string {
	return strings.Join(strings.Fields(strings.ToLower(s)), " ")
}
