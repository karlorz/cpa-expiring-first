package claude

import (
	"bytes"
	"encoding/json"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/karlorz/cpa-expiring-first/internal/probe/host"
	"github.com/karlorz/cpa-expiring-first/internal/probe/reading"
	"github.com/karlorz/cpa-expiring-first/internal/probe/wire"
)

type claudeUsageResponse struct {
	PlanType         string            `json:"plan_type"`
	Tier             string            `json:"tier"`
	Capabilities     []string          `json:"capabilities"`
	RateLimits       *claudeRateLimits `json:"rate_limits"`
	SessionLimit     *claudeWindow     `json:"session_limit"`
	FiveHour         *claudeWindow     `json:"five_hour"`
	Weekly           *claudeWindow     `json:"weekly"`
	Daily            *claudeWindow     `json:"daily"`
	UUID             string            `json:"uuid"`
	OrganizationUUID string            `json:"organization_uuid"`
	Data             []any             `json:"data"`
	claudeWindow
}

type claudeRateLimits struct {
	SessionLimit *claudeWindow `json:"session_limit"`
	FiveHour     *claudeWindow `json:"five_hour"`
	Weekly       *claudeWindow `json:"weekly"`
	Daily        *claudeWindow `json:"daily"`
	Primary      *claudeWindow `json:"primary"`
	Secondary    *claudeWindow `json:"secondary"`
}

type claudeWindow struct {
	ResetsAt          any `json:"resets_at"`
	ResetTime         any `json:"reset_time"`
	ResetAfterSeconds any `json:"reset_after_seconds"`
	Remaining         any `json:"remaining"`
	RemainingQueries  any `json:"remaining_queries"`
	Limit             any `json:"limit"`
	Used              any `json:"used"`
	UsedPercent       any `json:"used_percent"`
	LimitReached      any `json:"limit_reached"`
}

type effectiveWindow struct {
	resetAt              *time.Time
	remaining            int64
	windowType           WindowType
	longWindowResetAt    *time.Time
	shortWindowRemaining *int64
	shortWindowResetAt   *time.Time
	longWindowRemaining  *int64
}

// ParseClaudeUsage decodes a Claude usage response or its unified rate-limit headers into a
// reading. Headers win: they carry exact utilization for both windows.
func ParseClaudeUsage(raw []byte, headers host.Header, observedAt time.Time) ProbeResult {
	orgUUIDFromHeader := extractOrgFromHeaders(headers)

	if res, okHeaders := parseUnifiedRateLimitHeaders(headers, observedAt); okHeaders {
		if res.OrganizationUUID == "" && orgUUIDFromHeader != "" {
			res.OrganizationUUID = orgUUIDFromHeader
		}
		return res
	}

	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return failedResult(observedAt, "empty claude usage response")
	}

	if trimmed[0] == '[' {
		var orgList []claudeUsageResponse
		if errUnmarshal := json.Unmarshal(trimmed, &orgList); errUnmarshal == nil && len(orgList) > 0 {
			for _, org := range orgList {
				if res, okSingle := parseSingleUsage(org, observedAt); okSingle {
					switch {
					case org.UUID != "":
						res.OrganizationUUID = org.UUID
					case org.OrganizationUUID != "":
						res.OrganizationUUID = org.OrganizationUUID
					case orgUUIDFromHeader != "":
						res.OrganizationUUID = orgUUIDFromHeader
					}
					return res
				}
			}
			return failedResult(observedAt, "no quota info in organization list")
		}
	}

	var usage claudeUsageResponse
	decoder := json.NewDecoder(bytes.NewReader(trimmed))
	decoder.UseNumber()
	if errDecode := decoder.Decode(&usage); errDecode != nil {
		return failedResult(observedAt, "parse claude usage failed")
	}

	if res, okSingle := parseSingleUsage(usage, observedAt); okSingle {
		if res.OrganizationUUID == "" && orgUUIDFromHeader != "" {
			res.OrganizationUUID = orgUUIDFromHeader
		}
		return res
	}

	// A /v1/models response proves the credential is valid but carries no quota; treat a full
	// weekly window as the reading so the credential stays rankable.
	if len(usage.Data) > 0 {
		defaultReset := observedAt.UTC().Add(7 * 24 * time.Hour)
		orgUUID := usage.UUID
		if orgUUID == "" {
			orgUUID = usage.OrganizationUUID
		}
		if orgUUID == "" {
			orgUUID = orgUUIDFromHeader
		}
		return makeReadyResult(observedAt, &defaultReset, 100, WindowWeekly, &defaultReset, reading.PlanTypePro, orgUUID, nil, nil, nil)
	}

	return failedResult(observedAt, "trusted claude quota window unavailable")
}

