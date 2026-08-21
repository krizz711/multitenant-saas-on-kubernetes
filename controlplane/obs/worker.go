package obs

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// The worker's three metrics. They are separate from RequestDuration because
// they answer a different question: RequestDuration is about a user waiting,
// these are about work nobody is waiting on.

// JobDuration is how long the analysis itself took once it started. Under
// claim C3 this is expected to get *worse* when batch work is deprioritised -
// that is the trade being made, and hiding it would be dishonest.
var JobDuration = promauto.NewHistogramVec(
	prometheus.HistogramOpts{
		Name:    "job_duration_seconds",
		Help:    "Time spent executing a deferrable job, once started.",
		Buckets: []float64{0.05, 0.1, 0.25, 0.5, 1, 2, 5, 10, 30},
	},
	[]string{"tenant"},
)

// JobQueueWait is the gap between enqueue and start, and it is the metric that
// prices scale-to-zero. When a tenant's workers are scaled to nothing, the
// first job of the day pays the cold start here rather than in JobDuration.
// The buckets run to five minutes because a cold start is seconds, not
// milliseconds, and a bucket list that tops out too early would clip exactly
// the tail claim C1 is about.
var JobQueueWait = promauto.NewHistogramVec(
	prometheus.HistogramOpts{
		Name:    "job_queue_wait_seconds",
		Help:    "Time a job spent waiting in the queue before a worker took it.",
		Buckets: []float64{0.1, 0.5, 1, 2, 5, 10, 30, 60, 120, 300},
	},
	[]string{"tenant"},
)

// JobsProcessed counts outcomes. Split by outcome so a run that fails fast
// cannot be mistaken for a run that succeeded quickly.
var JobsProcessed = promauto.NewCounterVec(
	prometheus.CounterOpts{
		Name: "jobs_processed_total",
		Help: "Deferrable jobs taken off the queue, by outcome.",
	},
	[]string{"tenant", "outcome"},
)

// QueueErrors counts failures to *read* the queue, which are deliberately not
// counted as job failures.
//
// The distinction is not pedantic. A worker that starts before Redis is ready
// logs several dial failures before recovering - normal, expected, and nobody
// is harmed. Counting those as failed jobs would inflate the error rate that
// claims C1 and C3 are judged against with pure startup noise, and would do it
// most severely in exactly the scale-from-zero scenario C1 is about, where
// workers start cold all the time.
var QueueErrors = promauto.NewCounterVec(
	prometheus.CounterOpts{
		Name: "queue_read_errors_total",
		Help: "Failures to read from the queue. Not job failures; a cold worker produces these until its dependency is ready.",
	},
	[]string{"tenant"},
)
