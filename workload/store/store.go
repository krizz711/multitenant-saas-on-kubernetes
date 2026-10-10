// Package store is the shop's persistence: a product catalogue, a similarity
// graph, and a log of re-index runs.
//
// It lives under workload/ because these tables are domain-specific. A
// different application under this same control plane would have entirely
// different tables, and the control plane must not know they exist.
//
// Every connection is pinned to one tenant's schema by the search_path in its
// connection URL, and that tenant's database role holds no grant on any other
// schema. Isolation is enforced by Postgres rather than by remembering to add
// a WHERE clause, which is the difference between the bridge model of
// multi-tenancy and merely hoping.
package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/krizz711/multitenant-saas-on-kubernetes/workload/catalogue"
)

// Stats backs the cheap interactive read and feeds the per-tenant cost meter.
type Stats struct {
	Products     int64      `json:"products"`
	RelatedEdges int64      `json:"related_edges"`
	StorageBytes int64      `json:"storage_bytes"`
	LastReindex  *time.Time `json:"last_reindex,omitempty"`
}

// ReindexRun is one completed batch job.
type ReindexRun struct {
	ID       string
	Products int
	Edges    int
	Duration time.Duration
}

// Store is an interface so the API still runs with no database, the same
// reasoning as the lazy Redis dial: one absent dependency should not take down
// the endpoints that do not need it.
type Store interface {
	EnsureSchema(ctx context.Context) error
	SeedIfEmpty(ctx context.Context, tenant string, count int) (int, error)
	Search(ctx context.Context, q string, limit int) ([]catalogue.Product, error)
	AllProducts(ctx context.Context) ([]catalogue.Product, error)
	ReplaceRelated(ctx context.Context, edges []catalogue.Related) error
	RecordReindex(ctx context.Context, run ReindexRun) error
	Stats(ctx context.Context) (Stats, error)
	Close()
}

// Nop is used when DATABASE_URL is unset.
type Nop struct{}

func (Nop) EnsureSchema(context.Context) error                    { return nil }
func (Nop) SeedIfEmpty(context.Context, string, int) (int, error) { return 0, nil }
func (Nop) Search(context.Context, string, int) ([]catalogue.Product, error) {
	return nil, nil
}
func (Nop) AllProducts(context.Context) ([]catalogue.Product, error)  { return nil, nil }
func (Nop) ReplaceRelated(context.Context, []catalogue.Related) error { return nil }
func (Nop) RecordReindex(context.Context, ReindexRun) error           { return nil }
func (Nop) Stats(context.Context) (Stats, error)                      { return Stats{}, nil }
func (Nop) Close()                                                    {}

// Postgres is the real implementation.
type Postgres struct{ pool *pgxpool.Pool }

func NewPostgres(ctx context.Context, url string) (*Postgres, error) {
	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		return nil, fmt.Errorf("parse database url: %w", err)
	}
	// Deliberately small. Every API pod holding a large pool would let a few
	// tenants exhaust a shared Postgres's connection slots, which is the
	// noisy-neighbour problem relocated into the database tier.
	cfg.MaxConns = 8
	cfg.MinConns = 1
	cfg.MaxConnIdleTime = 5 * time.Minute

	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("connect: %w", err)
	}
	return &Postgres{pool: pool}, nil
}

func (p *Postgres) Close() { p.pool.Close() }

func (p *Postgres) EnsureSchema(ctx context.Context) error {
	const ddl = `
CREATE TABLE IF NOT EXISTS products (
    id          bigserial PRIMARY KEY,
    name        text             NOT NULL,
    description text             NOT NULL,
    price_inr   double precision NOT NULL,
    category    text             NOT NULL,
    created_at  timestamptz      NOT NULL DEFAULT now()
);

-- The shopper's search runs against these. Without them /products degrades
-- from an index lookup to a sequential scan as the catalogue grows, and the
-- cheap interactive read becomes the slow query that breaks its own SLO.
CREATE INDEX IF NOT EXISTS products_name_idx ON products (lower(name));
CREATE INDEX IF NOT EXISTS products_category_idx ON products (lower(category));

CREATE TABLE IF NOT EXISTS related_products (
    product_id bigint           NOT NULL,
    related_id bigint           NOT NULL,
    score      double precision NOT NULL,
    PRIMARY KEY (product_id, related_id)
);

CREATE TABLE IF NOT EXISTS reindex_runs (
    id          text        PRIMARY KEY,
    products    integer     NOT NULL,
    edges       integer     NOT NULL,
    duration_ms bigint      NOT NULL,
    created_at  timestamptz NOT NULL DEFAULT now()
);`

	if _, err := p.pool.Exec(ctx, ddl); err != nil {
		return fmt.Errorf("ensure schema: %w", err)
	}
	return nil
}

