package xai

import (
	"encoding/json"
	"strconv"
	"strings"

	"github.com/karlorz/cpa-expiring-first/internal/probe/reading"
)

func strconvFormatInt(value int64) string {
	return strconv.FormatInt(value, 10)
}

func strconvFormatFloat(value float64) string {
	return strconv.FormatFloat(value, 'f', -1, 64)
}

func parseTierNumber(text string) (int64, error) {
	return strconv.ParseInt(text, 10, 64)
}

// safeError truncates and redacts a probe error, so credentials never reach logs or the console.
func safeError(message string) string {
	trimmed := strings.TrimSpace(message)
	if trimmed == "" {
		return "probe failed"
	}
	if strings.Contains(trimmed, "access_token") || strings.Contains(trimmed, "refresh_token") {
		return "probe failed"
	}
	if len(trimmed) > 240 {
		return trimmed[:240]
	}
	return trimmed
}

// ClassifyPlan decides free or paid from the settings and billing bodies and the JWT tier. A
// credential whose plan cannot be fetched at all counts as free, matching quota-pacer.
func ClassifyPlan(settingsBody, billingBody []byte, jwtTier string) (PlanClass, string) {
	if class, okTier := classifyTierToken(jwtTier); okTier {
		if class == PlanClassPaid {
			return PlanClassPaid, "jwt_tier"
		}
		// A free or absent tier claim still defers to an explicit paid product in the bodies.
	}
	if class, source, okBodies := classifyJSONBodies(settingsBody, "settings"); okBodies {
		return class, source
	}
	if class, source, okBodies := classifyJSONBodies(billingBody, "billing"); okBodies {
		return class, source
	}
	if class, okTier := classifyTierToken(jwtTier); okTier {
		return class, "jwt_tier"
	}
	return PlanClassFree, "default_unfetchable"
}

func classifyJSONBodies(body []byte, source string) (PlanClass, string, bool) {
	if len(strings.TrimSpace(string(body))) == 0 {
		return "", "", false
	}
	blob := strings.ToLower(string(body))
	if hasPaidPlanSignal(blob) {
		return PlanClassPaid, source, true
	}
	if hasFreePlanSignal(blob) {
		return PlanClassFree, source, true
	}
	// A parseable body with no plan field proves nothing, so later sources still get a turn.
	var payload map[string]any
	if errUnmarshal := json.Unmarshal(body, &payload); errUnmarshal != nil {
		return "", "", false
	}
	if tier := extractTierLike(payload); tier != "" {
		if class, okTier := classifyTierToken(tier); okTier {
			return class, source, true
		}
	}
	return "", "", false
}

func extractTierLike(payload map[string]any) string {
	keys := []string{"tier", "plan", "plan_type", "planType", "subscription", "product", "sku", "name"}
	for _, key := range keys {
		value, okValue := payload[key]
		if !okValue {
			continue
		}
		if text := anyToTierString(value); text != "" {
			return text
		}
		if nested, okNested := value.(map[string]any); okNested {
			if text := extractTierLike(nested); text != "" {
				return text
			}
		}
	}
	for _, key := range []string{"user", "subscription", "product", "account", "data"} {
		if nested, okNested := payload[key].(map[string]any); okNested {
			if text := extractTierLike(nested); text != "" {
				return text
			}
		}
	}
	return ""
}

// anyToTierString normalizes a tier value to a string. SuperGrok sends `"tier": 1` as a number,
// which encoding/json decodes to float64.
func anyToTierString(value any) string {
	switch typed := value.(type) {
	case string:
		return strings.TrimSpace(typed)
	case json.Number:
		return strings.TrimSpace(typed.String())
	case float64:
		if typed == float64(int64(typed)) {
			return strconvFormatInt(int64(typed))
		}
		return strconvFormatFloat(typed)
	case int:
		return strconvFormatInt(int64(typed))
	case int64:
		return strconvFormatInt(typed)
	default:
		return ""
	}
}

func classifyTierToken(tier string) (PlanClass, bool) {
	text := strings.ToLower(strings.TrimSpace(tier))
	if text == "" {
		return "", false
	}
	if number, errParse := parseTierNumber(text); errParse == nil {
		switch {
		case number >= 1:
			return PlanClassPaid, true
		case number == 0:
			return PlanClassFree, true
		default:
			return "", false
		}
	}
	if hasPaidPlanSignal(text) {
		return PlanClassPaid, true
	}
	if hasFreePlanSignal(text) {
		return PlanClassFree, true
	}
	return "", false
}

func hasPaidPlanSignal(blob string) bool {
	lower := strings.ToLower(blob)
	paid := []string{
		"supergrok", "super-grok", "super_grok",
		"\"pro\"", "pro-", "-pro", "plan_pro", "plan-pro", "tier_pro", "tier-pro", "tier\":\"pro",
		"heavy", "lite",
		"plus", "team", "premium", "enterprise",
		"paid", "subscription_active",
	}
	for _, marker := range paid {
		if !strings.Contains(lower, marker) {
			continue
		}
		switch marker {
		case "lite", "plus", "pro-", "-pro", "\"pro\"":
			if !paidContextOK(lower, marker) {
				continue
			}
		}
		return true
	}
	switch strings.Trim(lower, "\"' \t\n") {
	case "pro", "lite", "heavy", "plus", "team", "premium":
		return true
	}
	return false
}

// paidContextOK requires a plan-ish word near an ambiguous marker such as "lite", so unrelated
// text does not read as a paid plan.
func paidContextOK(blob, needle string) bool {
	index := strings.Index(blob, needle)
	if index < 0 {
		return false
	}
	start := index - 24
	if start < 0 {
		start = 0
	}
	end := index + len(needle) + 24
	if end > len(blob) {
		end = len(blob)
	}
	window := blob[start:end]
	for _, context := range []string{"tier", "plan", "product", "sku", "subscription", "package"} {
		if strings.Contains(window, context) {
			return true
		}
	}
	return len(strings.TrimSpace(blob)) <= 32
}

func hasFreePlanSignal(blob string) bool {
	lower := strings.ToLower(blob)
	for _, marker := range []string{"grokfree", "grok-free", "grok_free", "free_tier", "freetier", "plan_free", "plan-free", "tier_free", "tier-free", "tier\":\"free", "\"free\"", "none"} {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	switch strings.Trim(lower, "\"' \t\n") {
	case "free", "none", "null", "":
		return true
	}
	return false
}

func planTypeFromClass(class PlanClass) reading.PlanType {
	switch class {
	case PlanClassFree:
		return reading.PlanTypeFree
	case PlanClassPaid:
		return reading.PlanTypePlus
	default:
		return reading.PlanTypeUnknown
	}
}

func isOAuthAuthKind(authKind string) bool {
	return strings.EqualFold(strings.TrimSpace(authKind), "oauth")
}
