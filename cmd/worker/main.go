// Command worker drains one tenant's queue and runs the CPU-bound analysis.
//
// One worker serves one tenant, named by the TENANT environment variable, and
// that is not a simplification - it is the architecture. Each tenant gets its
// own Deployment so KEDA can take that Deployment to zero without touching
// anybody else's. A worker that served every tenant could never scale to zero
// for any of them.
package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/krizz711/multitenant-saas-on-kubernetes/internal/job"
	"github.com/krizz711/multitenant-saas-on-kubernetes/internal/obs"
	"github.com/krizz711/multitenant-saas-on-kubernetes/internal/queue"
)

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stdout, nil))

	tenant := os.Getenv("TENANT")
	if tenant == "" {
		log.Error("TENANT is required: a worker serves exactly one tenant")
		os.Exit(1)
	}

	q, err := queue.NewRedis(envString("REDIS_URL", "redis://localhost:6379/0"))
	if err != nil {
		log.Error("redis", "err", err)
		os.Exit(1)
	}
	defer func() { _ = q.Close() }()

	// The worker serves no traffic but must still be scraped, so it runs a
	// metrics-only server. Without it every job metric would be invisible.
	metrics := &http.Server{
		Addr:              ":" + envString("METRICS_PORT", "9100"),
		Handler:           promhttp.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
	}
	go func() {
		if err := metrics.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Error("metrics listener failed", "err", err)
		}
	}()

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	log.Info("worker draining", "tenant", tenant, "metrics", metrics.Addr)
	drain(ctx, q, tenant, log)

	log.Info("worker stopping")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = metrics.Shutdown(shutdownCtx)
}

// drain is the worker loop. It blocks on the queue rather than polling it, so
// an idle worker consumes no CPU - which is what lets an idle tenant's cost
// actually fall to zero instead of merely looking idle.
func drain(ctx context.Context, q *queue.Redis, tenant string, log *slog.Logger) {
	const blockFor = 5 * time.Second

	for {
		// Checked before the read so a SIGTERM that arrives while blocked is
		// not silently swallowed by the next iteration.
		if ctx.Err() != nil {
			return
		}

		j, err := q.Dequeue(ctx, tenant, blockFor)
		switch {
		case errors.Is(err, queue.ErrEmpty):
			continue
		case errors.Is(err, context.Canceled):
			return
		case err != nil:
			log.Error("dequeue failed", "tenant", tenant, "err", err)
			obs.JobsProcessed.WithLabelValues(tenant, "error").Inc()

			// Back off rather than spinning: if Redis is down, a tight retry
			// loop turns one outage into a busy loop on every worker at once.
			select {
			case <-time.After(time.Second):
			case <-ctx.Done():
				return
			}
			continue
		}

		obs.JobQueueWait.WithLabelValues(tenant).Observe(time.Since(j.Enqueued).Seconds())

		start := time.Now()
		result := job.Run(j.Size, j.Seed)
		elapsed := time.Since(start)

		obs.JobDuration.WithLabelValues(tenant).Observe(elapsed.Seconds())
		obs.JobsProcessed.WithLabelValues(tenant, "ok").Inc()

		log.Info("job done",
			"tenant", tenant, "job", j.ID,
			"measurements", result.Measurements,
			"gauge_rr", result.GaugeRR,
			"took_ms", elapsed.Milliseconds())
	}
}

func envString(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