func parseSingleUsage(usage claudeUsageResponse, observedAt time.Time) (ProbeResult, bool) {
	planType := inferClaudePlanType(usage)
	orgUUID := usage.UUID
	if orgUUID == "" {
		orgUUID = usage.OrganizationUUID
	}

	if usage.RateLimits != nil {
		if win, okWindow := pickFromRateLimits(*usage.RateLimits, observedAt); okWindow {
			return makeReadyResult(observedAt, win.resetAt, win.remaining, win.windowType, win.longWindowResetAt, planType, orgUUID, win.shortWindowRemaining, win.shortWindowResetAt, win.longWindowRemaining), true
		}
	}

	if usage.Weekly != nil || usage.FiveHour != nil || usage.SessionLimit != nil {
		limits := claudeRateLimits{
			Weekly:       usage.Weekly,
			FiveHour:     usage.FiveHour,
			SessionLimit: usage.SessionLimit,
			Daily:        usage.Daily,
		}
		if win, okWindow := pickFromRateLimits(limits, observedAt); okWindow {
			return makeReadyResult(observedAt, win.resetAt, win.remaining, win.windowType, win.longWindowResetAt, planType, orgUUID, win.shortWindowRemaining, win.shortWindowResetAt, win.longWindowRemaining), true
		}
	}

	for _, candidate := range []struct {
		win  *claudeWindow
		wtyp WindowType
	}{
		{usage.Weekly, WindowWeekly},
		{usage.Daily, WindowDaily},
		{usage.FiveHour, WindowFiveHour},
		{usage.SessionLimit, WindowFiveHour},
		{&usage.claudeWindow, WindowWeekly},
	} {
		if candidate.win == nil || !hasWindowData(*candidate.win) {
			continue
		}
		resetAt := windowResetTime(observedAt, *candidate.win)
		remaining, okRemaining := windowRemaining(*candidate.win)
		if resetAt == nil || !okRemaining {
			continue
		}
		var longReset *time.Time
		if usage.Weekly != nil && hasWindowData(*usage.Weekly) {
			longReset = windowResetTime(observedAt, *usage.Weekly)
		}
		return makeReadyResult(observedAt, resetAt, remaining, candidate.wtyp, longReset, planType, orgUUID, nil, nil, nil), true
	}

	return ProbeResult{}, false
}

func makeReadyResult(observedAt time.Time, resetAt *time.Time, remaining int64, winType WindowType, longReset *time.Time, planType reading.PlanType, orgUUID string, shortRem *int64, shortReset *time.Time, longRem *int64) ProbeResult {
	var windows []reading.QuotaWindow
	if shortRem != nil && shortReset != nil {
		windows = append(windows, reading.QuotaWindow{Duration: 5 * time.Hour, Remaining: *shortRem, ResetAt: *shortReset})
	}
	switch {
	case longRem != nil && longReset != nil:
		windows = append(windows, reading.QuotaWindow{Duration: 7 * 24 * time.Hour, Remaining: *longRem, ResetAt: *longReset})
	case winType == WindowWeekly && resetAt != nil:
		if shortReset == nil || !shortReset.Equal(*resetAt) {
			windows = append(windows, reading.QuotaWindow{Duration: 7 * 24 * time.Hour, Remaining: remaining, ResetAt: *resetAt})
		}
	case winType == WindowFiveHour && resetAt != nil:
		if shortReset == nil {
			windows = append(windows, reading.QuotaWindow{Duration: 5 * time.Hour, Remaining: remaining, ResetAt: *resetAt})
		}
	case winType == WindowDaily && resetAt != nil:
		windows = append(windows, reading.QuotaWindow{Duration: 24 * time.Hour, Remaining: remaining, ResetAt: *resetAt})
	}
	return ProbeResult{
		ObservedAt:           observedAt.UTC(),
		ResetAt:              resetAt,
		Remaining:            int64Ptr(remaining),
		Window:               winType,
		Windows:              windows,
		LongWindowResetAt:    longReset,
		ShortWindowRemaining: shortRem,
		ShortWindowResetAt:   shortReset,
		LongWindowRemaining:  longRem,
		Status:               reading.StatusReady,
		PlanType:             planType,
		OrganizationUUID:     orgUUID,
	}
}

