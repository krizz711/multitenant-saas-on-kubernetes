// Package queue is the hand-off between the API and the worker, and the thing
// KEDA watches to decide how many workers should exist.
//
// One list per tenant, not one shared list. That is the whole reason this
// package exists in the shape it does: KEDA scales a Deployment from the depth
// of a specific key, so a shared queue could only ever scale a shared worker
// pool. Per-tenant keys are what make per-tenant scale-to-zero possible.
package queue

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

// Job is one unit of deferrable work. Enqueued is stamped by the producer so
// the worker can report how long the job waited, which is the number that
// moves when KEDA is too slow to scale or a priority class makes batch work
// yield.
type Job struct {
	ID       string    `json:"id"`
	Tenant   string    `json:"tenant"`
	Size     int       `json:"size"`
	Seed     int64     `json:"seed"`
	Enqueued time.Time `json:"enqueued"`
}

// ErrEmpty means the blocking read timed out with nothing to do. It is an
// ordinary outcome, not a failure: an idle tenant produces it forever.
var ErrEmpty = errors.New("queue: empty")

// Redis is a per-tenant FIFO backed by a Redis list.
type Redis struct {
	client *redis.Client
}

// NewRedis parses a redis:// URL rather than taking a host and port, because
// that is the form the Kubernetes Secret will hold.
func NewRedis(url string) (*Redis, error) {
	opts, err := redis.ParseURL(url)
	if err != nil {
		return nil, fmt.Errorf("parse redis url: %w", err)
	}
	return &Redis{client: redis.NewClient(opts)}, nil
}

// NewRedisClient wraps an existing client. Tests use it to point at miniredis.
func NewRedisClient(c *redis.Client) *Redis { return &Redis{client: c} }

func (r *Redis) Close() error { return r.client.Close() }

// Key is exported because the KEDA ScaledObject has to name the same key this
// code writes to. Two places that must agree, so only one of them defines it.
func Key(tenant string) string { return "q:" + tenant }

// Enqueue pushes to the head. Paired with a tail pop, that makes the list FIFO.
func (r *Redis) Enqueue(ctx context.Context, j Job) error {
	if j.Tenant == "" {
		return errors.New("queue: job has no tenant")
	}
	if j.ID == "" {
		j.ID = NewID()
	}
	if j.Enqueued.IsZero() {
		j.Enqueued = time.Now().UTC()
	}

	payload, err := json.Marshal(j)
	if err != nil {
		return fmt.Errorf("marshal job: %w", err)
	}
	return r.client.LPush(ctx, Key(j.Tenant), payload).Err()
}

// Dequeue blocks until a job arrives or block elapses.
//
// Blocking rather than polling matters more than it looks: a polling worker
// wakes up constantly and burns CPU while idle, which would show up as cost
// against a tenant who is not using the system and quietly undermine the
// scale-to-zero result this project is trying to measure.
func (r *Redis) Dequeue(ctx context.Context, tenant string, block time.Duration) (Job, error) {
	res, err := r.client.BRPop(ctx, block, Key(tenant)).Result()
	if errors.Is(err, redis.Nil) {
		return Job{}, ErrEmpty
	}
	if err != nil {
		return Job{}, fmt.Errorf("brpop: %w", err)
	}
	// BRPOP returns [key, value] because it can watch several keys at once.
	if len(res) != 2 {
		return Job{}, fmt.Errorf("brpop: unexpected reply of length %d", len(res))
	}

	var j Job
	if err := json.Unmarshal([]byte(res[1]), &j); err != nil {
		return Job{}, fmt.Errorf("unmarshal job: %w", err)
	}
	return j, nil
}

// Depth is what KEDA reads to decide how many workers a tenant needs.
func (r *Redis) Depth(ctx context.Context, tenant string) (int64, error) {
	return r.client.LLen(ctx, Key(tenant)).Result()
}

// NewID is a random job id. crypto/rand rather than math/rand: ids end up in
// logs that are compared across tenants, and collisions would be indetectable.
func NewID() string {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		// crypto/rand does not fail in practice; falling back to a timestamp
		// keeps the caller from having to handle an error that cannot happen.
		return fmt.Sprintf("t%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b)
}
