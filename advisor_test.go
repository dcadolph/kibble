package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestProviderReplies checks that each provider reads its own response shape,
// sends the credential in the header its API expects, and reports a non-200 as
// an error rather than parsing it as an answer.
func TestProviderReplies(t *testing.T) {
	t.Parallel()
	tests := []struct {
		Name       string
		Status     int
		Body       string
		WantHeader [2]string
		WantReply  string
		WantOK     bool
	}{{ // Test 0: Claude returns text blocks.
		Name: "anthropic", Status: 200,
		Body:       `{"content":[{"type":"text","text":"one "},{"type":"text","text":"two"}]}`,
		WantHeader: [2]string{"x-api-key", "secret"},
		WantReply:  "one two", WantOK: true,
	}, { // Test 1: a Claude refusal is an error, not an empty answer.
		Name: "anthropic", Status: 200,
		Body:   `{"content":[],"stop_reason":"refusal"}`,
		WantOK: false,
	}, { // Test 2: OpenAI returns choices.
		Name: "openai", Status: 200,
		Body:       `{"choices":[{"message":{"content":"hello"}}]}`,
		WantHeader: [2]string{"Authorization", "Bearer secret"},
		WantReply:  "hello", WantOK: true,
	}, { // Test 3: OpenAI with no choices is an error.
		Name: "openai", Status: 200, Body: `{"choices":[]}`, WantOK: false,
	}, { // Test 4: Ollama returns one message.
		Name: "ollama", Status: 200,
		Body: `{"message":{"content":"hello"}}`, WantReply: "hello", WantOK: true,
	}, { // Test 5: a provider error status is reported, not parsed.
		Name: "ollama", Status: 500, Body: `{"error":"boom"}`, WantOK: false,
	}, { // Test 6: an empty reply is an error rather than a silent pass.
		Name: "ollama", Status: 200, Body: `{"message":{"content":"  "}}`, WantOK: false,
	}}
	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d", testNum), func(t *testing.T) {
			t.Parallel()
			var gotHeader string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if test.WantHeader[0] != "" {
					gotHeader = r.Header.Get(test.WantHeader[0])
				}
				w.WriteHeader(test.Status)
				_, _ = w.Write([]byte(test.Body))
			}))
			defer srv.Close()

			var a Advisor
			switch test.Name {
			case "anthropic":
				a = &anthropic{key: "secret", model: "m", url: srv.URL, http: srv.Client()}
			case "openai":
				a = &openAI{key: "secret", model: "m", url: srv.URL, http: srv.Client()}
			case "ollama":
				a = &ollama{url: srv.URL, model: "m", http: srv.Client()}
			}

			got, err := a.Chat(context.Background(), "system", "user")
			if test.WantOK != (err == nil) {
				t.Fatalf("err = %v, want ok = %v", err, test.WantOK)
			}
			if !test.WantOK {
				if !strings.Contains(err.Error(), "advisor") {
					t.Errorf("error not tagged as an advisor failure: %v", err)
				}
				return
			}
			if got != test.WantReply {
				t.Errorf("reply = %q, want %q", got, test.WantReply)
			}
			if test.WantHeader[0] != "" && gotHeader != test.WantHeader[1] {
				t.Errorf("%s = %q, want %q", test.WantHeader[0], gotHeader, test.WantHeader[1])
			}
		})
	}
}

// TestNewAdvisorSelection checks the provider order is fixed and that an
// unconfigured environment reports no advisor rather than guessing.
func TestNewAdvisorSelection(t *testing.T) {
	tests := []struct {
		Env      map[string]string
		WantType string
		WantOK   bool
	}{{ // Test 0: a Claude key selects Claude.
		Env:      map[string]string{"ANTHROPIC_API_KEY": "k"},
		WantType: "*main.anthropic", WantOK: true,
	}, { // Test 1: with both keys, Claude wins by fixed order.
		Env:      map[string]string{"ANTHROPIC_API_KEY": "k", "OPENAI_API_KEY": "k"},
		WantType: "*main.anthropic", WantOK: true,
	}, { // Test 2: an OpenAI key alone selects OpenAI.
		Env:      map[string]string{"OPENAI_API_KEY": "k"},
		WantType: "*main.openAI", WantOK: true,
	}, { // Test 3: an explicit choice overrides the keys present.
		Env:      map[string]string{"KIBBLE_ADVISOR": "ollama", "ANTHROPIC_API_KEY": "k"},
		WantType: "*main.ollama", WantOK: true,
	}, { // Test 4: no keys falls through to a local Ollama.
		Env: map[string]string{}, WantType: "*main.ollama", WantOK: true,
	}, { // Test 5: an unknown explicit choice reports nothing rather than guessing.
		Env: map[string]string{"KIBBLE_ADVISOR": "nope"}, WantOK: false,
	}}
	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d", testNum), func(t *testing.T) {
			for _, k := range []string{"KIBBLE_ADVISOR", "ANTHROPIC_API_KEY", "OPENAI_API_KEY"} {
				t.Setenv(k, "")
			}
			for k, v := range test.Env {
				t.Setenv(k, v)
			}
			got, ok := NewAdvisor()
			if ok != test.WantOK {
				t.Fatalf("ok = %v, want %v", ok, test.WantOK)
			}
			if !ok {
				return
			}
			if name := fmt.Sprintf("%T", got); name != test.WantType {
				t.Errorf("advisor = %s, want %s", name, test.WantType)
			}
		})
	}
}