// pickFromRateLimits prefers the weekly window, except when a window is used up, which then
// decides when the credential becomes usable again.
func pickFromRateLimits(limits claudeRateLimits, observedAt time.Time) (effectiveWindow, bool) {
	var fiveHour, weekly *claudeWindow
	for _, w := range []*claudeWindow{limits.FiveHour, limits.SessionLimit, limits.Primary} {
		if w != nil && hasWindowData(*w) {
			fiveHour = w
			break
		}
	}
	for _, w := range []*claudeWindow{limits.Weekly, limits.Secondary} {
		if w != nil && hasWindowData(*w) {
			weekly = w
			break
		}
	}

	var fiveHourReset, weeklyReset *time.Time
	var fiveHourRem, weeklyRem int64
	var okFive, okWeek bool
	if fiveHour != nil {
		fiveHourReset = windowResetTime(observedAt, *fiveHour)
		fiveHourRem, okFive = windowRemaining(*fiveHour)
	}
	if weekly != nil {
		weeklyReset = windowResetTime(observedAt, *weekly)
		weeklyRem, okWeek = windowRemaining(*weekly)
	}

	if okFive && okWeek && fiveHourReset != nil && weeklyReset != nil {
		short, shortReset := int64Ptr(fiveHourRem), fiveHourReset
		long := int64Ptr(weeklyRem)
		switch {
		case weeklyRem <= 0:
			return effectiveWindow{resetAt: weeklyReset, remaining: 0, windowType: WindowWeekly, longWindowResetAt: weeklyReset, shortWindowRemaining: short, shortWindowResetAt: shortReset, longWindowRemaining: long}, true
		case fiveHourRem <= 0:
			return effectiveWindow{resetAt: fiveHourReset, remaining: 0, windowType: WindowFiveHour, longWindowResetAt: weeklyReset, shortWindowRemaining: short, shortWindowResetAt: shortReset, longWindowRemaining: long}, true
		default:
			return effectiveWindow{resetAt: weeklyReset, remaining: weeklyRem, windowType: WindowWeekly, longWindowResetAt: weeklyReset, shortWindowRemaining: short, shortWindowResetAt: shortReset, longWindowRemaining: long}, true
		}
	}
	if okWeek && weeklyReset != nil {
		return effectiveWindow{resetAt: weeklyReset, remaining: weeklyRem, windowType: WindowWeekly, longWindowResetAt: weeklyReset}, true
	}
	if okFive && fiveHourReset != nil {
		return effectiveWindow{resetAt: fiveHourReset, remaining: fiveHourRem, windowType: WindowFiveHour}, true
	}
	return effectiveWindow{}, false
}

