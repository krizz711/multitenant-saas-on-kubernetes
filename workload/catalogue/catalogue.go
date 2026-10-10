// Package catalogue is the demonstration application's domain: a product
// catalogue for an online shop.
//
// It replaced a gauge repeatability study, and the replacement is the point.
// The control plane did not change by a single line to accommodate it - the
// same admission tagging, queue, token gateway, autoscaler and priority
// classes govern a shop exactly as they governed a measurement-systems tool.
// TestControlPlaneDoesNotImportWorkload is what makes that claim checkable
// rather than rhetorical.
//
// The shapes this package must preserve are the only thing the control plane
// cares about: a cheap read a shopper waits on, an expensive model call, and a
// CPU-bound job nobody waits on.
package catalogue

import (
	"math"
	"sort"
	"strings"
	"unicode"
)

// Product is one item in a merchant's catalogue.
type Product struct {
	ID          int64   `json:"id"`
	Name        string  `json:"name"`
	Description string  `json:"description"`
	PriceINR    float64 `json:"price_inr"`
	Category    string  `json:"category"`
}

// Related is a similarity edge discovered by the re-index job: "customers
// looking at this also consider that".
type Related struct {
	ProductID int64
	RelatedID int64
	Score     float64
}

// Reindex recomputes the catalogue's similarity graph.
//
// This is the deferrable workload, and it is genuinely expensive on purpose.
// Cost is O(n^2) in the catalogue size because every product is compared with
// every other, which is what a real "related products" build does and what
// makes the job worth deferring. Claim C3 needs a job that actually competes
// for a core; anything cheaper would contend for nothing and prove nothing.
//
// topK bounds how many edges are kept per product, so the output size stays
// linear even though the computation is quadratic.
func Reindex(products []Product, topK int) []Related {
	if len(products) < 2 {
		return nil
	}
	if topK < 1 {
		topK = 1
	}

	vectors := make([]vector, len(products))
	for i, p := range products {
		vectors[i] = tfidfVector(p, products)
	}

	var edges []Related
	for i := range products {
		scored := make([]Related, 0, len(products)-1)
		for j := range products {
			if i == j {
				continue
			}
			s := cosine(vectors[i], vectors[j])
			if s > 0 {
				scored = append(scored, Related{
					ProductID: products[i].ID,
					RelatedID: products[j].ID,
					Score:     s,
				})
			}
		}
		// Stable, with an explicit tiebreak on id.
		//
		// sort.Slice is not stable, so products with equal similarity came
		// back in arbitrary order and two runs over the same catalogue could
		// produce different graphs. Every experiment here is a comparison
		// between runs, so non-determinism would surface as noise that looks
		// like a result. A unit test caught this; the ordering is now total.
		sort.SliceStable(scored, func(a, b int) bool {
			if scored[a].Score != scored[b].Score {
				return scored[a].Score > scored[b].Score
			}
			return scored[a].RelatedID < scored[b].RelatedID
		})
		if len(scored) > topK {
			scored = scored[:topK]
		}
		edges = append(edges, scored...)
	}
	return edges
}

// term is one weighted word. Vectors are SORTED SLICES rather than maps, and
// that is a correctness requirement, not a performance tweak.
//
// Go randomises map iteration order, so summing a dot product over a map gave
// results that differed in their last floating-point bits between runs. Those
// differences reshuffled near-ties in the ranking, and the "related products"
// graph came out different each rebuild. A determinism test caught it only
// when run alongside other tests, because the iteration order happened to be
// stable in isolation - the worst kind of bug to meet in an experiment.
//
// Sorted slices also let cosine merge two vectors in one pass instead of
// doing a map lookup per term, so the hot loop got faster as well.
type term struct {
	word   string
	weight float64
}

type vector []term

// tfidfVector weights a product's terms by how distinctive they are.
//
// Term frequency alone would make every product in a clothing catalogue look
// alike, because "cotton" and "size" appear everywhere. Inverse document
// frequency is what makes "waterproof" count for more than "the" - and it is
// also where most of the CPU goes, which is the point.
func tfidfVector(p Product, corpus []Product) vector {
	terms := tokenise(p.Name + " " + p.Description + " " + p.Category)
	if len(terms) == 0 {
		return nil
	}

	tf := map[string]float64{}
	for _, t := range terms {
		tf[t]++
	}

	vec := make(vector, 0, len(tf))
	n := float64(len(corpus))
	for word, count := range tf {
		df := 0
		for _, other := range corpus {
			if strings.Contains(
				strings.ToLower(other.Name+" "+other.Description+" "+other.Category),
				word,
			) {
				df++
			}
		}
		if df == 0 {
			continue
		}
		idf := math.Log(n / float64(df))
		vec = append(vec, term{word: word, weight: (count / float64(len(terms))) * idf})
	}

	// Sorting here is what makes every later sum reproducible.
	sort.Slice(vec, func(i, j int) bool { return vec[i].word < vec[j].word })
	return vec
}

// cosine merges two sorted vectors in a single pass. Both are in the same
// term order, so the additions happen in the same sequence every time and the
// result is bit-for-bit reproducible.
func cosine(a, b vector) float64 {
	if len(a) == 0 || len(b) == 0 {
		return 0
	}

	var dot, normA, normB float64
	for _, t := range a {
		normA += t.weight * t.weight
	}
	for _, t := range b {
		normB += t.weight * t.weight
	}
	if normA == 0 || normB == 0 {
		return 0
	}

	for i, j := 0, 0; i < len(a) && j < len(b); {
		switch {
		case a[i].word == b[j].word:
			dot += a[i].weight * b[j].weight
			i++
			j++
		case a[i].word < b[j].word:
			i++
		default:
			j++
		}
	}
	return dot / (math.Sqrt(normA) * math.Sqrt(normB))
}

func tokenise(s string) []string {
	fields := strings.FieldsFunc(strings.ToLower(s), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
	out := fields[:0]
	for _, f := range fields {
		if len(f) > 2 && !stopWords[f] {
			out = append(out, f)
		}
	}
	return out
}

// stopWords are dropped because they appear in nearly every description and
// would dominate the similarity scores without carrying any meaning.
var stopWords = map[string]bool{
	"the": true, "and": true, "for": true, "with": true, "this": true,
	"that": true, "from": true, "our": true, "you": true, "your": true,
	"are": true, "has": true, "its": true, "all": true, "can": true,
}
