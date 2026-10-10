// Command worker drains one tenant's queue and rebuilds its product
// similarity graph.
//
// One worker serves one tenant, named by the TENANT environment variable, and
// that is the architecture rather than a simplification. Each tenant gets its
// own Deployment so KEDA can take that Deployment to zero without touching
// anybody else's. A worker serving every tenant could never scale to zero for
// any of them.
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

	"github.com/krizz711/multitenant-saas-on-kubernetes/controlplane/obs"
	"github.com/krizz711/multitenant-saas-on-kubernetes/controlplane/queue"
	"github.com/krizz711/multitenant-saas-on-kubernetes/workload/catalogue"
	"github.com/krizz711/multitenant-saas-on-kubernetes/workload/store"
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

	db := openStore(context.Background(), log)
	defer db.Close()

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
	drain(ctx, q, db, tenant, log)

	log.Info("worker stopping")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = metrics.Shutdown(shutdownCtx)
}

func openStore(ctx context.Context, log *slog.Logger) store.Store {
	url := os.Getenv("DATABASE_URL")
	if url == "" {
		log.Info("no DATABASE_URL set, re-index will compute but not persist")
		return store.Nop{}
	}
	db, err := store.NewPostgres(ctx, url)
	if err != nil {
		log.Error("database unreachable", "err", err)
		return store.Nop{}
	}
	return db
}

// drain is the worker loop. It blocks on the queue rather than polling it, so
// an idle worker consumes no CPU - which is what lets an idle tenant's cost
// actually fall to zero instead of merely looking idle.
func drain(ctx context.Context, q *queue.Redis, db store.Store, tenant string, log *slog.Logger) {
	const blockFor = 5 * time.Second

	for {
		// Checked before the read so a SIGTERM arriving while blocked is not
		// swallowed by the next iteration.
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
			obs.QueueErrors.WithLabelValues(tenant).Inc()

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
		runReindex(ctx, db, tenant, j, log)
	}
}

// runReindex is the CPU-bound half of the workload.
//
// The arithmetic is real: a TF-IDF vector per product and a comparison of
// every product against every other. Claim C3 says a priority class stops
// batch work delaying interactive work, and two processes can only contend
// for a core if both are genuinely using one. A sleep would contend for
// nothing and the experiment would prove nothing.
func runReindex(ctx context.Context, db store.Store, tenant string, j queue.Job, log *slog.Logger) {
	start := time.Now()

	products, err := db.AllProducts(ctx)
	if err != nil {
		log.Error("load catalogue failed", "tenant", tenant, "job", j.ID, "err", err)
		obs.JobsProcessed.WithLabelValues(tenant, "error").Inc()
		return
	}

	edges := catalogue.Reindex(products, j.Size)

	if err := db.ReplaceRelated(ctx, edges); err != nil {
		log.Error("store related failed", "tenant", tenant, "job", j.ID, "err", err)
		obs.JobsProcessed.WithLabelValues(tenant, "error").Inc()
		return
	}

	elapsed := time.Since(start)
	if err := db.RecordReindex(ctx, store.ReindexRun{
		ID:       j.ID,
		Products: len(products),
		Edges:    len(edges),
		Duration: elapsed,
	}); err != nil {
		// The work succeeded and the graph is live; failing to log the run is
		// not worth reporting the job as failed.
		log.Warn("record run failed", "tenant", tenant, "job", j.ID, "err", err)
	}

	obs.JobDuration.WithLabelValues(tenant).Observe(elapsed.Seconds())
	obs.JobsProcessed.WithLabelValues(tenant, "ok").Inc()

	log.Info("reindex done",
		"tenant", tenant, "job", j.ID,
		"products", len(products), "edges", len(edges),
		"took_ms", elapsed.Milliseconds())
}

func envString(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
