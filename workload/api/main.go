// Command api is the workload's front door: a cheap read, an expensive model
// call, and a job hand-off. It is deliberately the least interesting code in
// this repository.
//
// The application is frozen once it works. Every feature after this point
// belongs to the control plane, and any new endpoint here is a sign that
// effort has drifted away from the actual project.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/krizz711/multitenant-saas-on-kubernetes/controlplane/admission"
	"github.com/krizz711/multitenant-saas-on-kubernetes/controlplane/model"
	"github.com/krizz711/multitenant-saas-on-kubernetes/controlplane/obs"
	"github.com/krizz711/multitenant-saas-on-kubernetes/controlplane/queue"
	"github.com/krizz711/multitenant-saas-on-kubernetes/workload/analysis"
)

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stdout, nil))

	llm := model.NewStub(
		envInt64("MODEL_SEED", 1),
		envDuration("MODEL_BASE_LATENCY", 120*time.Millisecond),
		envDuration("MODEL_JITTER", 80*time.Millisecond),
	)

	// The queue client is built even if Redis is unreachable. Dialling is
	// lazy, so an API pod that starts before Redis is ready comes up and
	// serves /summary and /ask; only /analyze fails, and it fails with a 503
	// that says so. Refusing to start would take out two working endpoints
	// because a third one's dependency was slow.
	q, err := queue.NewRedis(envString("REDIS_URL", "redis://localhost:6379/0"))
	if err != nil {
		log.Error("redis url", "err", err)
		os.Exit(1)
	}
	defer func() { _ = q.Close() }()

	srv := &http.Server{
		Addr:    ":" + envString("PORT", "8000"),
		Handler: routes(llm, q, log),

		// A read timeout bounds how long a slow client can hold a connection.
		// Without it a handful of stalled connections can pin a pod open that
		// the autoscaler is trying to drain.
		ReadHeaderTimeout: 5 * time.Second,
	}

	// Shut down on SIGTERM rather than dying on it. Kubernetes sends SIGTERM
	// and then waits before SIGKILL; a server that finishes its in-flight
	// requests in that window is the difference between a clean scale-down and
	// a spike of 502s in the results. This matters directly to claim C1.
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	go func() {
		log.Info("api listening", "addr", srv.Addr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Error("listen failed", "err", err)
			os.Exit(1)
		}
	}()

	<-ctx.Done()
	log.Info("shutting down")

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Error("graceful shutdown failed", "err", err)
	}
}

// routes builds the two-level router.
//
// The split is not cosmetic. admission.Middleware rejects any request without
// an X-Tenant header, and Prometheus scrapes /metrics without one — so the
// infrastructure endpoints must sit *outside* admission or the project loses
// its data source. Kubernetes probes /healthz for the same reason.
//
// Inside, admission wraps obs, never the other way round: obs labels its
// histogram from the tenant and class that admission puts on the context, so
// reversing them silently labels every metric "unknown".
func routes(llm model.Client, q *queue.Redis, log *slog.Logger) http.Handler {
	app := http.NewServeMux()
	app.HandleFunc("GET /summary", handleSummary)
	app.HandleFunc("POST /ask", handleAsk(llm, log))
	app.HandleFunc("POST /analyze", handleAnalyze(q, log))

	root := http.NewServeMux()
	root.Handle("GET /metrics", promhttp.Handler())
	root.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	root.Handle("/", admission.Middleware(obs.Middleware(app)))

	return root
}

// handleSummary is the cheap interactive read: the request the latency
// objective is written about, and the one that must stay fast while a batch
// job is running on the same node.
func handleSummary(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"tenant":     admission.Tenant(r.Context()),
		"class":      string(admission.Of(r.Context())),
		"series":     12,
		"open_flags": 3,
		"as_of":      time.Now().UTC().Format(time.RFC3339),
	})
}

// handleAsk is the expensive-in-money path. It is expensive in tokens rather
// than CPU, which is why capping it needs a gateway rather than a CPU limit.
func handleAsk(llm model.Client, log *slog.Logger) http.HandlerFunc {
	type request struct {
		Question string `json:"question"`
	}

	return func(w http.ResponseWriter, r *http.Request) {
		var req request
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10)).Decode(&req); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "body must be {\"question\": \"...\"}"})
			return
		}
		if req.Question == "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "question must not be empty"})
			return
		}

		tenant := admission.Tenant(r.Context())
		answer, err := llm.Ask(r.Context(), tenant, req.Question)
		if err != nil {
			// A cancelled request is the client's decision, not a server
			// fault, and must not be counted as one in the error rate.
			if errors.Is(err, context.Canceled) {
				return
			}
			log.Error("model call failed", "tenant", tenant, "err", err)
			writeJSON(w, http.StatusBadGateway, map[string]string{"error": "model unavailable"})
			return
		}

		writeJSON(w, http.StatusOK, answer)
	}
}

// handleAnalyze is the deferrable path. It does no work: it writes a job to
// the tenant's queue and returns 202 immediately.
//
// Returning before the work happens is the point. It is what lets the worker
// be scaled to zero independently of the API, and it is why the queue depth -
// not CPU - is the signal KEDA scales on.
func handleAnalyze(q *queue.Redis, log *slog.Logger) http.HandlerFunc {
	type request struct {
		Size int `json:"size"`
	}

	return func(w http.ResponseWriter, r *http.Request) {
		var req request
		// An empty body is allowed: size is optional and has a default.
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&req); err != nil && err.Error() != "EOF" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "body must be {\"size\": n} or empty"})
			return
		}
		if req.Size <= 0 {
			req.Size = analysis.DefaultSize
		}
		if req.Size > analysis.MaxSize {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "size too large", "max": analysis.MaxSize})
			return
		}

		tenant := admission.Tenant(r.Context())
		j := queue.Job{
			ID:       queue.NewID(),
			Tenant:   tenant,
			Size:     req.Size,
			Seed:     time.Now().UnixNano(),
			Enqueued: time.Now().UTC(),
		}

		if err := q.Enqueue(r.Context(), j); err != nil {
			log.Error("enqueue failed", "tenant", tenant, "err", err)
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "queue unavailable"})
			return
		}

		depth, err := q.Depth(r.Context(), tenant)
		if err != nil {
			depth = -1 // reporting it is a convenience, not worth failing over
		}

		writeJSON(w, http.StatusAccepted, map[string]any{
			"job_id":      j.ID,
			"tenant":      tenant,
			"size":        j.Size,
			"queue_depth": depth,
		})
	}
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func envString(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func envInt64(key string, def int64) int64 {
	if v, err := strconv.ParseInt(os.Getenv(key), 10, 64); err == nil {
		return v
	}
	return def
}

func envDuration(key string, def time.Duration) time.Duration {
	if v, err := time.ParseDuration(os.Getenv(key)); err == nil {
		return v
	}
	return def
}
