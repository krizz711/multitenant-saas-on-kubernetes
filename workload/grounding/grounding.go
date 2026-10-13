// Package grounding turns a shopper's question into a prompt grounded in that
// merchant's own catalogue.
//
// It sits BELOW the token gateway and implements model.Client, so the layering
// reads: gateway -> grounding -> vendor. That order is a correctness
// requirement, not a preference.
//
// When grounding happened above the gateway, the gateway cached on the fully
// grounded prompt. That prompt is a short instruction, five full product
// descriptions, and then the question - so two different questions that
// retrieve the same products produced text that was about ninety-five per cent
// identical. They cleared a 0.90 similarity threshold and collided. Asking for
// a warm jacket returned the cached answer about a waterproof one, flagged as
// a cache hit. A cache that answers the wrong question is worse than no cache.
//
// With grounding underneath, the gateway sees only what the shopper actually
// asked, which is the thing that should decide whether two requests are the
// same.
package grounding

import (
	"context"
	"fmt"
	"strings"

	"github.com/krizz711/multitenant-saas-on-kubernetes/controlplane/model"
	"github.com/krizz711/multitenant-saas-on-kubernetes/workload/catalogue"
)

// Retriever is the slice of the store this package needs. Narrow on purpose:
// grounding should not be able to write to the catalogue it reads.
type Retriever interface {
	Search(ctx context.Context, q string, limit int) ([]catalogue.Product, error)
}

// Client grounds a question and forwards it to the real model.
type Client struct {
	inner model.Client
	store Retriever
	limit int
}

func New(inner model.Client, store Retriever, limit int) *Client {
	if limit <= 0 {
		limit = 5
	}
	return &Client{inner: inner, store: store, limit: limit}
}

func (c *Client) Ask(ctx context.Context, tenant, question string) (model.Answer, error) {
	products, err := c.store.Search(ctx, Keywords(question), c.limit)
	if err != nil {
		// Answer ungrounded rather than fail. A catalogue lookup that failed
		// is a degraded answer; refusing the request outright is an outage.
		products = nil
	}
	return c.inner.Ask(ctx, tenant, BuildPrompt(question, products))
}

// BuildPrompt is exported so a handler can show the reader exactly what was
// sent to the model, which is the difference between a demonstration and a
// magic trick.
func BuildPrompt(question string, products []catalogue.Product) string {
	var sb strings.Builder
	sb.WriteString("You are a shopping assistant. Answer using only the products listed.\n")
	sb.WriteString("Be brief: two sentences at most, and name the product you recommend.\n")
	sb.WriteString("If nothing listed suits the request, say so plainly rather than recommending the closest item.\n\n")

	if len(products) == 0 {
		sb.WriteString("Products: none matched.\n\n")
	} else {
		sb.WriteString("Products:\n")
		for _, p := range products {
			fmt.Fprintf(&sb, "- %s (%s) Rs %.0f: %s\n",
				p.Name, p.Category, p.PriceINR, p.Description)
		}
		sb.WriteString("\n")
	}

	sb.WriteString("Question: ")
	sb.WriteString(question)
	return sb.String()
}

// Keywords reduces a question to a term worth searching on, because "do you
// have a waterproof jacket" matches no product name verbatim.
func Keywords(question string) string {
	for _, f := range strings.Fields(strings.ToLower(question)) {
		trimmed := strings.Trim(f, "?,.!\"'")
		if len(trimmed) > 4 && !questionWords[trimmed] {
			return trimmed
		}
	}
	return ""
}

var questionWords = map[string]bool{
	"which": true, "where": true, "there": true, "about": true, "would": true,
	"could": true, "should": true, "please": true, "whats": true, "under": true,
}