// ParseClaudeRateLimitError decodes a 429 body and headers. The credential is exhausted, so the
// reading reports zero remaining and the reset the provider hints at.
func ParseClaudeRateLimitError(raw []byte, headers host.Header, observedAt time.Time) ProbeResult {
	orgUUIDFromHeader := extractOrgFromHeaders(headers)

	if res, okHeaders := parseUnifiedRateLimitHeaders(headers, observedAt); okHeaders {
		res.Remaining = int64Ptr(0)
		res.Error = "rate limit reached"
		if res.OrganizationUUID == "" && orgUUIDFromHeader != "" {
			res.OrganizationUUID = orgUUIDFromHeader
		}
		return res
	}

	var resetAt *time.Time
	planType := reading.PlanTypePro

	if len(raw) > 0 {
		var generic map[string]any
		if errUnmarshal := json.Unmarshal(raw, &generic); errUnmarshal == nil {
			resetAt = extractResetFromGenericMap(generic, observedAt)
			if parsed := inferPlanFromGenericMap(generic); parsed != reading.PlanTypeUnknown {
				planType = parsed
			}
		}
	}
	if resetAt == nil && headers != nil {
		resetAt = extractResetFromHeaders(headers, observedAt)
	}
	if resetAt == nil {
		defaultReset := observedAt.UTC().Add(5 * time.Hour)
		resetAt = &defaultReset
	}

	zeroRemaining := int64(0)
	return ProbeResult{
		ObservedAt:       observedAt.UTC(),
		ResetAt:          resetAt,
		Remaining:        &zeroRemaining,
		Window:           WindowFiveHour,
		Windows:          []reading.QuotaWindow{{Duration: 5 * time.Hour, Remaining: 0, ResetAt: *resetAt}},
		Status:           reading.StatusReady,
		PlanType:         planType,
		OrganizationUUID: orgUUIDFromHeader,
		Error:            "rate limit reached",
	}
}

