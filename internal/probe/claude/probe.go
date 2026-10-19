package claude

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/karlorz/cpa-expiring-first/internal/probe/host"
	"github.com/karlorz/cpa-expiring-first/internal/probe/reading"
)

type httpDoer interface {
	HTTPDo(ctx context.Context, req host.HTTPRequest) (host.HTTPResponse, error)
}

type httpRawDoer interface {
	HTTPDoRaw(ctx context.Context, req host.HTTPRequest) (host.HTTPResponse, error)
}

// Prober runs Claude usage probes through the host HTTP transport.
type Prober struct {
	host  httpDoer
	clock clock
}

// NewProber returns a prober over hostAPI. A nil clock uses the wall clock.
func NewProber(hostAPI httpDoer, clockSource clock) Prober {
	if clockSource == nil {
		clockSource = realClock{}
	}
	return Prober{host: hostAPI, clock: clockSource}
}

// Probe requests Claude's quota endpoints and returns a result holding only safe fields. The
// API path first sends a one-token message, whose unified rate-limit headers carry the exact
// utilization; the web path lists organizations to find the usage URL.
func (p Prober) Probe(ctx context.Context, request ProbeRequest) ProbeResult {
	observedAt := p.clock.Now().UTC()
	baseURL := strings.TrimRight(strings.TrimSpace(request.BaseURL), "/")
	if baseURL == "" {
		baseURL = DefaultAPIBaseURL
	}
	headers := probeHeaders(request)
	orgUUID := strings.TrimSpace(request.OrganizationUUID)

	if orgUUID == "" && strings.Contains(baseURL, "claude.ai") {
		if discoveredOrg, orgBody, respStatus, errDiscover := p.discoverOrganization(ctx, request, baseURL, headers); errDiscover == nil && respStatus == http.StatusOK {
			if len(orgBody) > 0 {
				directResult := ParseClaudeUsage(orgBody, nil, observedAt)
				if directResult.Status == reading.StatusReady {
					directResult.AuthID = request.AuthID
					directResult.AuthIndex = request.AuthIndex
					if directResult.OrganizationUUID == "" && discoveredOrg != "" {
						directResult.OrganizationUUID = discoveredOrg
					}
					return directResult
				}
			}
			if discoveredOrg != "" {
				orgUUID = discoveredOrg
			}
		}
	}

	if !strings.Contains(baseURL, "claude.ai") {
		microReqBody := []byte(`{"model":"claude-haiku-4-5-20251001","max_tokens":1,"messages":[{"role":"user","content":"hi"}]}`)
		resp, errDo := p.doRequest(ctx, host.HTTPRequest{
			AuthIndex: request.AuthIndex,
			Method:    http.MethodPost,
			URL:       baseURL + "/v1/messages",
			Headers:   headers,
			Body:      microReqBody,
		})
		if errDo == nil {
			switch {
			case resp.StatusCode == http.StatusOK:
				result := ParseClaudeUsage(resp.Body, resp.Headers, observedAt)
				if result.Status == reading.StatusReady {
					return finishResult(result, request, orgUUID)
				}
			case resp.StatusCode == http.StatusTooManyRequests:
				return finishResult(ParseClaudeRateLimitError(resp.Body, resp.Headers, observedAt), request, orgUUID)
			case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
				return failedProbe(request, observedAt, fmt.Sprintf("claude probe status %d", resp.StatusCode))
			}
		}
	}

	urls := probeCandidateURLs(baseURL, orgUUID)
	var lastErr string
	var lastStatus int
	var lastBody []byte
	var lastHeaders host.Header

	for _, url := range urls {
		resp, errDo := p.doRequest(ctx, host.HTTPRequest{
			AuthIndex: request.AuthIndex,
			Method:    http.MethodGet,
			URL:       url,
			Headers:   headers,
		})
		if errDo != nil && resp.StatusCode == 0 {
			lastErr = "host http do failed"
			continue
		}
		lastStatus = resp.StatusCode
		lastBody = resp.Body
		lastHeaders = resp.Headers

		if resp.StatusCode == http.StatusOK && len(resp.Body) > 0 {
			result := ParseClaudeUsage(resp.Body, resp.Headers, observedAt)
			if result.Status == reading.StatusReady {
				return finishResult(result, request, orgUUID)
			}
			lastErr = "parse claude usage not ready"
			continue
		}
		if resp.StatusCode == http.StatusTooManyRequests {
			return finishResult(ParseClaudeRateLimitError(resp.Body, resp.Headers, observedAt), request, orgUUID)
		}
		if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
			return failedProbe(request, observedAt, fmt.Sprintf("claude probe status %d", resp.StatusCode))
		}
		lastErr = fmt.Sprintf("claude probe status %d", resp.StatusCode)
	}

	if lastStatus == http.StatusTooManyRequests {
		return finishResult(ParseClaudeRateLimitError(lastBody, lastHeaders, observedAt), request, orgUUID)
	}
	return failedProbe(request, observedAt, safeError(lastErr))
}

// doRequest keeps the non-2xx response body when the host transport supports it, so a 429 can
// still be parsed for its reset hint.
func (p Prober) doRequest(ctx context.Context, req host.HTTPRequest) (host.HTTPResponse, error) {
	if rawDoer, okRaw := p.host.(httpRawDoer); okRaw {
		return rawDoer.HTTPDoRaw(ctx, req)
	}
	return p.host.HTTPDo(ctx, req)
}

