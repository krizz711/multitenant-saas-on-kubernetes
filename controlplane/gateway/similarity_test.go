package gateway_test

import (
	"testing"

	"github.com/krizz711/multitenant-saas-on-kubernetes/controlplane/gateway"
)

// TestSimilarityOrderingOnBenchmarkPairs is where the cache threshold gets its
// justification.
//
// A threshold picked because it produced a pleasing hit rate is worthless; the
// question is whether paraphrases and materially-different questions actually
// separate, and by how much. This prints the scores so the gap can be quoted,
// and asserts the ordering that any usable threshold depends on.
func TestSimilarityOrderingOnBenchmarkPairs(t *testing.T) {
	e := gateway.LexicalEmbedder{}

	sim := func(a, b string) float64 {
		return gateway.Cosine(e.Embed(a), e.Embed(b))
	}

	grr := "What does a %GR&R of 18% mean for this gauge?"
	paraphrase := "Is 18 percent gauge R and R acceptable?"
	distinct := "What does an ndc of 18 mean for this gauge?"
	unrelated := "Why did line 3 drift out of control at 14:20 today?"

	identical := sim(grr, grr)
	para := sim(grr, paraphrase)
	dist := sim(grr, distinct)
	unrel := sim(grr, unrelated)

	t.Logf("identical  : %.3f", identical)
	t.Logf("paraphrase : %.3f  (should hit)", para)
	t.Logf("distinct   : %.3f  (must NOT hit)", dist)
	t.Logf("unrelated  : %.3f  (must NOT hit)", unrel)

	if identical < 0.999 {
		t.Errorf("identical text scored %.3f; the embedder is not deterministic", identical)
	}
	// The ordering is the part that must hold for ANY threshold to work.
	if !(para > unrel) {
		t.Errorf("paraphrase %.3f did not outscore unrelated %.3f", para, unrel)
	}
	// The honest finding: a purely lexical embedder scores the DISTINCT pair
	// highly, because "What does a ... of 18 ... mean for this gauge?" shares
	// almost every word with the paraphrase target. If dist >= para, no single
	// threshold can separate them and the limitation must be reported rather
	// than tuned around.
	if dist >= para {
		t.Logf("NOTE: distinct (%.3f) scores at or above paraphrase (%.3f).", dist, para)
		t.Logf("      A lexical embedder cannot separate this pair; only a")
		t.Logf("      semantic one can. This is a reportable limitation, not a bug.")
	}
}
