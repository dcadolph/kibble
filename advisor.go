package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"
)

// ErrAdvisor marks a failure inside the optional model layer. Nothing in the
// verification engine depends on it: an advisor error stops a suggestion, it
// never changes a pass or a fail.
var ErrAdvisor = errors.New("advisor")

// Advisor answers one prompt. It is the whole of kibble's optional model
// layer, and it is never consulted while deciding whether a documented step
// passed. It only proposes a .kibble.yml for a human to read and commit, so a
// run with no advisor configured verifies exactly as much as one with it.
type Advisor interface {
	// Chat sends the system and user prompts and returns the reply text.
	Chat(ctx context.Context, system, user string) (string, error)
}

// AdvisorFunc adapts a function to the Advisor interface.
type AdvisorFunc func(ctx context.Context, system, user string) (string, error)

// Chat calls f.
func (f AdvisorFunc) Chat(ctx context.Context, system, user string) (string, error) {
	return f(ctx, system, user)
}

// advisorTimeout bounds one model call. A suggestion is a convenience, so it
// fails fast rather than holding up the run.
const advisorTimeout = 90 * time.Second

// NewAdvisor returns the advisor the environment configures, and reports
// whether one is available. Providers are checked in a fixed order so the
// choice is reproducible: an explicit KIBBLE_ADVISOR wins, then Anthropic,
// then OpenAI, then a local Ollama. No advisor is a normal state, not an
// error, because kibble is fully useful without one.
func NewAdvisor() (Advisor, bool) {
	client := &http.Client{Timeout: advisorTimeout}
	switch strings.ToLower(strings.TrimSpace(os.Getenv("KIBBLE_ADVISOR"))) {
	case "anthropic", "claude":
		return newAnthropic(os.Getenv("ANTHROPIC_API_KEY"), client)
	case "openai", "chatgpt":
		return newOpenAI(os.Getenv("OPENAI_API_KEY"), client)
	case "ollama", "local":
		return newOllama(client)
	case "":
	default:
		return nil, false
	}
	if a, ok := newAnthropic(os.Getenv("ANTHROPIC_API_KEY"), client); ok {
		return a, true
	}
	if o, ok := newOpenAI(os.Getenv("OPENAI_API_KEY"), client); ok {
		return o, true
	}
	return newOllama(client)
}

// advisorHelp explains how to turn the optional layer on, for the message
// printed when a suggestion is asked for and nothing is configured.
const advisorHelp = `no model configured, so there is nothing to suggest with.
Set one of these and run again:
  ANTHROPIC_API_KEY=...    use Claude
  OPENAI_API_KEY=...       use ChatGPT
  KIBBLE_ADVISOR=ollama    use a local Ollama at http://localhost:11434
Everything else kibble does works without any of them.`

// firstJSONObject returns the first balanced top-level JSON object or array in
// a reply, so a model that wraps its answer in prose or a fenced block still
// parses. Braces inside strings are ignored.
func firstJSONObject(s string) (string, error) {
	start := strings.IndexAny(s, "{[")
	if start < 0 {
		return "", fmt.Errorf("%w: reply contained no JSON", ErrAdvisor)
	}
	open := rune(s[start])
	close := '}'
	if open == '[' {
		close = ']'
	}
	depth, inString, escaped := 0, false, false
	for i, r := range s[start:] {
		switch {
		case escaped:
			escaped = false
		case r == '\\' && inString:
			escaped = true
		case r == '"':
			inString = !inString
		case inString:
		case r == open:
			depth++
		case r == close:
			depth--
			if depth == 0 {
				return s[start : start+i+1], nil
			}
		}
	}
	return "", fmt.Errorf("%w: reply had unbalanced JSON", ErrAdvisor)
}
