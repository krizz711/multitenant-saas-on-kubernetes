package gateway

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

// ErrBudgetExceeded is returned when a tenant has spent its allowance for the
// current month. It is a policy decision surfaced as an error, not a fault.
var ErrBudgetExceeded = errors.New("gateway: tenant token budget exceeded")

// Budget enforces a per-tenant monthly ceiling on model tokens.
//
// The check is deliberately BEFORE-the-fact and the accounting is
// after-the-fact, because the output length of a model call is not knowable
// until the call returns. A tenant at 99% of its budget can therefore overshoot
// by one call. That is a real and acknowledged limitation: making it exact
// would require reserving a worst-case output allowance up front and refunding
// the difference, which would reject calls that would have fit. Overshooting by
// at most one response is the cheaper error.
type Budget struct {
	client       *redis.Client
	defaultLimit int64
	perTenant    map[string]int64
}

func NewBudget(client *redis.Client, defaultLimit int64, perTenant map[string]int64) *Budget {
	if perTenant == nil {
		perTenant = map[string]int64{}
	}
	return &Budget{client: client, defaultLimit: defaultLimit, perTenant: perTenant}
}

// Limit is the tenant's allowance. A premium tenant pays for more.
func (b *Budget) Limit(tenant string) int64 {
	if v, ok := b.perTenant[tenant]; ok {
		return v
	}
	return b.defaultLimit
}

// key is scoped to the calendar month so the allowance resets naturally and
// old counters expire without a cleanup job.
func budgetKey(tenant string, now time.Time) string {
	return fmt.Sprintf("budget:%s:%s", tenant, now.UTC().Format("2006-01"))
}

// Check reports whether the tenant may make another call.
func (b *Budget) Check(ctx context.Context, tenant string) error {
	used, err := b.Used(ctx, tenant)
	if err != nil {
		// Failing open is deliberate. A Redis blip should degrade billing
		// accuracy, not take the product offline for every tenant at once.
		// The alternative - failing closed - converts a monitoring problem
		// into an outage.
		return nil
	}
	if used >= b.Limit(tenant) {
		return fmt.Errorf("%w: used %d of %d tokens", ErrBudgetExceeded, used, b.Limit(tenant))
	}
	return nil
}

// Record adds actual usage after a call. A cache hit must NOT be recorded:
// no tokens were bought, and counting them would erase the very saving the
// cache exists to produce.
func (b *Budget) Record(ctx context.Context, tenant string, tokens int64) error {
	if tokens <= 0 {
		return nil
	}
	key := budgetKey(tenant, time.Now())

	pipe := b.client.TxPipeline()
	incr := pipe.IncrBy(ctx, key, tokens)
	// Two months of TTL so a counter read at a month boundary still finds the
	// previous month's value if it is needed for a report.
	pipe.Expire(ctx, key, 62*24*time.Hour)
	if _, err := pipe.Exec(ctx); err != nil {
		return fmt.Errorf("record usage: %w", err)
	}
	_ = incr
	return nil
}

// Used is the tenant's spend so far this month.
func (b *Budget) Used(ctx context.Context, tenant string) (int64, error) {
	v, err := b.client.Get(ctx, budgetKey(tenant, time.Now())).Int64()
	if errors.Is(err, redis.Nil) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("read usage: %w", err)
	}
	return v, nil
}