// parseUnifiedRateLimitHeaders reads Anthropic's unified rate-limit headers, which report 5-hour
// and weekly utilization exactly and therefore beat any JSON estimate.
func parseUnifiedRateLimitHeaders(headers host.Header, observedAt time.Time) (ProbeResult, bool) {
	if headers == nil {
		return ProbeResult{}, false
	}
	orgUUID := extractOrgFromHeaders(headers)

	util7dStr := wire.HeaderValue(headers, "anthropic-ratelimit-unified-7d-utilization")
	reset7dStr := wire.HeaderValue(headers, "anthropic-ratelimit-unified-7d-reset")
	status7d := strings.ToLower(wire.HeaderValue(headers, "anthropic-ratelimit-unified-7d-status"))

	util5hStr := wire.HeaderValue(headers, "anthropic-ratelimit-unified-5h-utilization")
	reset5hStr := wire.HeaderValue(headers, "anthropic-ratelimit-unified-5h-reset")
	status5h := strings.ToLower(wire.HeaderValue(headers, "anthropic-ratelimit-unified-5h-status"))

	unifiedStatus := strings.ToLower(wire.HeaderValue(headers, "anthropic-ratelimit-unified-status"))
	utilUnifiedStr := wire.HeaderValue(headers, "anthropic-ratelimit-unified-utilization")
	resetUnifiedStr := wire.HeaderValue(headers, "anthropic-ratelimit-unified-reset")

	hasUnified := util7dStr != "" || reset7dStr != "" || status7d != "" ||
		util5hStr != "" || reset5hStr != "" || status5h != "" ||
		unifiedStatus != "" || utilUnifiedStr != "" || resetUnifiedStr != ""
	if !hasUnified {
		return ProbeResult{}, false
	}

	var reset7d, reset5h, resetUnified *time.Time
	if reset7dStr != "" {
		if parsed, okParse := wire.ParseTimeString(reset7dStr); okParse {
			reset7d = parsed
		}
	}
	if reset5hStr != "" {
		if parsed, okParse := wire.ParseTimeString(reset5hStr); okParse {
			reset5h = parsed
		}
	}
	if resetUnifiedStr != "" {
		if parsed, okParse := wire.ParseTimeString(resetUnifiedStr); okParse {
			resetUnified = parsed
		}
	}

	var util7d float64
	var hasUtil7d bool
	if util7dStr != "" {
		if parsed, errParse := strconv.ParseFloat(util7dStr, 64); errParse == nil {
			util7d, hasUtil7d = parsed, true
		}
	}
	var util5h float64
	var hasUtil5h bool
	if util5hStr != "" {
		if parsed, errParse := strconv.ParseFloat(util5hStr, 64); errParse == nil {
			util5h, hasUtil5h = parsed, true
		}
	}
	var utilUnified float64
	var hasUtilUnified bool
	if utilUnifiedStr != "" {
		if parsed, errParse := strconv.ParseFloat(utilUnifiedStr, 64); errParse == nil {
			utilUnified, hasUtilUnified = parsed, true
		}
	}

	planType := reading.PlanTypePro

	if status7d == "rejected" || (hasUtil7d && util7d >= 1.0) {
		resetAt := reset7d
		if resetAt == nil {
			resetAt = resetUnified
		}
		if resetAt == nil {
			fallback := observedAt.UTC().Add(7 * 24 * time.Hour)
			resetAt = &fallback
		}
		return makeReadyResult(observedAt, resetAt, 0, WindowWeekly, reset7d, planType, orgUUID, nil, nil, nil), true
	}
	if status5h == "rejected" || (hasUtil5h && util5h >= 1.0) {
		resetAt := reset5h
		if resetAt == nil {
			resetAt = resetUnified
		}
		if resetAt == nil {
			fallback := observedAt.UTC().Add(5 * time.Hour)
			resetAt = &fallback
		}
		return makeReadyResult(observedAt, resetAt, 0, WindowFiveHour, reset7d, planType, orgUUID, nil, nil, nil), true
	}
	if unifiedStatus == "rejected" {
		resetAt := resetUnified
		if resetAt == nil {
			resetAt = reset7d
		}
		if resetAt == nil {
			resetAt = reset5h
		}
		if resetAt == nil {
			fallback := observedAt.UTC().Add(5 * time.Hour)
			resetAt = &fallback
		}
		return makeReadyResult(observedAt, resetAt, 0, WindowWeekly, reset7d, planType, orgUUID, nil, nil, nil), true
	}

	if hasUtil7d && hasUtil5h {
		remaining7d := int64(math.Max(0, math.Min(100, math.Round((1.0-util7d)*100))))
		remaining5h := int64(math.Max(0, math.Min(100, math.Round((1.0-util5h)*100))))
		if remaining7d <= 0 {
			resetAt := reset7d
			if resetAt == nil {
				fallback := observedAt.UTC().Add(7 * 24 * time.Hour)
				resetAt = &fallback
			}
			return makeReadyResult(observedAt, resetAt, 0, WindowWeekly, reset7d, planType, orgUUID, int64Ptr(remaining5h), reset5h, int64Ptr(remaining7d)), true
		}
		if remaining5h <= 0 {
			resetAt := reset5h
			if resetAt == nil {
				fallback := observedAt.UTC().Add(5 * time.Hour)
				resetAt = &fallback
			}
			return makeReadyResult(observedAt, resetAt, 0, WindowFiveHour, reset7d, planType, orgUUID, int64Ptr(remaining5h), reset5h, int64Ptr(remaining7d)), true
		}
		resetAt := reset7d
		if resetAt == nil {
			fallback := observedAt.UTC().Add(7 * 24 * time.Hour)
			resetAt = &fallback
		}
		return makeReadyResult(observedAt, resetAt, remaining7d, WindowWeekly, reset7d, planType, orgUUID, int64Ptr(remaining5h), reset5h, int64Ptr(remaining7d)), true
	}
	if hasUtil7d {
		remaining7d := int64(math.Max(0, math.Min(100, math.Round((1.0-util7d)*100))))
		resetAt := reset7d
		if resetAt == nil {
			fallback := observedAt.UTC().Add(7 * 24 * time.Hour)
			resetAt = &fallback
		}
		return makeReadyResult(observedAt, resetAt, remaining7d, WindowWeekly, reset7d, planType, orgUUID, nil, nil, nil), true
	}
	if hasUtil5h {
		remaining5h := int64(math.Max(0, math.Min(100, math.Round((1.0-util5h)*100))))
		resetAt := reset5h
		if resetAt == nil {
			fallback := observedAt.UTC().Add(5 * time.Hour)
			resetAt = &fallback
		}
		return makeReadyResult(observedAt, resetAt, remaining5h, WindowFiveHour, reset7d, planType, orgUUID, nil, nil, nil), true
	}
	if hasUtilUnified {
		remaining := int64(math.Max(0, math.Min(100, math.Round((1.0-utilUnified)*100))))
		resetAt := resetUnified
		if resetAt == nil {
			fallback := observedAt.UTC().Add(7 * 24 * time.Hour)
			resetAt = &fallback
		}
		return makeReadyResult(observedAt, resetAt, remaining, WindowWeekly, resetUnified, planType, orgUUID, nil, nil, nil), true
	}
	if unifiedStatus == "allowed" || status7d == "allowed" || status5h == "allowed" {
		resetAt := reset7d
		if resetAt == nil {
			resetAt = reset5h
		}
		if resetAt == nil {
			resetAt = resetUnified
		}
		if resetAt == nil {
			fallback := observedAt.UTC().Add(7 * 24 * time.Hour)
			resetAt = &fallback
		}
		return makeReadyResult(observedAt, resetAt, 100, WindowWeekly, reset7d, planType, orgUUID, nil, nil, nil), true
	}
	return ProbeResult{}, false
}

