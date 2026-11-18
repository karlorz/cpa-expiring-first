package codex

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

func TestStartBodyIsTinyNonStreamingRequest(t *testing.T) {
	prompt := PickStartPrompt(func(n int) int {
		if n <= 0 {
			t.Fatalf("intn(%d)", n)
		}
		return n / 2
	})
	if !knownStartPrompt(prompt) {
		t.Fatalf("prompt %q is not in StartPrompts", prompt)
	}
	raw, err := StartBody("", prompt)
	if err != nil {
		t.Fatal(err)
	}
	var body map[string]any
	if errUnmarshal := json.Unmarshal(raw, &body); errUnmarshal != nil {
		t.Fatal(errUnmarshal)
	}
	if body["model"] != DefaultStartModel || body["store"] != false || body["stream"] != false {
		t.Fatalf("body=%s", raw)
	}
	if DefaultStartModel != "gpt-6-luna" {
		t.Fatalf("DefaultStartModel=%q", DefaultStartModel)
	}
	if _, ok := body["max_output_tokens"]; !ok {
		t.Fatal("max_output_tokens missing")
	}
	if got := startBodyPrompt(t, body); got != prompt {
		t.Fatalf("encoded prompt=%q, want %q in %s", got, prompt, raw)
	}
}

func TestStartBodyEmptyPromptPicksFromPool(t *testing.T) {
	raw, err := StartBody("", "")
	if err != nil {
		t.Fatal(err)
	}
	var body map[string]any
	if errUnmarshal := json.Unmarshal(raw, &body); errUnmarshal != nil {
		t.Fatal(errUnmarshal)
	}
	if !knownStartPrompt(startBodyPrompt(t, body)) {
		t.Fatalf("empty prompt encoded %q, want one of %v", startBodyPrompt(t, body), StartPrompts)
	}
}

func knownStartPrompt(prompt string) bool {
	for _, candidate := range StartPrompts {
		if candidate == prompt {
			return true
		}
	}
	return false
}

func startBodyPrompt(t *testing.T, body map[string]any) string {
	t.Helper()
	input, _ := body["input"].([]any)
	if len(input) == 0 {
		t.Fatalf("input missing: %+v", body)
	}
	message, _ := input[0].(map[string]any)
	content, _ := message["content"].([]any)
	if len(content) == 0 {
		t.Fatalf("content missing: %+v", body)
	}
	part, _ := content[0].(map[string]any)
	text, _ := part["text"].(string)
	return text
}

func TestStartHeadersLookLikeCodexCLI(t *testing.T) {
	headers := StartHeaders(StartRequest{AccessToken: "tok", AccountID: "acct"})
	if got := headers["User-Agent"][0]; !strings.Contains(got, "codex_cli_rs/") {
		t.Fatalf("User-Agent=%q", got)
	}
	if headers["Originator"][0] != "codex_cli_rs" {
		t.Fatalf("Originator=%v", headers["Originator"])
	}
	if headers["Chatgpt-Account-Id"][0] != "acct" {
		t.Fatalf("account=%v", headers["Chatgpt-Account-Id"])
	}
	if !strings.HasPrefix(headers["Authorization"][0], "Bearer ") {
		t.Fatalf("Authorization=%v", headers["Authorization"])
	}
}

func TestEvaluateStartClassifiesStatus(t *testing.T) {
	ok, retry, _ := EvaluateStart(http.StatusOK, []byte(`{"id":"resp"}`))
	if !ok || retry {
		t.Fatal("200 should succeed")
	}
	ok, retry, msg := EvaluateStart(http.StatusTooManyRequests, []byte(`rate`))
	if ok || !retry || msg == "" {
		t.Fatalf("429 = ok=%v retry=%v msg=%q", ok, retry, msg)
	}
	ok, retry, _ = EvaluateStart(http.StatusUnauthorized, []byte(`nope`))
	if ok || retry {
		t.Fatal("401 should be terminal")
	}
}
