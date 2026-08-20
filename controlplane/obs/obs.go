// Package obs holds the measurement side of the control plane. Every number
// reported in the project's results is derived from what is registered here,
// so the bucket boundaries below are a design decision, not a default.
package obs

import (
	"net/http"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"

	"github.com/krizz711/multitenant-saas-on-kubernetes/controlplane/admission"
)

// SLO is the tail-latency objective for interactive requests. It exists as a
// constant so that the value in the code, the value in the bucket list and the
// value quoted in the paper cannot drift apart.
const SLO = 400 * time.Millisecond

// RequestDuration is the histogram every latency figure comes from.
//
// The buckets are chosen so that 0.4 is an exact boundary. histogram_quantile
// interpolates within a bucket, so a p95 that lands inside a wide bucket is a
// guess; putting the objective on an edge makes "did we meet the SLO" an exact
// comparison rather than an estimate. The client library defaults do not
// include 0.4, which is why they are not used.
var RequestDuration = promauto.NewHistogramVec(
	prometheus.HistogramOpts{
		Name:    "http_request_duration_seconds",
		Help:    "Request latency by route, tenant and latency class.",
		Buckets: []float64{0.01, 0.025, 0.05, 0.1, 0.2, 0.3, 0.4, 0.6, 1, 2, 5},
	},
	[]string{"route", "tenant", "class", "code"},
)

// statusRecorder captures the status code, which the standard
// http.ResponseWriter will not tell us after the fact.
type statusRecorder struct {
	http.ResponseWriter
	code int
}

func (s *statusRecorder) WriteHeader(code int) {
	s.code = code
	s.ResponseWriter.WriteHeader(code)
}

// Middleware times each request and records it against the tenant and class
// that admission.Middleware put on the context. It must therefore be wrapped
// *inside* admission.Middleware, not outside it.
func Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, code: http.StatusOK}

		next.ServeHTTP(rec, r)

		RequestDuration.WithLabelValues(
			r.URL.Path,
			admission.Tenant(r.Context()),
			string(admission.Of(r.Context())),
			http.StatusText(rec.code),
		).Observe(time.Since(start).Seconds())
	})
}
