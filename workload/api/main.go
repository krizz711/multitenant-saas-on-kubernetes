// Command api is the shop's front door: a catalogue search a shopper waits
// on, a model-backed question, and a re-index hand-off nobody waits on.
//
// It is deliberately the least interesting code in this repository. The whole
// point of the project is that this application is replaceable - it already
// was replaced once, from a measurement-systems tool to a shop, without the
// control plane changing a line.
package main

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/redis/go-redis/v9"

	"github.com/krizz711/multitenant-saas-on-kubernetes/controlplane/admission"
	"github.com/krizz711/multitenant-saas-on-kubernetes/controlplane/gateway"
	"github.com/krizz711/multitenant-saas-on-kubernetes/controlplane/model"
	"github.com/krizz711/multitenant-saas-on-kubernetes/controlplane/obs"
	"github.com/krizz711/multitenant-saas-on-kubernetes/controlplane/queue"
	"github.com/krizz711/multitenant-saas-on-kubernetes/workload/catalogue"
	"github.com/krizz711/multitenant-saas-on-kubernetes/workload/store"
)

//go:embed ui/*
var uiFiles embed.FS

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	tenant := envString("TENANT", "unknown")

	upstream := chooseModel(log)

	// One Redis client shared by the queue and the gateway. Dialling is lazy,
	// so a pod that starts before Redis is ready still comes up; the endpoints
	// needing Redis fail with a 503 that says so, rather than the pod refusing
	// to start and taking the working endpoints down with it.
	redisOpts, err := redis.ParseURL(envString("REDIS_URL", "redis://localhost:6379/0"))
	if err != nil {
		log.Error("redis url", "err", err)
		os.Exit(1)
	}
	rdb := redis.NewClient(redisOpts)
	defer func() { _ = rdb.Close() }()

	q := queue.NewRedisClient(rdb)

	// The gateway wraps the model rather than replacing it and satisfies the
	// same interface, so handleAsk below never learns which model is behind it.
	llm := gateway.New(
		upstream,
		gateway.NewBudget(rdb, envInt64("TOKEN_BUDGET", 100_000), nil),
		gateway.NewCache(
			rdb,
			gateway.LexicalEmbedder{},
			envFloat("CACHE_THRESHOLD", 0.90),
			envInt64("CACHE_MAX_ENTRIES", 200),
			envDuration("CACHE_TTL", 24*time.Hour),
		),
	)

	db := openStore(context.Background(), tenant, log)
	defer db.Close()

	srv := &http.Server{
		Addr:              ":" + envString("PORT", "8000"),
		Handler:           routes(llm, q, db, tenant, log),
		ReadHeaderTimeout: 5 * time.Second,
	}

	// Shut down on SIGTERM rather than dying on it. Kubernetes sends SIGTERM
	// and waits before SIGKILL; finishing in-flight requests in that window is
	// the difference between a clean scale-down and a spike of 502s in the
	// results, which matters directly to claim C1.
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	go func() {
		log.Info("api listening", "addr", srv.Addr, "tenant", tenant)
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

// chooseModel uses the real model when a key is present and a deterministic
// stub otherwise.
//
// The stub is not a placeholder to be embarrassed about: experiments run
// against it on purpose, because a rate-limited dependency in the request path
// would contaminate every latency number in the results. The real client is
// for demonstrations, where an answer a human can read matters more than a
// reproducible millisecond.
func chooseModel(log *slog.Logger) model.Client {
	// Provider selection is the only place in the codebase that knows which
	// vendor is in use. The gateway, the budget, the cache, the coalescer and
	// every metric sit behind model.Client and are indifferent to the answer
	// here - which is the architectural claim, made concrete.
	if id, secret := os.Getenv("GLOO_CLIENT_ID"), os.Getenv("GLOO_CLIENT_SECRET"); id != "" && secret != "" {
		m := os.Getenv("GLOO_MODEL")
		log.Info("using Gloo (OpenAI-compatible)", "model", or(m, "auto-routing"))
		return model.NewGloo(id, secret, m, envDuration("MODEL_TIMEOUT", 30*time.Second))
	}
	if key := os.Getenv("GEMINI_API_KEY"); key != "" {
		m := envString("GEMINI_MODEL", "gemini-2.0-flash")
		log.Info("using Gemini", "model", m)
		return model.NewGemini(key, m, envDuration("MODEL_TIMEOUT", 20*time.Second))
	}

	// The deterministic stub is what the experiments run against on purpose:
	// a rate-limited dependency in the request path would contaminate every
	// latency number in the results.
	log.Info("no model credentials set, using deterministic stub")
	return model.NewStub(
		envInt64("MODEL_SEED", 1),
		envDuration("MODEL_BASE_LATENCY", 120*time.Millisecond),
		envDuration("MODEL_JITTER", 80*time.Millisecond),
	)
}

func or(v, fallback string) string {
	if v != "" {
		return v
	}
	return fallback
}

func openStore(ctx context.Context, tenant string, log *slog.Logger) store.Store {
	url := os.Getenv("DATABASE_URL")
	if url == "" {
		log.Info("no DATABASE_URL set, running without a catalogue")
		return store.Nop{}
	}

	db, err := store.NewPostgres(ctx, url)
	if err != nil {
		log.Error("database unreachable, continuing without a catalogue", "err", err)
		return store.Nop{}
	}

	// Preparation runs in the background, retrying, rather than once at
	// startup.
	//
	// The Helm hook that creates this tenant's schema and database role is a
	// post-install hook, so it can land AFTER the pods it serves. A single
	// attempt at boot would fail, be logged, and never be retried - leaving a
	// pod that is healthy, serving, and permanently unable to read its own
	// catalogue. Retrying also covers the ordinary case of Postgres still
	// starting up.
	go prepare(db, tenant, envInt("CATALOGUE_SIZE", 400), log)
	return db
}

func prepare(db store.Store, tenant string, size int, log *slog.Logger) {
	backoff := 2 * time.Second
	for attempt := 1; ; attempt++ {
		ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
		err := db.EnsureSchema(ctx)
		if err == nil {
			var n int
			n, err = db.SeedIfEmpty(ctx, tenant, size)
			if err == nil {
				cancel()
				log.Info("catalogue ready", "tenant", tenant, "products", n)
				return
			}
		}
		cancel()

		log.Warn("catalogue not ready yet, retrying",
			"tenant", tenant, "attempt", attempt, "in", backoff.String(), "err", err)
		time.Sleep(backoff)
		if backoff < 30*time.Second {
			backoff *= 2
		}
	}
}

// routes builds the two-level router.
//
// The split is not cosmetic. admission.Middleware rejects any request with no
// X-Tenant header, and Prometheus scrapes /metrics without one while the
// kubelet probes /healthz without one. Routing those through admission would
// return 400 to every scrape and leave the project with no data source.
//
// Inside, admission wraps obs and never the reverse: obs labels its histogram
// from the tenant and class admission puts on the context, so reversing them
// silently labels every metric "unknown".
func routes(llm model.Client, q *queue.Redis, db store.Store, tenant string, log *slog.Logger) http.Handler {
	app := http.NewServeMux()
	app.HandleFunc("GET /products", handleProducts(db, log))
	app.HandleFunc("POST /ask", handleAsk(llm, db, log))
	app.HandleFunc("POST /reindex", handleReindex(q, log))

	root := http.NewServeMux()
	root.Handle("GET /metrics", promhttp.Handler())
	root.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})

	// The demo storefront. Served outside admission because a browser loading
	// a page cannot set a custom header; the page's own fetch calls supply
	// X-Tenant, exactly as a real single-page app would after sign-in.
	root.HandleFunc("GET /shop", func(w http.ResponseWriter, r *http.Request) {
		page, err := uiFiles.ReadFile("ui/index.html")
		if err != nil {
			http.Error(w, "ui unavailable", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(strings.ReplaceAll(string(page), "{{TENANT}}", tenant)))
	})

	root.Handle("/", admission.Middleware(obs.Middleware(app)))
	return root
}

