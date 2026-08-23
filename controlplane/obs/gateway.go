package obs

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// The gateway's metrics. Claim C2 is argued entirely from these, so each one
// exists to answer a specific question a reviewer will ask.

// ModelTokens is the raw material of the cost meter. Split by kind because
// providers price prompt and output tokens differently, so a single total
// could not be converted to rupees.
//
// Only tokens actually bought are counted here. A cache hit must never
// increment it: counting avoided tokens as spend would erase the exact saving
// the cache exists to produce.
var ModelTokens = promauto.NewCounterVec(
	prometheus.CounterOpts{
		Name: "model_tokens_total",
		Help: "Tokens actually purchased from the model, by tenant and kind.",
	},
	[]string{"tenant", "kind"},
)

// TokensAvoided is the other half of the story: what the cache and the
// coalescer saved. Reported separately rather than netted off, so the saving
// is auditable instead of being an inference from a smaller bill.
var TokensAvoided = promauto.NewCounterVec(
	prometheus.CounterOpts{
		Name: "model_tokens_avoided_total",
		Help: "Tokens not purchased because a cache hit or coalesced call served the request.",
	},
	[]string{"tenant", "reason"},
)

// CacheLookups is the hit rate, which is the headline number for C2.
var CacheLookups = promauto.NewCounterVec(
	prometheus.CounterOpts{
		Name: "gateway_cache_lookups_total",
		Help: "Cache lookups by outcome.",
	},
	[]string{"tenant", "result"},
)

// CacheSimilarity records the best score of every lookup, hit or miss.
//
// This is what makes the similarity threshold defensible rather than arbitrary.
// The distribution shows where paraphrases and genuinely different questions
// actually fall, so the chosen cut-off can be justified from data instead of
// picked because it produced a pleasing hit rate.
var CacheSimilarity = promauto.NewHistogramVec(
	prometheus.HistogramOpts{
		Name:    "gateway_cache_similarity",
		Help:    "Best cosine similarity found per lookup.",
		Buckets: []float64{0.1, 0.2, 0.3, 0.4, 0.5, 0.6, 0.7, 0.8, 0.9, 0.95, 0.99, 1.0},
	},
	[]string{"tenant"},
)

// BudgetRejections counts requests refused for being over the monthly ceiling.
var BudgetRejections = promauto.NewCounterVec(
	prometheus.CounterOpts{
		Name: "gateway_budget_rejections_total",
		Help: "Requests refused because the tenant exhausted its token budget.",
	},
	[]string{"tenant"},
)
