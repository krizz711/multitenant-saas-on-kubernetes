package model

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// Gemini calls Google's hosted model. It satisfies model.Client, so the
// gateway, the handlers and every metric work with it unchanged - which is
// what the interface was for.
//
// The free tier is rate limited, and that is precisely why the gateway in
// front of it matters rather than being an optimisation. A per-tenant budget,
// a cache and request coalescing are what stop three tenants' agents from
// exhausting a shared quota within a minute. Running this behind the gateway
// is the design the architecture diagram describes.
type Gemini struct {
	apiKey  string
	model   string
	client  *http.Client
	timeout time.Duration
}

func NewGemini(apiKey, model string, timeout time.Duration) *Gemini {
	if model == "" {
		model = "gemini-2.0-flash"
	}
	if timeout <= 0 {
		timeout = 20 * time.Second
	}
	return &Gemini{
		apiKey:  apiKey,
		model:   model,
		timeout: timeout,
		client:  &http.Client{Timeout: timeout},
	}
}

type geminiRequest struct {
	Contents []geminiContent `json:"contents"`
}

type geminiContent struct {
	Parts []geminiPart `json:"parts"`
}

type geminiPart struct {
	Text string `json:"text"`
}

type geminiResponse struct {
	Candidates []struct {
		Content struct {
			Parts []geminiPart `json:"parts"`
		} `json:"content"`
	} `json:"candidates"`
	UsageMetadata struct {
		PromptTokenCount     int `json:"promptTokenCount"`
		CandidatesTokenCount int `json:"candidatesTokenCount"`
	} `json:"usageMetadata"`
	Error *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

func (g *Gemini) Ask(ctx context.Context, tenant, question string) (Answer, error) {
	body, err := json.Marshal(geminiRequest{
		Contents: []geminiContent{{Parts: []geminiPart{{Text: question}}}},
	})
	if err != nil {
		return Answer{}, fmt.Errorf("marshal request: %w", err)
	}

	url := fmt.Sprintf(
		"https://generativelanguage.googleapis.com/v1beta/models/%s:generateContent",
		g.model)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return Answer{}, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	// The key goes in a header, never the query string: URLs end up in proxy
	// logs, browser history and Prometheus target lists.
	req.Header.Set("x-goog-api-key", g.apiKey)

	resp, err := g.client.Do(req)
	if err != nil {
		return Answer{}, fmt.Errorf("call model: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	var parsed geminiResponse
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return Answer{}, fmt.Errorf("decode response (http %d): %w", resp.StatusCode, err)
	}
	if parsed.Error != nil {
		return Answer{}, fmt.Errorf("model error %d: %s", parsed.Error.Code, parsed.Error.Message)
	}
	if len(parsed.Candidates) == 0 || len(parsed.Candidates[0].Content.Parts) == 0 {
		return Answer{}, fmt.Errorf("model returned no candidates (http %d)", resp.StatusCode)
	}

	var sb strings.Builder
	for _, p := range parsed.Candidates[0].Content.Parts {
		sb.WriteString(p.Text)
	}
	text := strings.TrimSpace(sb.String())

	// Prefer the provider's own token counts over the four-characters-per-token
	// estimate. The estimate exists for the stub, where nothing authoritative
	// is available; billing a real call from a guess when the provider told us
	// the true number would make the cost meter wrong on purpose.
	prompt := parsed.UsageMetadata.PromptTokenCount
	output := parsed.UsageMetadata.CandidatesTokenCount
	if prompt == 0 {
		prompt = EstimateTokens(question)
	}
	if output == 0 {
		output = EstimateTokens(text)
	}

	return Answer{
		Text:         text,
		PromptTokens: prompt,
		OutputTokens: output,
		Model:        g.model,
	}, nil
}
