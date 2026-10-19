package xai

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/karlorz/cpa-expiring-first/internal/probe/host"
	"github.com/karlorz/cpa-expiring-first/internal/probe/reading"
)

// httpRawDoer must return non-2xx response bodies, because an xAI settings or billing request
// that fails authorization answers 401.
type httpRawDoer interface {
	HTTPDoRaw(ctx context.Context, req host.HTTPRequest) (host.HTTPResponse, error)
}

// Prober runs xAI plan and billing probes through the host HTTP transport.
type Prober struct {
	host  httpRawDoer
	clock clock
}

// NewProber returns a prober over hostAPI. A nil clock uses the wall clock.
func NewProber(hostAPI httpRawDoer, clockSource clock) Prober {
	if clockSource == nil {
		clockSource = realClock{}
	}
	return Prober{host: hostAPI, clock: clockSource}
}

// Probe classifies the plan from settings, billing and the access-token tier, then reads the
// weekly billing window for OAuth credentials. It never issues a chat completion.
func (p Prober) Probe(ctx context.Context, request ProbeRequest) ProbeResult {
	observedAt := p.clock.Now().UTC()
	baseURL := strings.TrimRight(strings.TrimSpace(request.BaseURL), "/")
	if baseURL == "" {
		baseURL = DefaultAPIBaseURL
	}
	headers := probeHeaders(request)

	var settingsBody []byte
	var billingBody []byte
	var lastErr string
	var lastStatus int
	var authFailed bool
	var authErr string

	for _, url := range planCandidateURLs(baseURL, "settings") {
		resp, errDo := p.host.HTTPDoRaw(ctx, host.HTTPRequest{AuthIndex: request.AuthIndex, Method: http.MethodGet, URL: url, Headers: headers})
		if errDo != nil {
			lastErr = "settings request failed"
			continue
		}
		lastStatus = resp.StatusCode
		bodyText := string(resp.Body)
		if IsUnauthorizedProbe(resp.StatusCode, bodyText) {
			authFailed = true
			authErr = fmt.Sprintf("settings status %d", resp.StatusCode)
			if looksLikeAuthFailureText(bodyText) {
				authErr = safeError(bodyText)
			}
			// Trying the other settings URLs after a 401 would fail the same way.
			break
		}
		if resp.StatusCode >= 200 && resp.StatusCode < 300 && len(resp.Body) > 0 {
			settingsBody = resp.Body
			break
		}
		lastErr = fmt.Sprintf("settings status %d", resp.StatusCode)
	}

	if !authFailed {
		for _, url := range planCandidateURLs(baseURL, "billing?format=credits") {
			resp, errDo := p.host.HTTPDoRaw(ctx, host.HTTPRequest{AuthIndex: request.AuthIndex, Method: http.MethodGet, URL: url, Headers: headers})
			if errDo != nil {
				if lastErr == "" {
					lastErr = "billing request failed"
				}
				continue
			}
			lastStatus = resp.StatusCode
			bodyText := string(resp.Body)
			if IsUnauthorizedProbe(resp.StatusCode, bodyText) {
				authFailed = true
				authErr = fmt.Sprintf("billing status %d", resp.StatusCode)
				if looksLikeAuthFailureText(bodyText) {
					authErr = safeError(bodyText)
				}
				break
			}
			if resp.StatusCode >= 200 && resp.StatusCode < 300 && len(resp.Body) > 0 {
				billingBody = resp.Body
				break
			}
			if lastErr == "" {
				lastErr = fmt.Sprintf("billing status %d", resp.StatusCode)
			}
		}
	}

	result := ProbeResult{
		AuthID:     request.AuthID,
		AuthIndex:  request.AuthIndex,
		ObservedAt: observedAt,
		HTTPStatus: lastStatus,
	}
	if authFailed {
		result.AuthFailed = true
		result.PlanClass = PlanClassUnknown
		result.PlanType = reading.PlanTypeUnknown
		result.Source = "auth_failed"
		result.Status = reading.StatusProbeFailed
		if authErr != "" {
			result.Error = safeError(authErr)
		} else {
			result.Error = "unauthorized"
		}
		if result.HTTPStatus == 0 {
			result.HTTPStatus = http.StatusUnauthorized
		}
		return result
	}

	class, source := ClassifyPlan(settingsBody, billingBody, jwtTierClaim(request.AccessToken))
	result.PlanClass = class
	result.PlanType = planTypeFromClass(class)
	result.Source = source
	if lastErr != "" {
		result.Error = safeError(lastErr)
	}
	result.Status = reading.StatusReady

	if isOAuthAuthKind(request.AuthKind) {
		if longAt, okReset := p.fetchOAuthWeeklyLongWindow(ctx, request); okReset && longAt != nil && !longAt.IsZero() {
			result.LongResetAt = longAt
			result.Windows = []reading.QuotaWindow{{Duration: 7 * 24 * time.Hour, Remaining: 100, ResetAt: *longAt}}
		}
	}
	return result
}

