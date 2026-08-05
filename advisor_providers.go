package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
)

// Provider defaults. Each client is deliberately small: kibble asks one
// question and reads one answer, so a full SDK would be weight the tool
// carries for a feature most runs never use.
const (
	// anthropicURL is the Claude Messages API endpoint.
	anthropicURL = "https://api.anthropic.com/v1/messages"
	// anthropicVersion is the required API version header value.
	anthropicVersion = "2023-06-01"
	// anthropicModel is the default Claude model.
	anthropicModel = "claude-opus-4-8"
	// openaiURL is the OpenAI chat completions endpoint.
	openaiURL = "https://api.openai.com/v1/chat/completions"
	// openaiModel is the default OpenAI model.
	openaiModel = "gpt-4o"
	// ollamaModel is the default local model.
	ollamaModel = "llama3.1"
	// advisorMaxTokens bounds the reply, which is one JSON object.
	advisorMaxTokens = 4096
)

// envOr returns the environment value for key, or fallback when unset.
func envOr(key, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return fallback
}

// postJSON sends v to url with the given headers and returns the response
// body, failing when the status is not 200 so a provider error is named
// rather than parsed as a reply.
func postJSON(ctx context.Context, c *http.Client, url string, headers map[string]string,
	v any) ([]byte, error) {
	body, err := json.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("%w: encode request: %w", ErrAdvisor, err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("%w: build request: %w", ErrAdvisor, err)
	}
	req.Header.Set("Content-Type", "application/json")
	for k, val := range headers {
		req.Header.Set(k, val)
	}
	resp, err := c.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%w: request: %w", ErrAdvisor, err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("%w: read body: %w", ErrAdvisor, err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%w: status %d: %s", ErrAdvisor, resp.StatusCode,
			truncate(strings.TrimSpace(string(raw)), 200))
	}
	return raw, nil
}

// anthropic calls the Claude Messages API.
type anthropic struct {
	// key authenticates requests and is never logged.
	key string
	// model is the Claude model id.
	model string
	// url is the endpoint, overridable for tests.
	url string
	// http performs requests.
	http *http.Client
}

// newAnthropic returns a Claude advisor, or false when no key is set.
func newAnthropic(key string, c *http.Client) (Advisor, bool) {
	if strings.TrimSpace(key) == "" {
		return nil, false
	}
	return &anthropic{
		key:   key,
		model: envOr("KIBBLE_ADVISOR_MODEL", anthropicModel),
		url:   envOr("ANTHROPIC_BASE_URL", anthropicURL),
		http:  c,
	}, true
}

// Chat sends the prompts to Claude and returns the reply text.
func (a *anthropic) Chat(ctx context.Context, system, user string) (string, error) {
	raw, err := postJSON(ctx, a.http, a.url, map[string]string{
		"x-api-key":         a.key,
		"anthropic-version": anthropicVersion,
	}, map[string]any{
		"model":      a.model,
		"max_tokens": advisorMaxTokens,
		"system":     system,
		"messages":   []map[string]string{{"role": "user", "content": user}},
	})
	if err != nil {
		return "", err
	}
	var out struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		StopReason string `json:"stop_reason"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return "", fmt.Errorf("%w: decode reply: %w", ErrAdvisor, err)
	}
	if out.StopReason == "refusal" {
		return "", fmt.Errorf("%w: the model declined the request", ErrAdvisor)
	}
	var b strings.Builder
	for _, block := range out.Content {
		if block.Type == "text" {
			b.WriteString(block.Text)
		}
	}
	return nonEmptyReply(b.String())
}

// openAI calls the OpenAI chat completions API.
type openAI struct {
	// key authenticates requests and is never logged.
	key string
	// model is the OpenAI model id.
	model string
	// url is the endpoint, overridable for tests.
	url string
	// http performs requests.
	http *http.Client
}

// newOpenAI returns an OpenAI advisor, or false when no key is set.
func newOpenAI(key string, c *http.Client) (Advisor, bool) {
	if strings.TrimSpace(key) == "" {
		return nil, false
	}
	return &openAI{
		key:   key,
		model: envOr("KIBBLE_ADVISOR_MODEL", openaiModel),
		url:   envOr("OPENAI_BASE_URL", openaiURL),
		http:  c,
	}, true
}

// Chat sends the prompts to OpenAI and returns the reply text.
func (o *openAI) Chat(ctx context.Context, system, user string) (string, error) {
	raw, err := postJSON(ctx, o.http, o.url, map[string]string{
		"Authorization": "Bearer " + o.key,
	}, map[string]any{
		"model": o.model,
		"messages": []map[string]string{
			{"role": "system", "content": system},
			{"role": "user", "content": user},
		},
	})
	if err != nil {
		return "", err
	}
	var out struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return "", fmt.Errorf("%w: decode reply: %w", ErrAdvisor, err)
	}
	if len(out.Choices) == 0 {
		return "", fmt.Errorf("%w: reply had no choices", ErrAdvisor)
	}
	return nonEmptyReply(out.Choices[0].Message.Content)
}

// ollama calls a local Ollama server, so the optional layer can run with no
// account, no key, and no data leaving the machine.
type ollama struct {
	// url is the chat endpoint.
	url string
	// model is the local model name.
	model string
	// http performs requests.
	http *http.Client
}

// newOllama returns an Ollama advisor. It is always available in principle,
// since a local server needs no credential; an unreachable server surfaces as
// a request error when a suggestion is actually asked for.
func newOllama(c *http.Client) (Advisor, bool) {
	host := envOr("OLLAMA_HOST", "http://localhost:11434")
	return &ollama{
		url:   strings.TrimRight(host, "/") + "/api/chat",
		model: envOr("KIBBLE_ADVISOR_MODEL", ollamaModel),
		http:  c,
	}, true
}

// Chat sends the prompts to Ollama and returns the reply text.
func (o *ollama) Chat(ctx context.Context, system, user string) (string, error) {
	raw, err := postJSON(ctx, o.http, o.url, nil, map[string]any{
		"model":  o.model,
		"stream": false,
		"messages": []map[string]string{
			{"role": "system", "content": system},
			{"role": "user", "content": user},
		},
	})
	if err != nil {
		return "", err
	}
	var out struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return "", fmt.Errorf("%w: decode reply: %w", ErrAdvisor, err)
	}
	return nonEmptyReply(out.Message.Content)
}

// nonEmptyReply trims a reply and rejects an empty one, so a silent provider
// is reported rather than treated as an answer.
func nonEmptyReply(s string) (string, error) {
	if reply := strings.TrimSpace(s); reply != "" {
		return reply, nil
	}
	return "", fmt.Errorf("%w: the model returned no text", ErrAdvisor)
}
