package store

import (
	"fmt"
	"hash/fnv"
	"math/rand"

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
		material := trade.materials[rng.Intn(len(trade.materials))]
		quality := qualifiers[rng.Intn(len(qualifiers))]
		feature := trade.features[rng.Intn(len(trade.features))]

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
	materials  []string
	features   []string
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
			materials:  []string{"aluminium", "carbon", "matte", "brushed", "compact"},
			features: []string{
				"Noise cancelling and a 30 hour battery",
				"USB-C fast charging included",
				"Low latency wireless connection",
				"Backlit with adjustable brightness",
			},
		}
	case "tenant-b":
		return trade{
			brand:      "Northfield Outdoor",
			categories: []string{"jacket", "boots", "backpack", "tent", "gloves", "fleece"},
			materials:  []string{"merino", "ripstop", "goretex", "canvas", "down"},
			features: []string{
				"Fully waterproof with taped seams",
				"Breathable and quick drying",
				"Rated to minus ten degrees",
				"Packs down to one litre",
			},
		}
	default:
		return trade{
			brand:      "Harbour Home",
			categories: []string{"skillet", "kettle", "blender", "storage", "lamp", "cushion"},
			materials:  []string{"cast iron", "stoneware", "bamboo", "stainless", "linen"},
			features: []string{
				"Dishwasher safe and oven proof",
				"Hand finished in small batches",
				"Non stick without any coating",
				"Stackable to save cupboard space",
			},
		}
	}
}

var qualifiers = []string{"Classic", "Pro", "Lite", "Heritage", "Everyday", "Studio"}
