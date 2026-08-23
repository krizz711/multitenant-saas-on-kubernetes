package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/krizz711/multitenant-saas-on-kubernetes/controlplane/model"
)

// Entry is one cached answer plus the vector it was stored under.
type Entry struct {
	Question string       `json:"question"`
	Answer   model.Answer `json:"answer"`
	Vector   []float32    `json:"vector"`
	Stored   time.Time    `json:"stored"`
}

// Cache holds recent question/answer pairs per tenant and serves a new
// question from them when one is close enough.
//
// Entries are keyed PER TENANT and never shared. This is not a performance
// choice, it is a correctness and confidentiality one: a cached answer may
// contain one customer's measurements, and serving it to another customer
// would be a data breach dressed up as an optimisation. The benchmark in
// experiments/questions.yaml has a whole group devoted to catching exactly
// that mistake.
type Cache struct {
	client       *redis.Client
	embedder     Embedder
	threshold    float64
	maxPerTenant int64
	ttl          time.Duration
}

func NewCache(client *redis.Client, embedder Embedder, threshold float64, maxPerTenant int64, ttl time.Duration) *Cache {
	return &Cache{
		client:       client,
		embedder:     embedder,
		threshold:    threshold,
		maxPerTenant: maxPerTenant,
		ttl:          ttl,
	}
}

func cacheKey(tenant string) string { return "cache:" + tenant }

// Lookup returns the best entry above the similarity threshold, and the score
// it achieved.
//
// The search is a linear scan over the tenant's entries rather than a vector
// index. With a cap of a few hundred entries per tenant a scan is microseconds,
// and it keeps the dependency surface to plain Redis instead of requiring a
// vector database - which matters for a project whose infrastructure budget is
// zero. A production system at a larger scale would need an index, and the
// paper should say so rather than pretending this scales indefinitely.
func (c *Cache) Lookup(ctx context.Context, tenant, question string) (model.Answer, float64, bool) {
	raw, err := c.client.LRange(ctx, cacheKey(tenant), 0, c.maxPerTenant-1).Result()
	if err != nil || len(raw) == 0 {
		return model.Answer{}, 0, false
	}

	query := c.embedder.Embed(question)
	best, bestScore := model.Answer{}, 0.0
	found := false

	for _, item := range raw {
		var e Entry
		if json.Unmarshal([]byte(item), &e) != nil {
			continue
		}
		if score := Cosine(query, e.Vector); score > bestScore {
			bestScore, best, found = score, e.Answer, true
		}
	}

	if !found || bestScore < c.threshold {
		return model.Answer{}, bestScore, false
	}

	best.Cached = true
	return best, bestScore, true
}

// Store adds an answer to the tenant's cache, trimming to the cap.
func (c *Cache) Store(ctx context.Context, tenant, question string, answer model.Answer) error {
	payload, err := json.Marshal(Entry{
		Question: question,
		Answer:   answer,
		Vector:   c.embedder.Embed(question),
		Stored:   time.Now().UTC(),
	})
	if err != nil {
		return fmt.Errorf("marshal cache entry: %w", err)
	}

	key := cacheKey(tenant)
	pipe := c.client.TxPipeline()
	pipe.LPush(ctx, key, payload)
	// Trim rather than evict by age: a fixed cap bounds both memory and the
	// cost of the linear scan above, which is what keeps lookup latency flat.
	pipe.LTrim(ctx, key, 0, c.maxPerTenant-1)
	pipe.Expire(ctx, key, c.ttl)
	_, err = pipe.Exec(ctx)
	return err
}
