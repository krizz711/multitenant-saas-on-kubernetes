// Package admission stamps every incoming request with the tenant it belongs
// to and the latency class it falls into. Everything downstream — metrics,
// scheduling, budgets, cost attribution — keys off these two values, so this
// is the first stage of the control plane and the only one that must run on
// every single request.
package admission

import (
	"context"
	"net/http"
	"strings"
)

// Class separates work a user is waiting on from work nobody is waiting on.
// The control plane may delay Deferrable work; it may never delay Interactive.
type Class string

const (
	Interactive Class = "interactive"
	Deferrable  Class = "deferrable"
)

// TenantHeader is where the ingress is expected to put the tenant identity.
// In production this would be derived from an authenticated token rather than
// trusted from a header — see the note in Middleware.
const TenantHeader = "X-Tenant"

type ctxKey int

const (
	tenantKey ctxKey = iota
	classKey
)

// deferrablePaths are the routes whose work is queued rather than served
// synchronously. Kept as an explicit set: classification is a policy decision,
// not something to infer from the HTTP method.
var deferrablePaths = map[string]bool{
	"/analyze": true,
	"/report":  true,
}

// Middleware rejects requests that carry no tenant, then records the tenant
// and the latency class on the request context.
//
// Trusting a header is acceptable here because the cluster's NetworkPolicy
// makes the ingress the only route to these pods. If that ever stops being
// true, this is the line that has to change.
func Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tenant := strings.TrimSpace(r.Header.Get(TenantHeader))
		if tenant == "" {
			http.Error(w, `{"error":"missing `+TenantHeader+` header"}`, http.StatusBadRequest)
			return
		}

		class := Interactive
		if deferrablePaths[r.URL.Path] {
			class = Deferrable
		}

		ctx := context.WithValue(r.Context(), tenantKey, tenant)
		ctx = context.WithValue(ctx, classKey, class)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// Tenant returns the tenant recorded by Middleware, or "unknown" if the
// request did not pass through it.
func Tenant(ctx context.Context) string {
	if v, ok := ctx.Value(tenantKey).(string); ok {
		return v
	}
	return "unknown"
}

// Of returns the latency class recorded by Middleware, defaulting to
// Interactive — the safer assumption, since it is the class that is protected.
func Of(ctx context.Context) Class {
	if v, ok := ctx.Value(classKey).(Class); ok {
		return v
	}
	return Interactive
}
