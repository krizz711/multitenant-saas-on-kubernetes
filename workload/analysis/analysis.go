// Package job is the deferrable work: the CPU-bound half of the workload.
//
// It computes a gauge repeatability-and-reproducibility study, the analysis
// that motivated this whole project. The arithmetic is real rather than
// simulated, and that is deliberate. Claim C3 says a priority class stops
// batch work from delaying interactive work, and two processes can only
// compete for a core if both are actually using one - a time.Sleep would
// contend for nothing and the experiment would prove nothing.
package analysis

import "math"

// d2 is the AIAG bias-correction constant for a subgroup of three trials. It
// converts an average range into a standard deviation estimate.
const d2Trials3 = 1.693

// Result is what one analysis produces. The numbers are not consumed by
// anything - the point is the CPU spent producing them - but they are correct,
// which means a reviewer can check them.
type Result struct {
	Measurements    int     `json:"measurements"`
	Repeatability   float64 `json:"repeatability"`
	Reproducibility float64 `json:"reproducibility"`
	GaugeRR         float64 `json:"gauge_rr"`
	PartVariation   float64 `json:"part_variation"`
}

const (
	operators = 3
	trials    = 3
)

// DefaultSize and MaxSize live here, next to the code whose cost they govern,
// so the API and the benchmark cannot disagree about them.
//
// DefaultSize is calibrated, not guessed: measured at roughly 71 ns per part
// on the development machine, 3.5M parts is about 250 ms of solid CPU. That is
// long enough for a job to genuinely contend for a core - which claim C3
// requires - and short enough that a seven-configuration experiment matrix
// finishes in an evening. Recalibrate with `go test ./internal/job -bench=.`
// if the hardware changes; the paper should quote the machine it was tuned on.
const (
	DefaultSize = 3_500_000
	MaxSize     = 40_000_000
)

// Run analyses parts x 3 operators x 3 trials synthetic measurements.
//
// Cost is linear in parts, so the caller tunes runtime with one number. Given
// the same seed it produces the same answer in the same time, which is what
// makes an experiment repeatable rather than merely rerunnable.
func Run(parts int, seed int64) Result {
	if parts < 1 {
		parts = 1
	}
	rng := newLCG(seed)

	operatorSums := make([]float64, operators)
	var rangeSum float64

	// Part means are accumulated with Welford's online algorithm rather than
	// kept in a slice. Cost has to scale to millions of parts for a job to
	// occupy a core long enough to contend with anything, and a slice that
	// long would exhaust the pod's memory limit before it exhausted its CPU
	// limit - which would test the wrong resource entirely.
	var partCount int
	var partMean, partM2 float64

	for p := 0; p < parts; p++ {
		// Each part has a true size; the spread across parts is the signal a
		// gauge study is trying to resolve.
		truth := 10 + rng.next()*4

		var partSum float64
		for o := 0; o < operators; o++ {
			// Each operator carries a small persistent offset. That offset is
			// what reproducibility measures.
			bias := (float64(o) - 1) * 0.08

			lo, hi := math.Inf(1), math.Inf(-1)
			var opSum float64
			for t := 0; t < trials; t++ {
				v := truth + bias + (rng.next()-0.5)*0.35
				opSum += v
				lo = math.Min(lo, v)
				hi = math.Max(hi, v)
			}
			// The range within one operator's repeat trials is repeatability:
			// same part, same operator, so anything left is the gauge itself.
			rangeSum += hi - lo
			operatorSums[o] += opSum
			partSum += opSum
		}
		pm := partSum / float64(operators*trials)
		partCount++
		delta := pm - partMean
		partMean += delta / float64(partCount)
		partM2 += delta * (pm - partMean)
	}

	n := float64(parts * operators)
	meanRange := rangeSum / n

	// Repeatability (equipment variation) from the average range.
	ev := meanRange / d2Trials3

	// Reproducibility (appraiser variation) from the spread of operator means,
	// with the repeatability contribution removed - without that correction the
	// same variation is counted twice.
	opLo, opHi := math.Inf(1), math.Inf(-1)
	for _, s := range operatorSums {
		m := s / float64(parts*trials)
		opLo = math.Min(opLo, m)
		opHi = math.Max(opHi, m)
	}
	avSquared := math.Pow((opHi-opLo)/1.693, 2) - (ev*ev)/(float64(parts)*trials)
	av := 0.0
	if avSquared > 0 {
		av = math.Sqrt(avSquared)
	}

	return Result{
		Measurements:    parts * operators * trials,
		Repeatability:   ev,
		Reproducibility: av,
		GaugeRR:         math.Sqrt(ev*ev + av*av),
		PartVariation:   sampleStdDev(partM2, partCount),
	}
}

// sampleStdDev finishes Welford's algorithm. Bessel's correction (n-1) is used
// because these parts are a sample of the population a gauge study is
// generalising to, not the whole of it.
func sampleStdDev(m2 float64, n int) float64 {
	if n < 2 {
		return 0
	}
	return math.Sqrt(m2 / float64(n-1))
}

// lcg is a linear congruential generator, used instead of math/rand so the
// numbers depend only on the seed and never on Go's global source, which other
// packages can reseed. An experiment that cannot be replayed is not evidence.
type lcg struct{ state uint64 }

func newLCG(seed int64) *lcg {
	return &lcg{state: uint64(seed)*6364136223846793005 + 1442695040888963407}
}

// next returns a value in [0,1).
func (l *lcg) next() float64 {
	l.state = l.state*6364136223846793005 + 1442695040888963407
	return float64(l.state>>11) / float64(1<<53)
}