func (p Prober) discoverOrganization(ctx context.Context, request ProbeRequest, baseURL string, headers host.Header) (string, []byte, int, error) {
	orgURL := baseURL + "/organizations"
	resp, errDo := p.doRequest(ctx, host.HTTPRequest{
		AuthIndex: request.AuthIndex,
		Method:    http.MethodGet,
		URL:       orgURL,
		Headers:   headers,
	})
	if errDo != nil && resp.StatusCode == 0 {
		return "", nil, 0, errDo
	}
	if resp.StatusCode != http.StatusOK {
		return "", resp.Body, resp.StatusCode, nil
	}
	var orgList []struct {
		UUID string `json:"uuid"`
		ID   string `json:"id"`
	}
	trimmed := bytes.TrimSpace(resp.Body)
	if len(trimmed) > 0 && trimmed[0] == '[' {
		if errUnmarshal := json.Unmarshal(trimmed, &orgList); errUnmarshal == nil && len(orgList) > 0 {
			uuid := orgList[0].UUID
			if uuid == "" {
				uuid = orgList[0].ID
			}
			return uuid, resp.Body, resp.StatusCode, nil
		}
	}
	var singleOrg struct {
		UUID string `json:"uuid"`
		ID   string `json:"id"`
	}
	if errUnmarshal := json.Unmarshal(trimmed, &singleOrg); errUnmarshal == nil {
		uuid := singleOrg.UUID
		if uuid == "" {
			uuid = singleOrg.ID
		}
		return uuid, resp.Body, resp.StatusCode, nil
	}
	return "", resp.Body, resp.StatusCode, nil
}

func probeCandidateURLs(baseURL string, orgUUID string) []string {
	base := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	urls := make([]string, 0, 4)
	if strings.Contains(base, "api.anthropic.com") || !strings.Contains(base, "claude.ai") {
		urls = append(urls, base+"/v1/models")
		if orgUUID != "" {
			urls = append(urls, fmt.Sprintf("%s/organizations/%s/usage", base, orgUUID))
		}
		urls = append(urls, base+"/v1/messages")
		return urls
	}
	if orgUUID != "" {
		return append(urls,
			fmt.Sprintf("%s/organizations/%s/usage", base, orgUUID),
			fmt.Sprintf("%s/organizations/%s/rate_limits", base, orgUUID),
			fmt.Sprintf("%s/organizations/%s/stats", base, orgUUID),
			fmt.Sprintf("%s/organizations/%s", base, orgUUID),
		)
	}
	return append(urls,
		base+"/organizations",
		base+"/organizations/usage",
		base+"/usage",
	)
}

// claudeProbeBetaOAuth and claudeProbeBetaAPIKey mirror the Anthropic-Beta value CLIProxyAPI's
// own Claude executor computes for a one-token probe request. They are literals because this
// plugin cannot import CLIProxyAPI's internal executor packages; re-verify when the executor's
// beta list changes.
const (
	claudeProbeBetaOAuth  = "claude-code-20250219,oauth-2025-04-20,interleaved-thinking-2025-05-14,redact-thinking-2026-02-12,thinking-token-count-2026-05-13,context-management-2025-06-27,prompt-caching-scope-2026-01-05,mid-conversation-system-2026-04-07,fallback-credit-2026-06-01"
	claudeProbeBetaAPIKey = "claude-code-20250219,interleaved-thinking-2025-05-14,redact-thinking-2026-02-12,thinking-token-count-2026-05-13,context-management-2025-06-27,prompt-caching-scope-2026-01-05,mid-conversation-system-2026-04-07"
)

func probeHeaders(request ProbeRequest) host.Header {
	token := "$TOKEN$"
	if accessToken := strings.TrimSpace(request.AccessToken); accessToken != "" {
		token = accessToken
	}
	anthropicBeta := claudeProbeBetaAPIKey
	if strings.Contains(token, "sk-ant-oat") {
		anthropicBeta = claudeProbeBetaOAuth
	}
	headers := host.Header{
		"Accept":            []string{"application/json"},
		"Content-Type":      []string{"application/json"},
		"User-Agent":        []string{"claude-cli/2.1.258 (external, cli)"},
		"Anthropic-Version": []string{"2023-06-01"},
		"Authorization":     []string{"Bearer " + token},
		"Anthropic-Beta":    []string{anthropicBeta},
		// These identity headers match real Claude Code traffic, so the probe reads as a
		// genuine CLI helper request rather than an unrecognized client.
		"Anthropic-Dangerous-Direct-Browser-Access": []string{"true"},
		"X-App":                   []string{"cli"},
		"X-Stainless-Retry-Count": []string{"0"},
		"X-Stainless-Runtime":     []string{"node"},
		"X-Stainless-Lang":        []string{"js"},
		"X-Stainless-Timeout":     []string{"600"},
	}
	if strings.HasPrefix(token, "sk-ant-api") {
		headers["X-Api-Key"] = []string{token}
	}
	if strings.HasPrefix(token, "sk-ant-sid") || strings.HasPrefix(token, "sessionKey=") {
		cookieVal := token
		if !strings.HasPrefix(cookieVal, "sessionKey=") {
			cookieVal = "sessionKey=" + token
		}
		headers["Cookie"] = []string{cookieVal}
	}
	return headers
}

func finishResult(result ProbeResult, request ProbeRequest, orgUUID string) ProbeResult {
	result.AuthID = request.AuthID
	result.AuthIndex = request.AuthIndex
	if result.OrganizationUUID == "" && orgUUID != "" {
		result.OrganizationUUID = orgUUID
	}
	return result
}

func failedProbe(request ProbeRequest, observedAt time.Time, message string) ProbeResult {
	result := failedResult(observedAt, safeError(message))
	result.AuthID = request.AuthID
	result.AuthIndex = request.AuthIndex
	return result
}

func safeError(message string) string {
	trimmed := strings.TrimSpace(message)
	if trimmed == "" {
		return "probe failed"
	}
	return trimmed
}