// handleProducts is the cheap interactive read: the request a shopper is
// waiting on, the one the 400 ms objective is written about, and the one
// claim C3 protects while a neighbour rebuilds its index.
func handleProducts(db store.Store, log *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		query := strings.TrimSpace(r.URL.Query().Get("q"))
		limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))

		products, err := db.Search(r.Context(), query, limit)
		if err != nil {
			log.Error("search failed", "err", err)
			writeJSON(w, http.StatusServiceUnavailable,
				map[string]string{"error": "catalogue unavailable"})
			return
		}

		stats, err := db.Stats(r.Context())
		if err != nil {
			// A missing summary should not fail a search that succeeded.
			stats = store.Stats{}
		}

		writeJSON(w, http.StatusOK, map[string]any{
			"tenant":   admission.Tenant(r.Context()),
			"class":    string(admission.Of(r.Context())),
			"query":    query,
			"count":    len(products),
			"products": products,
			"catalogue": map[string]any{
				"products":      stats.Products,
				"related_edges": stats.RelatedEdges,
				"storage_bytes": stats.StorageBytes,
				"last_reindex":  stats.LastReindex,
			},
		})
	}
}

// handleAsk is the expensive-in-money path. It is expensive in tokens rather
// than CPU, which is why capping it needs a gateway and not a CPU limit.
//
// The question is grounded in the tenant's own catalogue before it reaches the
// model. That is what makes the per-tenant cache correct rather than merely
// convenient: the prompt contains one merchant's products, so an answer cached
// from it must never be served to another merchant.
func handleAsk(llm model.Client, db store.Store, log *slog.Logger) http.HandlerFunc {
	type request struct {
		Question string `json:"question"`
	}

	return func(w http.ResponseWriter, r *http.Request) {
		var req request
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10)).Decode(&req); err != nil {
			writeJSON(w, http.StatusBadRequest,
				map[string]string{"error": `body must be {"question": "..."}`})
			return
		}
		req.Question = strings.TrimSpace(req.Question)
		if req.Question == "" {
			writeJSON(w, http.StatusBadRequest,
				map[string]string{"error": "question must not be empty"})
			return
		}

		tenant := admission.Tenant(r.Context())

		grounding, err := db.Search(r.Context(), keywords(req.Question), 5)
		if err != nil {
			log.Error("grounding search failed", "tenant", tenant, "err", err)
		}

		answer, err := llm.Ask(r.Context(), tenant, buildPrompt(req.Question, grounding))
		if err != nil {
			// A cancelled request is the client's decision, not a server
			// fault, and must not count against the error rate.
			if errors.Is(err, context.Canceled) {
				return
			}
			// Over budget is a policy outcome, not a failure. 429 tells the
			// caller to slow down; a 502 would suggest the model broke and
			// send them retrying into the same wall.
			if errors.Is(err, gateway.ErrBudgetExceeded) {
				writeJSON(w, http.StatusTooManyRequests, map[string]string{
					"error": "token budget exhausted for this billing period",
				})
				return
			}
			log.Error("model call failed", "tenant", tenant, "err", err)
			writeJSON(w, http.StatusBadGateway,
				map[string]string{"error": "model unavailable"})
			return
		}

		writeJSON(w, http.StatusOK, map[string]any{
			"answer":        answer.Text,
			"cached":        answer.Cached,
			"model":         answer.Model,
			"prompt_tokens": answer.PromptTokens,
			"output_tokens": answer.OutputTokens,
			"products":      grounding,
		})
	}
}