// fetchOAuthWeeklyLongWindow reads the official weekly billing window. It reports okReset only
// when a 2xx body was received, so a stale window is not kept alive by a network failure. The
// Authorization header and billing body are never logged.
func (p Prober) fetchOAuthWeeklyLongWindow(ctx context.Context, request ProbeRequest) (*time.Time, bool) {
	headers := probeHeaders(request)
	if userID := strings.TrimSpace(request.UserID); userID != "" {
		headers["x-userid"] = []string{userID}
	}
	resp, errDo := p.host.HTTPDoRaw(ctx, host.HTTPRequest{
		AuthIndex: request.AuthIndex,
		Method:    http.MethodGet,
		URL:       CLIChatProxyBillingURL,
		Headers:   headers,
	})
	if errDo != nil {
		return nil, false
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 || len(resp.Body) == 0 {
		return nil, false
	}
	longAt, _ := ParseWeeklyLongWindowReset(resp.Body)
	return longAt, true
}

func planCandidateURLs(baseURL, suffix string) []string {
	base := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	suffix = strings.TrimLeft(suffix, "/")
	out := make([]string, 0, 4)
	seen := map[string]struct{}{}
	add := func(url string) {
		url = strings.TrimSpace(url)
		if url == "" {
			return
		}
		if _, okSeen := seen[url]; okSeen {
			return
		}
		seen[url] = struct{}{}
		out = append(out, url)
	}
	add(base + "/" + suffix)
	if strings.HasSuffix(base, "/v1") {
		add(strings.TrimSuffix(base, "/v1") + "/" + suffix)
	}
	if strings.Contains(base, "api.x.ai") {
		root := strings.Replace(base, "api.x.ai", "management-api.x.ai", 1)
		add(strings.TrimSuffix(root, "/v1") + "/" + suffix)
	}
	return out
}

func probeHeaders(request ProbeRequest) host.Header {
	token := "$TOKEN$"
	if accessToken := strings.TrimSpace(request.AccessToken); accessToken != "" {
		token = accessToken
	}
	return host.Header{
		"Accept":                []string{"application/json"},
		"Authorization":         []string{"Bearer " + token},
		"User-Agent":            []string{"cpa-expiring-first/xai-plan"},
		"X-XAI-Token-Auth":      []string{"xai-grok-cli"},
		"x-grok-client-version": []string{"0.2.93"},
	}
}

// jwtTierClaim reads the tier claim from an access token payload. The signature is not
// verified: the value only classifies the plan.
func jwtTierClaim(accessToken string) string {
	parts := strings.Split(strings.TrimSpace(accessToken), ".")
	if len(parts) < 2 {
		return ""
	}
	payload, errDecode := base64.RawURLEncoding.DecodeString(parts[1])
	if errDecode != nil {
		payload, errDecode = base64.StdEncoding.DecodeString(parts[1])
		if errDecode != nil {
			return ""
		}
	}
	var claims map[string]any
	if errUnmarshal := json.Unmarshal(payload, &claims); errUnmarshal != nil {
		return ""
	}
	for _, key := range []string{"tier", "plan", "plan_type", "subscription_tier", "xai_tier"} {
		if text := anyToTierString(claims[key]); text != "" {
			return text
		}
	}
	return ""
}

// IsUnauthorizedProbe reports whether an xAI probe failed authorization, covering HTTP 401 and
// the host's credential-failure wording. Plain network failures, 404 and 5xx do not count.
func IsUnauthorizedProbe(httpStatus int, errMsg string) bool {
	if httpStatus == http.StatusUnauthorized {
		return true
	}
	return looksLikeAuthFailureText(errMsg)
}

func looksLikeAuthFailureText(msg string) bool {
	lower := strings.ToLower(strings.TrimSpace(msg))
	if lower == "" {
		return false
	}
	markers := []string{
		"status 401",
		"http 401",
		"unauthorized",
		"unauthenticated",
		"invalid or expired credentials",
		"expired credentials",
		"no auth context",
		"permissiondenied",
		"permission denied",
		"auth context",
	}
	for _, marker := range markers {
		if !strings.Contains(lower, marker) {
			continue
		}
		// "auth context" only means a failure when something is actually missing.
		if marker == "auth context" && !strings.Contains(lower, "no auth") && !strings.Contains(lower, "missing auth") {
			continue
		}
		return true
	}
	return false
}

// AccessTokenExpired reports whether an access token's exp claim has passed. A token whose exp
// cannot be read is not assumed expired.
func AccessTokenExpired(accessToken string, now time.Time) bool {
	exp, okExp := jwtExpClaim(accessToken)
	if !okExp {
		return false
	}
	return !exp.After(now.UTC())
}

func jwtExpClaim(accessToken string) (time.Time, bool) {
	parts := strings.Split(strings.TrimSpace(accessToken), ".")
	if len(parts) < 2 {
		return time.Time{}, false
	}
	payload, errDecode := base64.RawURLEncoding.DecodeString(parts[1])
	if errDecode != nil {
		payload, errDecode = base64.StdEncoding.DecodeString(parts[1])
		if errDecode != nil {
			return time.Time{}, false
		}
	}
	var claims map[string]any
	if errUnmarshal := json.Unmarshal(payload, &claims); errUnmarshal != nil {
		return time.Time{}, false
	}
	switch value := claims["exp"].(type) {
	case float64:
		if value <= 0 {
			return time.Time{}, false
		}
		return time.Unix(int64(value), 0).UTC(), true
	case json.Number:
		seconds, errParse := value.Int64()
		if errParse != nil || seconds <= 0 {
			return time.Time{}, false
		}
		return time.Unix(seconds, 0).UTC(), true
	case string:
		seconds, errParse := strconv.ParseInt(strings.TrimSpace(value), 10, 64)
		if errParse != nil || seconds <= 0 {
			return time.Time{}, false
		}
		return time.Unix(seconds, 0).UTC(), true
	default:
		return time.Time{}, false
	}
}
