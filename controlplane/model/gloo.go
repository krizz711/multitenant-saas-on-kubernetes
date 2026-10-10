package model

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// Gloo calls an OpenAI-compatible chat-completions endpoint behind OAuth2
// client credentials.
//
// It exists mainly to prove a point the architecture claims: the gateway, the
// budget, the cache, the coalescer and every metric are indifferent to which
// vendor is behind model.Client. Swapping Gemini for Gloo is an environment
// variable, and nothing downstream is recompiled or reconfigured. A control
// plane that only worked with one provider would not be a control plane.
//
// Being OpenAI-compatible, the request and response shapes here also fit
// OpenAI, Groq, Together, OpenRouter and most self-hosted servers. Only the
// token exchange is Gloo-specific.
type Gloo struct {
	clientID     string
	clientSecret string
	model        string
	tokenURL     string
	chatURL      string
	client       *http.Client

	mu      sync.Mutex
	token   string
	expires time.Time
}

func NewGloo(clientID, clientSecret, model string, timeout time.Duration) *Gloo {
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	return &Gloo{
		clientID:     clientID,
		clientSecret: clientSecret,
		model:        model,
		tokenURL:     "https://platform.ai.gloo.com/oauth2/token",
		chatURL:      "https://platform.ai.gloo.com/ai/v2/chat/completions",
		client:       &http.Client{Timeout: timeout},
	}
}

type oauthResponse struct {
	AccessToken string `json:"access_token"`
	ExpiresIn   int    `json:"expires_in"`
	Error       string `json:"error"`
}

// accessToken fetches and caches a bearer token.
//
// Tokens last an hour, so fetching one per request would double the latency of
// every model call and triple the traffic for no benefit. It is refreshed a
// minute early, because a token that expires mid-flight fails the call it was
// fetched for.
func (g *Gloo) accessToken(ctx context.Context) (string, error) {
	g.mu.Lock()
	defer g.mu.Unlock()

	if g.token != "" && time.Now().Before(g.expires) {
		return g.token, nil
	}

	form := url.Values{}
	form.Set("grant_type", "client_credentials")
	form.Set("scope", "api/access")

	req, err := http.NewRequestWithContext(
		ctx, http.MethodPost, g.tokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return "", fmt.Errorf("build token request: %w", err)
	}
	basic := base64.StdEncoding.EncodeToString(
		[]byte(g.clientID + ":" + g.clientSecret))
	req.Header.Set("Authorization", "Basic "+basic)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := g.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("fetch token: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	var parsed oauthResponse
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return "", fmt.Errorf("decode token (http %d): %w", resp.StatusCode, err)
	}
	if parsed.AccessToken == "" {
		return "", fmt.Errorf("no token returned (http %d): %s", resp.StatusCode, parsed.Error)
	}

	ttl := time.Duration(parsed.ExpiresIn) * time.Second
	if ttl <= 0 {
		ttl = time.Hour
	}
	g.token = parsed.AccessToken
	g.expires = time.Now().Add(ttl - time.Minute)
	return g.token, nil
}

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type chatRequest struct {
	Model       string        `json:"model,omitempty"`
	AutoRouting bool          `json:"auto_routing,omitempty"`
	Messages    []chatMessage `json:"messages"`
}

type chatResponse struct {
	Choices []struct {
		Message chatMessage `json:"message"`
	} `json:"choices"`
	Usage struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
	} `json:"usage"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
}

func (g *Gloo) Ask(ctx context.Context, tenant, question string) (Answer, error) {
	token, err := g.accessToken(ctx)
	if err != nil {
		return Answer{}, err
	}

	payload := chatRequest{
		Messages: []chatMessage{{Role: "user", Content: question}},
	}
	// With no model named, let the provider route. Naming one pins the cost
	// and the behaviour, which is what an experiment needs.
	if g.model == "" {
		payload.AutoRouting = true
	} else {
		payload.Model = g.model
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return Answer{}, fmt.Errorf("marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(
		ctx, http.MethodPost, g.chatURL, bytes.NewReader(body))
	if err != nil {
		return Answer{}, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := g.client.Do(req)
	if err != nil {
		return Answer{}, fmt.Errorf("call model: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	var parsed chatResponse
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return Answer{}, fmt.Errorf("decode response (http %d): %w", resp.StatusCode, err)
	}
	if parsed.Error != nil {
		return Answer{}, fmt.Errorf("model error: %s", parsed.Error.Message)
	}
	if len(parsed.Choices) == 0 {
		return Answer{}, fmt.Errorf("model returned no choices (http %d)", resp.StatusCode)
	}

	text := strings.TrimSpace(parsed.Choices[0].Message.Content)

	// The provider's own counts beat the four-characters-per-token estimate.
	// Billing a real call from a guess when the truth was reported would make
	// the cost meter wrong on purpose.
	prompt := parsed.Usage.PromptTokens
	output := parsed.Usage.CompletionTokens
	if prompt == 0 {
		prompt = EstimateTokens(question)
	}
	if output == 0 {
		output = EstimateTokens(text)
	}

	name := g.model
	if name == "" {
		name = "gloo-auto"
	}

	return Answer{
		Text:         text,
		PromptTokens: prompt,
		OutputTokens: output,
		Model:        name,
	}, nil
}
