package codex

import (
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"net/http"
	"strings"

	"github.com/karlorz/cpa-expiring-first/internal/probe/host"
)

// ResponsesURL is ChatGPT's Codex responses endpoint, called through the host transport to
// start an unused weekly usage window.
const ResponsesURL = "https://chatgpt.com/backend-api/codex/responses"

// DefaultStartModel is the model used when the plugin config does not name one.
const DefaultStartModel = "gpt-6-luna"

// StartPrompts are short human-like messages used for unused-window safety requests.
var StartPrompts = []string{
	"hi",
	"hello",
	"ok",
	"thanks",
	"what is 1+1",
}

const startUserAgent = "codex_cli_rs/0.76.0 (Debian 13.0.0; x86_64) WindowsTerminal"

// StartRequest is one unused-window safety request.
type StartRequest struct {
	AuthIndex   string
	AccountID   string
	AccessToken string
	Model       string
	Prompt      string
}

// StartHeaders returns Codex CLI-shaped headers for a safety request.
func StartHeaders(request StartRequest) host.Header {
	token := "$TOKEN$"
	if accessToken := strings.TrimSpace(request.AccessToken); accessToken != "" {
		token = accessToken
	}
	headers := host.Header{
		"Accept":        []string{"application/json"},
		"Authorization": []string{"Bearer " + token},
		"Content-Type":  []string{"application/json"},
		"OpenAI-Beta":   []string{"responses=v1"},
		"Originator":    []string{"codex_cli_rs"},
		"User-Agent":    []string{startUserAgent},
	}
	if accountID := strings.TrimSpace(request.AccountID); accountID != "" {
		headers["Chatgpt-Account-Id"] = []string{accountID}
	}
	return headers
}

// PickStartPrompt returns one entry from StartPrompts. intn must behave like rand.IntN.
func PickStartPrompt(intn func(int) int) string {
	if len(StartPrompts) == 0 {
		return "hi"
	}
	if intn == nil {
		intn = rand.IntN
	}
	index := intn(len(StartPrompts))
	if index < 0 || index >= len(StartPrompts) {
		index = 0
	}
	return StartPrompts[index]
}

// StartBody encodes a tiny non-streaming Codex responses payload.
func StartBody(model, prompt string) ([]byte, error) {
	model = strings.TrimSpace(model)
	if model == "" {
		model = DefaultStartModel
	}
	prompt = strings.TrimSpace(prompt)
	if prompt == "" {
		prompt = PickStartPrompt(rand.IntN)
	}
	return json.Marshal(map[string]any{
		"model":        model,
		"instructions": "Reply with one short word.",
		"input": []map[string]any{{
			"type":    "message",
			"role":    "user",
			"content": []map[string]string{{"type": "input_text", "text": prompt}},
		}},
		"reasoning":         map[string]string{"effort": "none", "summary": "auto"},
		"store":             false,
		"stream":            false,
		"max_output_tokens": 16,
	})
}

// StartHTTPRequest builds the host HTTP call for one safety request.
func StartHTTPRequest(request StartRequest) (host.HTTPRequest, error) {
	body, errBody := StartBody(request.Model, request.Prompt)
	if errBody != nil {
		return host.HTTPRequest{}, fmt.Errorf("encode Codex start request: %w", errBody)
	}
	return host.HTTPRequest{
		AuthIndex: request.AuthIndex,
		Method:    http.MethodPost,
		URL:       ResponsesURL,
		Headers:   StartHeaders(request),
		Body:      body,
	}, nil
}

// EvaluateStart reports whether a Codex responses call started the window.
func EvaluateStart(status int, body []byte) (ok bool, retryable bool, message string) {
	if status >= 200 && status < 300 {
		return true, false, ""
	}
	text := host.RedactBytes(body)
	if len(text) > 160 {
		text = text[:160]
	}
	if status == http.StatusTooManyRequests || status >= 500 || status == 0 {
		return false, true, fmt.Sprintf("codex start status %d: %s", status, text)
	}
	return false, false, fmt.Sprintf("codex start status %d: %s", status, text)
}
