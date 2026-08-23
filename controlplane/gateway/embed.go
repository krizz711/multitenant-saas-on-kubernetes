// Package gateway is the only route to the language model. It enforces a
// per-tenant token budget, serves near-duplicate prompts from a cache, and
// collapses identical concurrent prompts into a single upstream call.
//
// It implements model.Client, so it slots underneath the API handlers without
// any of them changing. That was the point of making model.Client an interface
// in Module 2.
package gateway

import (
	"hash/fnv"
	"math"
	"strings"
	"unicode"
)

// Embedder turns a prompt into a vector so that two differently-worded
// questions can be compared numerically.
//
// It is an interface because the quality of the cache is entirely the quality
// of this component, and it is the piece most likely to be swapped. A
// hosted embedding API would capture genuine semantics; the local
// implementation below captures lexical overlap only. Naming that honestly
// matters more than the result looking good.
type Embedder interface {
	Embed(text string) []float32
	Name() string
}

// Dims is the vector width. 256 is enough to keep hash collisions rare for a
// vocabulary this small, and small enough that a linear scan over a tenant's
// cached entries costs microseconds.
const Dims = 256

// LexicalEmbedder is a hashing vectoriser over word unigrams and bigrams,
// L2-normalised so a dot product is a cosine similarity.
//
// It is deterministic, needs no network and no API key, which is what makes
// every experiment in this project reproducible and runnable in CI. Its
// limitation is real and must be stated in the paper: it measures LEXICAL
// overlap, not meaning. "Is 18 percent gauge R and R acceptable?" and "What
// does a %GR&R of 18% mean?" share words and will score highly; two
// paraphrases with no shared vocabulary will not. A hosted embedding model
// would close that gap, at the cost of a rate-limited network call in the
// measurement path.
//
// Bigrams are included because unigrams alone cannot distinguish "gauge R&R of
// 18" from "ndc of 18" strongly enough - and that pair is precisely the
// distinct-questions case the benchmark says must NOT collapse.
type LexicalEmbedder struct{}

func (LexicalEmbedder) Name() string { return "lexical-hash-256" }

func (LexicalEmbedder) Embed(text string) []float32 {
	tokens := tokenise(text)
	vec := make([]float32, Dims)

	for i, tok := range tokens {
		vec[bucket(tok)] += 1
		if i+1 < len(tokens) {
			vec[bucket(tok+" "+tokens[i+1])] += 1
		}
	}

	// L2-normalise so that cosine similarity is a plain dot product and long
	// prompts do not automatically score higher than short ones.
	var sum float64
	for _, v := range vec {
		sum += float64(v) * float64(v)
	}
	if sum == 0 {
		return vec
	}
	norm := float32(math.Sqrt(sum))
	for i := range vec {
		vec[i] /= norm
	}
	return vec
}

func bucket(s string) uint32 {
	h := fnv.New32a()
	_, _ = h.Write([]byte(s))
	return h.Sum32() % Dims
}

// tokenise lowercases and splits on anything that is not a letter or digit, so
// "%GR&R" and "gauge R and R" reduce to comparable pieces. Numbers are kept:
// "18%" versus "50%" is a different question, and dropping digits would make
// the cache answer the wrong one.
func tokenise(text string) []string {
	fields := strings.FieldsFunc(strings.ToLower(text), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
	out := fields[:0]
	for _, f := range fields {
		if len(f) > 1 || (len(f) == 1 && unicode.IsDigit(rune(f[0]))) {
			out = append(out, f)
		}
	}
	return out
}

// Cosine assumes both vectors are already L2-normalised, which Embed
// guarantees, so it is a dot product.
func Cosine(a, b []float32) float64 {
	if len(a) != len(b) {
		return 0
	}
	var dot float64
	for i := range a {
		dot += float64(a[i]) * float64(b[i])
	}
	return dot
}
