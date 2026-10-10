package catalogue_test

import (
	"testing"

	"github.com/krizz711/multitenant-saas-on-kubernetes/workload/catalogue"
)

func products() []catalogue.Product {
	return []catalogue.Product{
		{ID: 1, Name: "Goretex Rain Jacket", Category: "jacket",
			Description: "Fully waterproof with taped seams, breathable shell"},
		{ID: 2, Name: "Ripstop Storm Jacket", Category: "jacket",
			Description: "Waterproof taped seams, lightweight breathable shell"},
		{ID: 3, Name: "Cast Iron Skillet", Category: "skillet",
			Description: "Oven proof pan, hand finished, non stick without coating"},
		{ID: 4, Name: "Stoneware Kettle", Category: "kettle",
			Description: "Dishwasher safe kettle, hand finished in small batches"},
	}
}

func TestRelatedProductsAreActuallyRelated(t *testing.T) {
	edges := catalogue.Reindex(products(), 1)

	best := map[int64]int64{}
	for _, e := range edges {
		if _, seen := best[e.ProductID]; !seen {
			best[e.ProductID] = e.RelatedID
		}
	}

	// The two waterproof jackets share most of their vocabulary and should
	// find each other ahead of a skillet. If this fails the similarity
	// function has stopped being about meaning, and the "related products"
	// panel would be recommending cookware to someone shopping for rainwear.
	if best[1] != 2 {
		t.Errorf("jacket 1 matched product %d, want 2", best[1])
	}
	if best[2] != 1 {
		t.Errorf("jacket 2 matched product %d, want 1", best[2])
	}
}

func TestTopKBoundsTheOutput(t *testing.T) {
	// The computation is quadratic but the output must stay linear, or a large
	// catalogue would write millions of rows on every rebuild.
	for _, k := range []int{1, 2, 3} {
		edges := catalogue.Reindex(products(), k)
		perProduct := map[int64]int{}
		for _, e := range edges {
			perProduct[e.ProductID]++
		}
		for id, n := range perProduct {
			if n > k {
				t.Errorf("topK=%d: product %d kept %d edges", k, id, n)
			}
		}
	}
}

func TestReindexIsDeterministic(t *testing.T) {
	// Every experiment is a comparison between two runs, so a workload that
	// varied run to run would make the difference between configurations
	// impossible to separate from noise.
	a := catalogue.Reindex(products(), 2)
	b := catalogue.Reindex(products(), 2)

	if len(a) != len(b) {
		t.Fatalf("edge counts differ: %d vs %d", len(a), len(b))
	}
	for i := range a {
		if a[i] != b[i] {
			t.Fatalf("edge %d differs:\n %+v\n %+v", i, a[i], b[i])
		}
	}
}

func TestNoProductIsRelatedToItself(t *testing.T) {
	for _, e := range catalogue.Reindex(products(), 3) {
		if e.ProductID == e.RelatedID {
			t.Fatalf("product %d is its own top match", e.ProductID)
		}
	}
}

func TestTinyCatalogueDoesNotPanic(t *testing.T) {
	// A freshly onboarded merchant has an empty or near-empty catalogue, and
	// the scheduled re-index will still fire.
	if got := catalogue.Reindex(nil, 5); got != nil {
		t.Errorf("empty catalogue produced %d edges", len(got))
	}
	if got := catalogue.Reindex(products()[:1], 5); got != nil {
		t.Errorf("single product produced %d edges", len(got))
	}
}

// BenchmarkReindex is how CATALOGUE_SIZE gets calibrated: the job must be
// long enough to contend for a core in the C3 experiment, and short enough
// that a demo finishes while somebody is watching.
func BenchmarkReindex(b *testing.B) {
	large := make([]catalogue.Product, 0, 400)
	for i := 0; i < 400; i++ {
		p := products()[i%4]
		p.ID = int64(i + 1)
		large = append(large, p)
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		catalogue.Reindex(large, 8)
	}
}
