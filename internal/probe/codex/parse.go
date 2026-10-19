package codex

import (
	"bytes"
	"encoding/json"
	"strings"
	"time"

	"github.com/karlorz/cpa-expiring-first/internal/probe/reading"
	"github.com/karlorz/cpa-expiring-first/internal/probe/wire"
)

type whamUsage struct {
	PlanType  string        `json:"plan_type"`
	RateLimit whamRateLimit `json:"rate_limit"`
}

type whamRateLimit struct {
	PrimaryWindow   whamWindow `json:"primary_window"`
	SecondaryWindow whamWindow `json:"secondary_window"`
}

type whamWindow struct {
	ResetAt            any `json:"reset_at"`
	ResetAfterSeconds  any `json:"reset_after_seconds"`
	LimitWindowSeconds any `json:"limit_window_seconds"`
	Remaining          any `json:"remaining"`
	Limit              any `json:"limit"`
	Used               any `json:"used"`
	UsedPercent        any `json:"used_percent"`
	LimitReached       any `json:"limit_reached"`
	Name               any `json:"name"`
	Type               any `json:"type"`
	Category           any `json:"category"`
	Label              any `json:"label"`
	Bucket             any `json:"bucket"`
	Scope              any `json:"scope"`
}

// ParseWhamUsage decodes a wham usage response into a reading with a trusted quota window.
func ParseWhamUsage(raw []byte, observedAt time.Time) ProbeResult {
	var usage whamUsage
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if errDecode := decoder.Decode(&usage); errDecode != nil {
		return failedResult(observedAt, "parse wham usage failed")
	}
	result, okWindow := pickEffectiveWindow(usage, observedAt)
	if !okWindow {
		return failedResult(observedAt, "trusted quota window unavailable")
	}
	return ProbeResult{
		ObservedAt:           observedAt.UTC(),
		ResetAt:              result.resetAt,
		Remaining:            int64Ptr(result.remaining),
		Window:               result.windowType,
		Windows:              result.windows,
		LongWindowResetAt:    result.longWindowResetAt,
		ShortWindowRemaining: result.shortWindowRemaining,
		ShortWindowResetAt:   result.shortWindowResetAt,
		LongWindowRemaining:  result.longWindowRemaining,
		Status:               reading.StatusReady,
		PlanType:             inferPlanType(usage.PlanType),
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

func hasWindowData(window whamWindow) bool {
	if _, okReset := wire.ParseAnyTime(window.ResetAt); okReset {
		return true
	}
	if seconds, okSeconds := wire.ToInt64(window.ResetAfterSeconds); okSeconds && seconds > 0 {
		return true
	}
	for _, value := range []any{window.Remaining, window.Limit, window.Used, window.UsedPercent} {
		if _, okFloat := wire.ToFloat64(value); okFloat {
			return true
		}
	}
	if _, okReached := wire.ToBool(window.LimitReached); okReached {
		return true
	}
	return false
}

func windowResetTime(observedAt time.Time, window whamWindow) *time.Time {
	if resetAt, okReset := wire.ParseAnyTime(window.ResetAt); okReset {
		return resetAt
	}
	seconds, okSeconds := wire.ToInt64(window.ResetAfterSeconds)
	if !okSeconds || seconds <= 0 {
		return nil
	}
	resetAt := observedAt.UTC().Add(time.Duration(seconds) * time.Second)
	return &resetAt
}

func windowRemaining(window whamWindow) (int64, bool) {
	if reached, okReached := wire.ToBool(window.LimitReached); okReached && reached {
		return 0, true
	}
	if usedPercent, okPercent := wire.ToFloat64(window.UsedPercent); okPercent {
		return wire.ClampPercent(100 - usedPercent), true
	}
	limit, okLimit := wire.ToFloat64(window.Limit)
	remaining, okRemaining := wire.ToFloat64(window.Remaining)
	if okRemaining {
		if okLimit && limit > 0 {
			return wire.ClampPercent((remaining / limit) * 100), true
		}
		if remaining <= 1.0 && remaining >= 0 {
			return wire.ClampPercent(remaining * 100), true
		}
		return wire.ClampPercent(remaining), true
	}
	used, okUsed := wire.ToFloat64(window.Used)
	if okLimit && okUsed && limit > 0 {
		return wire.ClampPercent(((limit - used) / limit) * 100), true
	}
	return 0, false
}

func windowMetadataStrings(window whamWindow) []string {
	fields := []any{window.Name, window.Type, window.Category, window.Label, window.Bucket, window.Scope}
	values := make([]string, 0, len(fields))
	for _, field := range fields {
		text, okText := wire.ToString(field)
		if !okText {
			continue
		}
		normalized := strings.ToLower(strings.TrimSpace(text))
		normalized = strings.ReplaceAll(normalized, "_", " ")
		normalized = strings.ReplaceAll(normalized, "-", " ")
		if normalized != "" {
			values = append(values, normalized)
		}
	}
	return values
}

func inferPlanType(value string) reading.PlanType {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "free":
		return reading.PlanTypeFree
	case "plus":
		return reading.PlanTypePlus
	case "pro":
		return reading.PlanTypePro
	case "team", "business", "self_serve_business_prolite":
		return reading.PlanTypeTeam
	default:
		return reading.PlanTypeUnknown
	}
}

func paidPlan(planType reading.PlanType) bool {
	switch planType {
	case reading.PlanTypePlus, reading.PlanTypePro, reading.PlanTypeTeam:
		return true
	default:
		return false
	}
}

func int64Ptr(value int64) *int64 {
	return &value
}