func extractOrgFromHeaders(headers host.Header) string {
	if headers == nil {
		return ""
	}
	return wire.HeaderValue(headers, "anthropic-organization-id")
}

func extractResetFromGenericMap(m map[string]any, observedAt time.Time) *time.Time {
	for _, key := range []string{"resets_at", "resetsAt", "reset_at", "reset_time", "reset_after_seconds"} {
		value, okValue := m[key]
		if !okValue {
			continue
		}
		if parsed, okParse := wire.ParseAnyTime(value); okParse {
			return parsed
		}
		if seconds, okSeconds := wire.ToInt64(value); okSeconds && seconds > 0 {
			resetAt := observedAt.UTC().Add(time.Duration(seconds) * time.Second)
			return &resetAt
		}
	}
	for _, nestedKey := range []string{"error", "rate_limit", "rate_limits", "detail"} {
		if nested, okNested := m[nestedKey].(map[string]any); okNested {
			if resetAt := extractResetFromGenericMap(nested, observedAt); resetAt != nil {
				return resetAt
			}
		}
	}
	return nil
}

func extractResetFromHeaders(headers host.Header, observedAt time.Time) *time.Time {
	for key, values := range headers {
		lowerKey := strings.ToLower(strings.TrimSpace(key))
		for _, value := range values {
			trimmed := strings.TrimSpace(value)
			if trimmed == "" {
				continue
			}
			switch lowerKey {
			case "anthropic-ratelimit-unified-7d-reset", "anthropic-ratelimit-unified-5h-reset", "anthropic-ratelimit-unified-reset",
				"anthropic-ratelimit-requests-reset", "anthropic-ratelimit-tokens-reset", "x-ratelimit-reset":
				if parsed, okParse := wire.ParseTimeString(trimmed); okParse {
					return parsed
				}
			case "retry-after":
				if seconds, errParse := strconv.ParseInt(trimmed, 10, 64); errParse == nil && seconds > 0 {
					resetAt := observedAt.UTC().Add(time.Duration(seconds) * time.Second)
					return &resetAt
				}
				if parsed, okParse := wire.ParseTimeString(trimmed); okParse {
					return parsed
				}
			}
		}
	}
	return nil
}

func inferPlanFromGenericMap(m map[string]any) reading.PlanType {
	for _, key := range []string{"plan_type", "plan", "tier", "subscription"} {
		if value, okValue := m[key]; okValue {
			if text, okText := wire.ToString(value); okText {
				if parsed := inferPlanType(text); parsed != reading.PlanTypeUnknown {
					return parsed
				}
			}
		}
	}
	if capabilities, okCapabilities := m["capabilities"].([]any); okCapabilities {
		for _, entry := range capabilities {
			if text, okText := wire.ToString(entry); okText {
				if parsed := inferPlanType(text); parsed != reading.PlanTypeUnknown {
					return parsed
				}
			}
		}
	}
	return reading.PlanTypeUnknown
}

