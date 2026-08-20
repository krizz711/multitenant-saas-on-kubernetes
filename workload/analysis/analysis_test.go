package analysis_test

import (
	"testing"

	"github.com/krizz711/multitenant-saas-on-kubernetes/workload/analysis"
)

func TestRunIsDeterministic(t *testing.T) {
	// Same seed, same answer. Every experiment in this project is a comparison
	// between two runs, so a workload that varied run to run would make the
	// difference between configurations impossible to separate from noise.
	a := analysis.Run(500, 42)
	b := analysis.Run(500, 42)
	if a != b {
		t.Fatalf("same seed gave different results:\n %+v\n %+v", a, b)
	}
}

func TestDifferentSeedsGiveDifferentData(t *testing.T) {
	a := analysis.Run(500, 1)
	b := analysis.Run(500, 2)
	if a == b {
		t.Fatal("different seeds produced identical results; the seed is being ignored")
	}
}

func TestMeasurementCountScalesWithSize(t *testing.T) {
	// 3 operators x 3 trials per part.
	if got := analysis.Run(100, 1).Measurements; got != 900 {
		t.Fatalf("measurements = %d, want 900", got)
	}
}

func TestVarianceComponentsArePlausible(t *testing.T) {
	r := analysis.Run(2000, 7)

	if r.Repeatability <= 0 {
		t.Errorf("repeatability = %f, want > 0", r.Repeatability)
	}
	// GaugeRR is the root sum of squares of its two components, so it can
	// never be smaller than either one.
	if r.GaugeRR < r.Repeatability {
		t.Errorf("gauge RR %f is below repeatability %f", r.GaugeRR, r.Repeatability)
	}
	// Parts are spread over ~4 units while gauge noise is ~0.35, so a usable
	// gauge must resolve the parts. If this ever fails the synthetic data has
	// stopped resembling a real study.
	if r.PartVariation <= r.GaugeRR {
		t.Errorf("part variation %f <= gauge RR %f: the gauge cannot see the parts", r.PartVariation, r.GaugeRR)
	}
}

func BenchmarkRun(b *testing.B) {
	for i := 0; i < b.N; i++ {
		analysis.Run(analysis.DefaultSize, int64(i))
	}
}
