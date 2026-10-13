package store

import (
	"fmt"
	"hash/fnv"
	"math/rand"
	"sort"

	"github.com/krizz711/multitenant-saas-on-kubernetes/workload/catalogue"
)

// SeedCatalogue builds a merchant's product list.
//
// Each tenant sells something different, and that is load-bearing for the
// demonstration rather than flavour. The cross-tenant isolation claims - that
// a search result, a cached model answer or a similarity edge from one
// merchant can never surface for another - are only visible if the merchants
// are visibly different. Three identical catalogues would hide a leak.
//
// Generation is deterministic in the tenant name, so the same merchant gets
// the same catalogue on every rebuild and a re-index run is reproducible.
func SeedCatalogue(tenant string, count int) []catalogue.Product {
	if count < 1 {
		count = 1
	}

	h := fnv.New64a()
	_, _ = h.Write([]byte(tenant))
	rng := rand.New(rand.NewSource(int64(h.Sum64())))

	trade := tradeFor(tenant)
	products := make([]catalogue.Product, 0, count)

	for i := 0; i < count; i++ {
		category := trade.categories[i%len(trade.categories)]
		names := trade.materialNames()
		material := names[rng.Intn(len(names))]
		quality := qualifiers[rng.Intn(len(qualifiers))]
		// The feature follows from the material rather than being drawn
		// independently, so the description never contradicts the name.
		feature := trade.materials[material]

		products = append(products, catalogue.Product{
			Name: fmt.Sprintf("%s %s %s %d", quality, material, category, 100+i),
			Description: fmt.Sprintf(
				"%s %s %s for everyday use. %s. Shipped by %s.",
				quality, material, category, feature, trade.brand),
			// A wide price spread so a cost-per-tenant figure has something to
			// vary against, and so "cheapest" is a meaningful query.
			PriceINR: float64(199 + rng.Intn(24_800)),
			Category: category,
		})
	}
	return products
}

type trade struct {
	brand      string
	categories []string
	// materials maps a material to the claim that material actually implies.
	//
	// Pairing them randomly produced a catalogue that contradicted itself - a
	// goretex jacket described as "rated to minus ten degrees" and nothing
	// anywhere claiming to be waterproof. The assistant then correctly refused
	// to recommend anything for rain, which looks like a broken assistant and
	// is really broken data. Grounded answers are only as good as the ground.
	materials map[string]string
}

func (t trade) materialNames() []string {
	names := make([]string, 0, len(t.materials))
	for m := range t.materials {
		names = append(names, m)
	}
	sort.Strings(names) // deterministic: the same merchant twice is the same catalogue
	return names
}

// tradeFor maps a tenant to a line of business. Unknown tenants fall back to
// the general store rather than failing: a new tenant should onboard without
// a code change, which is the whole premise of the Helm chart.
func tradeFor(tenant string) trade {
	switch tenant {
	case "tenant-a":
		return trade{
			brand:      "Meridian Electronics",
			categories: []string{"headphones", "keyboard", "monitor", "webcam", "powerbank", "router"},
			materials: map[string]string{
				"noise-cancelling": "Blocks engine and cabin noise, with a 30 hour battery. Ideal for travel and flights",
				"fast-charge":      "USB-C fast charging, zero to full in under an hour",
				"wireless":         "Low latency wireless connection with no dongle required",
				"backlit":          "Backlit with adjustable brightness for working after dark",
				"compact":          "Folds flat and weighs under 300 grams, built for a carry-on",
			},
		}
	case "tenant-b":
		return trade{
			brand:      "Northfield Outdoor",
			categories: []string{"jacket", "boots", "backpack", "tent", "gloves", "fleece"},
			materials: map[string]string{
				"goretex": "Fully waterproof with taped seams, built for heavy rain and storms",
				"down":    "Rated to minus ten degrees, the warmest thing we sell for winter",
				"merino":  "Breathable and quick drying, warm even when damp",
				"ripstop": "Packs down to one litre and resists tearing on rock",
				"canvas":  "Hard wearing everyday fabric, not waterproof",
			},
		}
	default:
		return trade{
			brand:      "Harbour Home",
			categories: []string{"skillet", "kettle", "blender", "storage", "lamp", "cushion"},
			materials: map[string]string{
				"cast iron": "Oven proof to 250 degrees and naturally non stick once seasoned",
				"stoneware": "Hand finished in small batches, dishwasher and microwave safe",
				"bamboo":    "Lightweight, sustainably grown, stackable to save cupboard space",
				"stainless": "Dishwasher safe and will not stain or rust",
				"linen":     "Washable, softens with use, oeko-tex certified",
			},
		}
	}
}

var qualifiers = []string{"Classic", "Pro", "Lite", "Heritage", "Everyday", "Studio"}
