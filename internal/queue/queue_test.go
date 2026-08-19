package queue_test

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"

	"github.com/krizz711/multitenant-saas-on-kubernetes/internal/queue"
)

// newTestQueue runs Redis in-process. The alternative - requiring a container
// - would mean these tests only run where Docker does, and a test that is
// skipped in CI is a test nobody runs.
func newTestQueue(t *testing.T) *queue.Redis {
	t.Helper()
	s := miniredis.RunT(t)
	return queue.NewRedisClient(redis.NewClient(&redis.Options{Addr: s.Addr()}))
}

func TestQueueIsFIFO(t *testing.T) {
	ctx := context.Background()
	q := newTestQueue(t)

	for _, size := range []int{1, 2, 3} {
		if err := q.Enqueue(ctx, queue.Job{Tenant: "tenant-a", Size: size}); err != nil {
			t.Fatalf("enqueue %d: %v", size, err)
		}
	}

	// LPUSH writes to the head and BRPOP reads from the tail, so jobs must come
	// back in the order they went in. Getting this backwards would make the
	// newest job jump the queue and quietly wreck every queue-wait number.
	for _, want := range []int{1, 2, 3} {
		got, err := q.Dequeue(ctx, "tenant-a", time.Second)
		if err != nil {
			t.Fatalf("dequeue: %v", err)
		}
		if got.Size != want {
			t.Fatalf("out of order: got size %d, want %d", got.Size, want)
		}
	}
}

func TestQueuesAreIsolatedPerTenant(t *testing.T) {
	ctx := context.Background()
	q := newTestQueue(t)

	if err := q.Enqueue(ctx, queue.Job{Tenant: "tenant-a", Size: 1}); err != nil {
		t.Fatal(err)
	}

	// tenant-b must not see it. This is the property KEDA scales on: if the
	// keys were shared, scaling tenant-b's workers from tenant-a's backlog
	// would bill the wrong customer and break scale-to-zero for both.
	depth, err := q.Depth(ctx, "tenant-b")
	if err != nil {
		t.Fatal(err)
	}
	if depth != 0 {
		t.Fatalf("tenant-b sees %d jobs from tenant-a", depth)
	}

	if depth, err = q.Depth(ctx, "tenant-a"); err != nil || depth != 1 {
		t.Fatalf("tenant-a depth = %d, err = %v; want 1, nil", depth, err)
	}
}

func TestDequeueOnEmptyQueueReportsEmptyNotError(t *testing.T) {
	ctx := context.Background()
	q := newTestQueue(t)

	_, err := q.Dequeue(ctx, "idle-tenant", 50*time.Millisecond)
	// An idle tenant produces this forever. If it were treated as a failure
	// the worker's error rate would climb whenever nothing was wrong.
	if err != queue.ErrEmpty {
		t.Fatalf("got %v, want queue.ErrEmpty", err)
	}
}

func TestEnqueueStampsIDAndTime(t *testing.T) {
	ctx := context.Background()
	q := newTestQueue(t)

	if err := q.Enqueue(ctx, queue.Job{Tenant: "tenant-a", Size: 5}); err != nil {
		t.Fatal(err)
	}
	got, err := q.Dequeue(ctx, "tenant-a", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if got.ID == "" {
		t.Error("job came back with no id")
	}
	// Without this stamp the worker cannot compute queue wait, which is the
	// metric that prices cold starts under claim C1.
	if got.Enqueued.IsZero() {
		t.Error("job came back with no enqueue time")
	}
}

func TestEnqueueRejectsJobWithNoTenant(t *testing.T) {
	q := newTestQueue(t)
	if err := q.Enqueue(context.Background(), queue.Job{Size: 1}); err == nil {
		t.Fatal("accepted a job with no tenant; it would be unattributable and unbillable")
	}
}