func hasWindowData(window claudeWindow) bool {
	if _, okParse := wire.ParseAnyTime(window.ResetsAt); okParse {
		return true
	}
	if _, okParse := wire.ParseAnyTime(window.ResetTime); okParse {
		return true
	}
	if seconds, okSeconds := wire.ToInt64(window.ResetAfterSeconds); okSeconds && seconds > 0 {
		return true
	}
	for _, value := range []any{window.Remaining, window.RemainingQueries, window.Limit, window.Used, window.UsedPercent} {
		if _, okFloat := wire.ToFloat64(value); okFloat {
			return true
		}
	}
	if reached, okReached := wire.ToBool(window.LimitReached); okReached && reached {
		return true
	}
	return false
}

func windowResetTime(observedAt time.Time, window claudeWindow) *time.Time {
	for _, candidate := range []any{window.ResetsAt, window.ResetTime} {
		if resetAt, okParse := wire.ParseAnyTime(candidate); okParse {
			return resetAt
		}
	}
	if seconds, okSeconds := wire.ToInt64(window.ResetAfterSeconds); okSeconds && seconds > 0 {
		resetAt := observedAt.UTC().Add(time.Duration(seconds) * time.Second)
		return &resetAt
	}
	return nil
}

func windowRemaining(window claudeWindow) (int64, bool) {
	if remaining, okRemaining := wire.ToFloat64(window.Remaining); okRemaining {
		return nonNegativeCeil(remaining), true
	}
	if remainingQueries, okQueries := wire.ToFloat64(window.RemainingQueries); okQueries {
		return nonNegativeCeil(remainingQueries), true
	}
	limit, okLimit := wire.ToFloat64(window.Limit)
	used, okUsed := wire.ToFloat64(window.Used)
	if okLimit && okUsed {
		return nonNegativeCeil(limit - used), true
	}
	if reached, okReached := wire.ToBool(window.LimitReached); okReached && reached {
		return 0, true
	}
	if usedPercent, okPercent := wire.ToFloat64(window.UsedPercent); okPercent {
		return nonNegativeCeil(100 - usedPercent), true
	}
	return 0, false
}

func inferClaudePlanType(usage claudeUsageResponse) reading.PlanType {
	for _, capability := range usage.Capabilities {
		lower := strings.ToLower(strings.TrimSpace(capability))
		if strings.Contains(lower, "pro") {
			return reading.PlanTypePro
		}
		if strings.Contains(lower, "team") || strings.Contains(lower, "enterprise") {
			return reading.PlanTypeTeam
		}
	}
	for _, raw := range []string{usage.PlanType, usage.Tier} {
		if parsed := inferPlanType(raw); parsed != reading.PlanTypeUnknown {
			return parsed
		}
	}
	return reading.PlanTypeUnknown
}

func inferPlanType(value string) reading.PlanType {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "free", "claude_free", "none":
		return reading.PlanTypeFree
	case "plus":
		return reading.PlanTypePlus
	case "pro", "claude_pro", "claude-pro":
		return reading.PlanTypePro
	case "team", "claude_team", "enterprise":
		return reading.PlanTypeTeam
	default:
		return reading.PlanTypeUnknown
	}
}

func failedResult(observedAt time.Time, message string) ProbeResult {
	return ProbeResult{
		ObservedAt: observedAt.UTC(),
		Window:     WindowUnknown,
		Status:     reading.StatusProbeFailed,
		PlanType:   reading.PlanTypeUnknown,
		Error:      message,
	}
}

func nonNegativeCeil(value float64) int64 {
	if value <= 0 {
		return 0
	}
	return int64(math.Ceil(value))
}

func int64Ptr(value int64) *int64 {
	return &value
}
