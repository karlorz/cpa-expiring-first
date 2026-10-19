// Package host adapts CLIProxyAPI host callbacks to the small surface the quota probes need.
// It is a copy of the equivalent layer in quota-pacer, trimmed to the read-only calls a
// scheduler may make: list credentials, read one credential's physical JSON, and issue an
// upstream HTTP request through the host transport.
package host

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"regexp"
	"strings"
	"time"
)

// RedactedValue replaces sensitive header values and JSON fields in recorded or logged text.
const RedactedValue = "[REDACTED]"

// ErrInvalidRequest reports a request rejected before it reached the host.
var ErrInvalidRequest = errors.New("host: invalid request")

// Header is the header map used by host HTTP calls.
type Header map[string][]string

// AuthFile is the subset of host.auth.list the probes read. ID is the credential ID the host
// also hands to scheduler.pick as SchedulerAuthCandidate.ID, so quota readings key on it.
type AuthFile struct {
	ID             string    `json:"id"`
	AuthIndex      string    `json:"auth_index"`
	Name           string    `json:"name,omitempty"`
	Priority       int       `json:"priority,omitempty"`
	Type           string    `json:"type,omitempty"`
	Provider       string    `json:"provider,omitempty"`
	ProjectID      string    `json:"project_id,omitempty"`
	BaseURL        string    `json:"base_url,omitempty"`
	Disabled       bool      `json:"disabled,omitempty"`
	Unavailable    bool      `json:"unavailable,omitempty"`
	NextRetryAfter time.Time `json:"next_retry_after,omitempty"`
}

// AuthDocument is the host.auth.get view of one credential's physical JSON.
type AuthDocument struct {
	AuthIndex string          `json:"auth_index"`
	Name      string          `json:"name,omitempty"`
	Path      string          `json:"path,omitempty"`
	JSON      json.RawMessage `json:"json"`
}

// HTTPRequest is one upstream request issued through host.http.do.
type HTTPRequest struct {
	AuthIndex string `json:"auth_index,omitempty"`
	Method    string `json:"Method"`
	URL       string `json:"URL"`
	Headers   Header `json:"Headers,omitempty"`
	Body      []byte `json:"Body,omitempty"`
}

// HTTPResponse is the host.http.do result.
type HTTPResponse struct {
	StatusCode int    `json:"StatusCode"`
	Headers    Header `json:"Headers,omitempty"`
	Body       []byte `json:"Body,omitempty"`
}

// UnmarshalJSON accepts the host's http.Response encoding (StatusCode/Headers/Body with a
// base64 body) and the legacy snake_case shape.
func (r *HTTPResponse) UnmarshalJSON(data []byte) error {
	var raw struct {
		StatusCode      *int    `json:"StatusCode"`
		StatusCodeSnake *int    `json:"status_code"`
		Headers         Header  `json:"Headers"`
		HeadersLower    Header  `json:"headers"`
		Body            *string `json:"Body"`
		BodyLower       *string `json:"body"`
	}
	if errUnmarshal := json.Unmarshal(data, &raw); errUnmarshal != nil {
		return errUnmarshal
	}
	if raw.StatusCode != nil {
		r.StatusCode = *raw.StatusCode
	} else if raw.StatusCodeSnake != nil {
		r.StatusCode = *raw.StatusCodeSnake
	}
	if raw.Headers != nil {
		r.Headers = raw.Headers
	} else {
		r.Headers = raw.HeadersLower
	}
	body, errBody := decodeBody(raw.Body, raw.BodyLower)
	if errBody != nil {
		return errBody
	}
	r.Body = body
	return nil
}

func decodeBody(official, legacy *string) ([]byte, error) {
	if official != nil {
		if *official == "" {
			return nil, nil
		}
		decoded, errDecode := base64.StdEncoding.DecodeString(*official)
		if errDecode != nil {
			return nil, fmt.Errorf("decode host response body: %w", errDecode)
		}
		return decoded, nil
	}
	if legacy == nil || *legacy == "" {
		return nil, nil
	}
	if looksLikeBase64(*legacy) {
		if decoded, errDecode := base64.StdEncoding.DecodeString(*legacy); errDecode == nil {
			return decoded, nil
		}
	}
	return []byte(*legacy), nil
}

func looksLikeBase64(value string) bool {
	trimmed := strings.TrimSpace(value)
	return len(trimmed)%4 == 0 && !strings.ContainsAny(trimmed, "{}<>\n\r")
}

// Callbacks is the host surface this package depends on.
type Callbacks interface {
	ListAuthFiles(ctx context.Context) ([]AuthFile, error)
	GetAuth(ctx context.Context, authIndex string) (AuthDocument, error)
	SaveAuth(ctx context.Context, name string, raw json.RawMessage) error
	HTTPDo(ctx context.Context, req HTTPRequest) (HTTPResponse, error)
}

// Client adapts host callbacks to the probe-facing API.
type Client struct {
	callbacks Callbacks
}

// NewClient returns a client over callbacks.
func NewClient(callbacks Callbacks) *Client {
	return &Client{callbacks: callbacks}
}

// ListAuthFiles returns every credential the host can describe.
func (c *Client) ListAuthFiles(ctx context.Context) ([]AuthFile, error) {
	files, errList := c.callbacks.ListAuthFiles(ctx)
	if errList != nil {
		return nil, fmt.Errorf("host.auth.list: %w", errList)
	}
	return files, nil
}