// SeedIfEmpty gives the merchant a catalogue on first start.
//
// Each tenant gets a DIFFERENT catalogue, which is not decoration: it is how
// the demo shows that a search result or a cached answer from one merchant can
// never surface for another. If all three sold the same products, a
// cross-tenant leak would be invisible.
func (p *Postgres) SeedIfEmpty(ctx context.Context, tenant string, count int) (int, error) {
	// Seeding must happen once even when several pods start together, which
	// they do on every rolling update. Without the lock both pods see an
	// empty table, both insert, and the merchant silently ends up with twice
	// the catalogue - a bug that looks like bad data rather than a race.
	//
	// The lock is held for the transaction, so a crash releases it rather
	// than wedging every future start.
	conn, err := p.pool.Acquire(ctx)
	if err != nil {
		return 0, fmt.Errorf("acquire for seed: %w", err)
	}
	defer conn.Release()

	tx, err := conn.Begin(ctx)
	if err != nil {
		return 0, fmt.Errorf("begin seed: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// A constant derived from the tenant, so merchants do not block each other.
	if _, err := tx.Exec(ctx,
		`SELECT pg_advisory_xact_lock(hashtext($1))`, "seed:"+tenant); err != nil {
		return 0, fmt.Errorf("seed lock: %w", err)
	}

	var existing int64
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM products`).Scan(&existing); err != nil {
		return 0, fmt.Errorf("count products: %w", err)
	}
	if existing > 0 {
		return int(existing), nil
	}

	products := SeedCatalogue(tenant, count)

	rows := make([][]any, 0, len(products))
	for _, pr := range products {
		rows = append(rows, []any{pr.Name, pr.Description, pr.PriceINR, pr.Category})
	}
	if _, err := tx.CopyFrom(ctx,
		pgx.Identifier{"products"},
		[]string{"name", "description", "price_inr", "category"},
		pgx.CopyFromRows(rows),
	); err != nil {
		return 0, fmt.Errorf("seed products: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, fmt.Errorf("commit seed: %w", err)
	}
	return len(products), nil
}

// Search is the shopper's interactive read.
func (p *Postgres) Search(ctx context.Context, q string, limit int) ([]catalogue.Product, error) {
	if limit <= 0 || limit > 50 {
		limit = 20
	}
	const sql = `
SELECT id, name, description, price_inr, category
FROM products
WHERE $1 = ''
   OR lower(name) LIKE '%' || lower($1) || '%'
   OR lower(category) LIKE '%' || lower($1) || '%'
ORDER BY id
LIMIT $2`

	rows, err := p.pool.Query(ctx, sql, q, limit)
	if err != nil {
		return nil, fmt.Errorf("search: %w", err)
	}
	defer rows.Close()

	var out []catalogue.Product
	for rows.Next() {
		var pr catalogue.Product
		if err := rows.Scan(&pr.ID, &pr.Name, &pr.Description, &pr.PriceINR, &pr.Category); err != nil {
			return nil, fmt.Errorf("scan product: %w", err)
		}
		out = append(out, pr)
	}
	return out, rows.Err()
}

func (p *Postgres) AllProducts(ctx context.Context) ([]catalogue.Product, error) {
	rows, err := p.pool.Query(ctx,
		`SELECT id, name, description, price_inr, category FROM products ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("all products: %w", err)
	}
	defer rows.Close()

	var out []catalogue.Product
	for rows.Next() {
		var pr catalogue.Product
		if err := rows.Scan(&pr.ID, &pr.Name, &pr.Description, &pr.PriceINR, &pr.Category); err != nil {
			return nil, fmt.Errorf("scan product: %w", err)
		}
		out = append(out, pr)
	}
	return out, rows.Err()
}

// ReplaceRelated swaps in a freshly computed similarity graph.
//
// One transaction, because a shopper browsing mid-re-index must see either the
// old graph or the new one and never a half-deleted one. Truncate-then-insert
// outside a transaction would show an empty "related products" panel for the
// whole duration of the rebuild.
func (p *Postgres) ReplaceRelated(ctx context.Context, edges []catalogue.Related) error {
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := tx.Exec(ctx, `DELETE FROM related_products`); err != nil {
		return fmt.Errorf("clear related: %w", err)
	}

	if len(edges) > 0 {
		rows := make([][]any, 0, len(edges))
		for _, e := range edges {
			rows = append(rows, []any{e.ProductID, e.RelatedID, e.Score})
		}
		if _, err := tx.CopyFrom(ctx,
			pgx.Identifier{"related_products"},
			[]string{"product_id", "related_id", "score"},
			pgx.CopyFromRows(rows),
		); err != nil {
			return fmt.Errorf("insert related: %w", err)
		}
	}
	return tx.Commit(ctx)
}

func (p *Postgres) RecordReindex(ctx context.Context, run ReindexRun) error {
	const sql = `
INSERT INTO reindex_runs (id, products, edges, duration_ms)
VALUES ($1, $2, $3, $4)
ON CONFLICT (id) DO NOTHING`
	if _, err := p.pool.Exec(ctx, sql,
		run.ID, run.Products, run.Edges, run.Duration.Milliseconds()); err != nil {
		return fmt.Errorf("record reindex: %w", err)
	}
	return nil
}

// Stats also reports the schema's on-disk size, which is the storage term of
// the per-tenant cost meter. Measuring it rather than estimating it is what
// makes the rupee figure attributable instead of assumed.
func (p *Postgres) Stats(ctx context.Context) (Stats, error) {
	const sql = `
SELECT (SELECT count(*) FROM products),
       (SELECT count(*) FROM related_products),
       coalesce((SELECT sum(pg_total_relation_size(c.oid))
                 FROM pg_class c
                 JOIN pg_namespace n ON n.oid = c.relnamespace
                 WHERE n.nspname = current_schema()), 0),
       (SELECT max(created_at) FROM reindex_runs)`

	var s Stats
	if err := p.pool.QueryRow(ctx, sql).Scan(
		&s.Products, &s.RelatedEdges, &s.StorageBytes, &s.LastReindex,
	); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Stats{}, nil
		}
		return Stats{}, fmt.Errorf("stats: %w", err)
	}
	return s, nil
}