// buildPrompt grounds the question in the merchant's catalogue.
func buildPrompt(question string, products []catalogue.Product) string {
	var sb strings.Builder
	sb.WriteString("You are a shopping assistant. Answer using only the products listed.\n")
	sb.WriteString("Be brief: two sentences at most, and name the product you recommend.\n\n")

	if len(products) == 0 {
		sb.WriteString("Products: none matched.\n\n")
	} else {
		sb.WriteString("Products:\n")
		for _, p := range products {
			fmt.Fprintf(&sb, "- %s (%s) Rs %.0f: %s\n",
				p.Name, p.Category, p.PriceINR, p.Description)
		}
		sb.WriteString("\n")
	}

	sb.WriteString("Question: ")
	sb.WriteString(question)
	return sb.String()
}

// keywords strips the question down to terms worth searching on, because
// "do you have a waterproof jacket" matches no product name verbatim.
func keywords(question string) string {
	fields := strings.Fields(strings.ToLower(question))
	for _, f := range fields {
		trimmed := strings.Trim(f, "?,.!\"'")
		if len(trimmed) > 4 && !questionWords[trimmed] {
			return trimmed
		}
	}
	return ""
}

var questionWords = map[string]bool{
	"which": true, "where": true, "there": true, "about": true, "would": true,
	"could": true, "should": true, "please": true, "whats": true, "under": true,
}

// handleReindex is the deferrable path. It does no work: it writes a job to
// the tenant's queue and returns 202 immediately.
//
// Returning before the work happens is the point. It is what lets the worker
// scale to zero independently of the API, and why queue depth rather than CPU
// is the signal KEDA scales on.
func handleReindex(q *queue.Redis, log *slog.Logger) http.HandlerFunc {
	type request struct {
		TopK int `json:"top_k"`
	}

	return func(w http.ResponseWriter, r *http.Request) {
		var req request
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&req); err != nil && err.Error() != "EOF" {
			writeJSON(w, http.StatusBadRequest,
				map[string]string{"error": `body must be {"top_k": n} or empty`})
			return
		}
		if req.TopK <= 0 {
			req.TopK = 8
		}

		tenant := admission.Tenant(r.Context())
		j := queue.Job{
			ID:       queue.NewID(),
			Tenant:   tenant,
			Size:     req.TopK,
			Seed:     time.Now().UnixNano(),
			Enqueued: time.Now().UTC(),
		}

		if err := q.Enqueue(r.Context(), j); err != nil {
			log.Error("enqueue failed", "tenant", tenant, "err", err)
			writeJSON(w, http.StatusServiceUnavailable,
				map[string]string{"error": "queue unavailable"})
			return
		}

		depth, err := q.Depth(r.Context(), tenant)
		if err != nil {
			depth = -1 // a convenience, not worth failing the request over
		}

		writeJSON(w, http.StatusAccepted, map[string]any{
			"job_id":      j.ID,
			"tenant":      tenant,
			"top_k":       j.Size,
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

func envInt(key string, def int) int {
	if v, err := strconv.Atoi(os.Getenv(key)); err == nil {
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

func envFloat(key string, def float64) float64 {
	if v, err := strconv.ParseFloat(os.Getenv(key), 64); err == nil {
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