// GetAuth returns the physical JSON of one credential, reading the backing file when the host
// reports one so probe credentials match what the provider sees.
func (c *Client) GetAuth(ctx context.Context, authIndex string) (AuthDocument, error) {
	document, errGet := c.callbacks.GetAuth(ctx, authIndex)
	if errGet != nil {
		return AuthDocument{}, fmt.Errorf("host.auth.get: %w", errGet)
	}
	if strings.TrimSpace(document.Path) != "" {
		if data, errRead := os.ReadFile(document.Path); errRead == nil && json.Valid(data) {
			document.JSON = data
			return document, nil
		}
	}
	if len(document.JSON) == 0 {
		return AuthDocument{}, errors.New("host.auth.get returned no credential JSON")
	}
	return document, nil
}

// GetAuthJSON returns one credential's physical JSON.
func (c *Client) GetAuthJSON(ctx context.Context, authIndex string) (json.RawMessage, error) {
	document, errGet := c.GetAuth(ctx, authIndex)
	if errGet != nil {
		return nil, errGet
	}
	return document.JSON, nil
}

// SaveAuth persists one credential JSON through host.auth.save.
func (c *Client) SaveAuth(ctx context.Context, name string, raw json.RawMessage) error {
	name = strings.TrimSpace(name)
	if name == "" || len(raw) == 0 {
		return fmt.Errorf("%w: name and json are required", ErrInvalidRequest)
	}
	if errSave := c.callbacks.SaveAuth(ctx, name, raw); errSave != nil {
		return fmt.Errorf("host.auth.save: %w", errSave)
	}
	return nil
}

// PatchPriority writes a top-level integer priority through host.auth.save. Invalid (non-integer)
// values never reach this path. Larger values are higher fill-first priority.
func (c *Client) PatchPriority(ctx context.Context, file AuthFile, priority int) error {
	document, errGet := c.GetAuth(ctx, file.AuthIndex)
	if errGet != nil {
		return errGet
	}
	patched, errPatch := SetPriorityJSON(document.JSON, priority)
	if errPatch != nil {
		return errPatch
	}
	name := strings.TrimSpace(document.Name)
	if name == "" {
		name = strings.TrimSpace(file.Name)
	}
	if name == "" {
		name = strings.TrimSpace(file.ID)
	}
	return c.SaveAuth(ctx, name, patched)
}

// SetPriorityJSON replaces the top-level priority field with an integer.
func SetPriorityJSON(raw []byte, priority int) (json.RawMessage, error) {
	var object map[string]json.RawMessage
	if errUnmarshal := json.Unmarshal(raw, &object); errUnmarshal != nil {
		return nil, fmt.Errorf("decode credential json: %w", errUnmarshal)
	}
	encoded, errMarshal := json.Marshal(priority)
	if errMarshal != nil {
		return nil, errMarshal
	}
	object["priority"] = encoded
	patched, errMarshal := json.Marshal(object)
	if errMarshal != nil {
		return nil, errMarshal
	}
	return patched, nil
}

// HTTPDo issues an upstream request and treats every non-2xx status as an error, with the body
// redacted. Probes that must inspect the status use HTTPDoRaw instead.
func (c *Client) HTTPDo(ctx context.Context, req HTTPRequest) (HTTPResponse, error) {
	resp, errDo := c.HTTPDoRaw(ctx, req)
	if errDo != nil {
		return HTTPResponse{}, errDo
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return resp, fmt.Errorf("host.http.do %s %s: status %d: %s", req.Method, RedactURL(req.URL), resp.StatusCode, RedactBytes(resp.Body))
	}
	return resp, nil
}

// HTTPDoRaw issues an upstream request and returns the response whatever its status.
func (c *Client) HTTPDoRaw(ctx context.Context, req HTTPRequest) (HTTPResponse, error) {
	method := strings.TrimSpace(req.Method)
	if method == "" || strings.TrimSpace(req.URL) == "" {
		return HTTPResponse{}, fmt.Errorf("%w: method and url are required", ErrInvalidRequest)
	}
	req.Method = method
	resp, errDo := c.callbacks.HTTPDo(ctx, req)
	if errDo != nil {
		return HTTPResponse{}, fmt.Errorf("host.http.do %s %s: %w", method, RedactURL(req.URL), errDo)
	}
	return resp, nil
}

var (
	sensitiveTextPattern = regexp.MustCompile(`(?i)(authorization|api[_-]?key|token|cookie|set-cookie)(\s*[:=]\s*)([^\s,;\"'}]+)`)
	bearerTextPattern    = regexp.MustCompile(`(?i)(bearer\s+)([^\s,;\"'}]+)`)
)

// RedactBytes returns text with credential-shaped values replaced, so probe errors can be
// logged and shown in the console.
func RedactBytes(raw []byte) string {
	return RedactText(string(raw))
}

// RedactText returns value with bearer tokens and credential fields replaced.
func RedactText(value string) string {
	redacted := bearerTextPattern.ReplaceAllString(value, "${1}"+RedactedValue)
	return sensitiveTextPattern.ReplaceAllString(redacted, "${1}${2}"+RedactedValue)
}

// RedactURL returns rawURL with sensitive query parameters replaced.
func RedactURL(rawURL string) string {
	parsed, errParse := url.Parse(rawURL)
	if errParse != nil || parsed.RawQuery == "" {
		return rawURL
	}
	query := parsed.Query()
	for key := range query {
		if sensitiveKey(key) {
			query.Set(key, RedactedValue)
		}
	}
	parsed.RawQuery = query.Encode()
	return parsed.String()
}

func sensitiveKey(key string) bool {
	normalized := strings.ToLower(strings.ReplaceAll(strings.TrimSpace(key), "-", "_"))
	return normalized == "authorization" ||
		normalized == "cookie" ||
		normalized == "set_cookie" ||
		strings.Contains(normalized, "token") ||
		strings.Contains(normalized, "api_key") ||
		strings.Contains(normalized, "apikey")
}
