package live

import (
	"encoding/json"
	"strconv"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

// InUseWindow is how long the last successful Live pick is shown as in use.
const InUseWindow = 2 * time.Minute

// liveLimitFallback is used when Codex reports usage_limit_reached without a reset time.
const liveLimitFallback = time.Hour

var knownLivePlans = map[string]struct{}{
	"free": {}, "plus": {}, "pro": {}, "team": {}, "business": {}, "enterprise": {},
}

// HostState is the host.auth.list subset Live selection reads. StatusMessage is parsed for a
// usage_limit_reached reset and is never copied onto the status page or into logs.
type HostState struct {
	AuthID         string
	Disabled       bool
	Unavailable    bool
	NextRetryAfter time.Time
	StatusMessage  string
}

// Skip is one allowlisted credential Live did not pick, with a sanitized reason.
type Skip struct {
	AuthID string `json:"auth_id"`
	Reason string `json:"reason"`
}

// MatchesAllowlist reports whether authID is one of the configured live_auth_ids, including
// basename aliases the selector already accepts.
func MatchesAllowlist(authID string, authIDs []string) bool {
	allowlist := make(map[string]struct{}, len(authIDs)*2)
	for _, id := range authIDs {
		for _, alias := range uniqueIDAliases(id) {
			allowlist[alias] = struct{}{}
		}
	}
	for _, alias := range uniqueIDAliases(authID) {
		if _, ok := allowlist[alias]; ok {
			return true
		}
	}
	return false
}

// NoteHostStates replaces host-listing skips. A usage_limit_reached deadline is copied into
// remembered limits so a later listing that omits StatusMessage does not revive that account.
func (s *Selector) NoteHostStates(states []HostState, now time.Time) {
	if now.IsZero() {
		now = time.Now()
	}
	limits := make(map[string]time.Time)
	retries := make(map[string]time.Time)
	blocked := make(map[string]string)
	for _, state := range states {
		id := strings.TrimSpace(state.AuthID)
		if id == "" {
			continue
		}
		aliases := uniqueIDAliases(id)
		if until, _, ok := parseUsageLimit(state.StatusMessage, now); ok {
			for _, alias := range aliases {
				limits[alias] = until
			}
		}
		if state.NextRetryAfter.After(now) {
			for _, alias := range aliases {
				retries[alias] = state.NextRetryAfter
			}
		}
		reason := ""
		if state.Disabled {
			reason = "Account disabled"
		} else if state.Unavailable {
			reason = "Unavailable"
		}
		if reason != "" {
			for _, alias := range aliases {
				blocked[alias] = reason
			}
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.hostLimits = limits
	s.retries = retries
	s.blocked = blocked
	if s.remembered == nil {
		s.remembered = make(map[string]time.Time)
	}
	mergeLimitMap(s.remembered, limits)
}

// RememberLimit keeps a Live usage-limit deadline after the host listing drops it. until is the
// earliest time the credential may be tried again.
func (s *Selector) RememberLimit(authID string, until time.Time) {
	authID = strings.TrimSpace(authID)
	if authID == "" || until.IsZero() {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.remembered == nil {
		s.remembered = make(map[string]time.Time)
	}
	for _, alias := range uniqueIDAliases(authID) {
		if existing, ok := s.remembered[alias]; !ok || until.After(existing) {
			s.remembered[alias] = until
		}
	}
}

func mergeLimitMap(dst, src map[string]time.Time) {
	if len(src) == 0 {
		return
	}
	if dst == nil {
		return
	}
	for id, until := range src {
		if existing, ok := dst[id]; !ok || until.After(existing) {
			dst[id] = until
		}
	}
}

func copyFutureLimits(src map[string]time.Time, now time.Time) map[string]time.Time {
	if len(src) == 0 {
		return nil
	}
	out := make(map[string]time.Time, len(src))
	for id, until := range src {
		if until.After(now) {
			out[id] = until
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func unionFutureLimits(now time.Time, maps ...map[string]time.Time) map[string]time.Time {
	var out map[string]time.Time
	for _, src := range maps {
		for id, until := range src {
			if !until.After(now) {
				continue
			}
			if out == nil {
				out = make(map[string]time.Time)
			}
			if existing, ok := out[id]; !ok || until.After(existing) {
				out[id] = until
			}
		}
	}
	return out
}

func lookupTime(candidate pluginapi.SchedulerAuthCandidate, values map[string]time.Time, now time.Time) (time.Time, bool) {
	for _, alias := range candidateAliasIDs(candidate) {
		if until, ok := values[alias]; ok && until.After(now) {
			return until, true
		}
	}
	return time.Time{}, false
}

func lookupBlocked(candidate pluginapi.SchedulerAuthCandidate, blocked map[string]string) string {
	for _, alias := range candidateAliasIDs(candidate) {
		if reason := blocked[alias]; reason != "" {
			return reason
		}
	}
	return ""
}

func exclusionReason(
	candidate pluginapi.SchedulerAuthCandidate,
	limits, retries map[string]time.Time,
	blocked map[string]string,
	now time.Time,
) string {
	if parseBoolAttr(candidate.Attributes["disabled"]) || isDisabledStatus(candidate.Status) {
		return "Account disabled"
	}
	if parseBoolAttr(candidate.Attributes["unavailable"]) {
		return "Unavailable"
	}
	if reason := lookupBlocked(candidate, blocked); reason != "" {
		return reason
	}
	if until, plan, ok := parseUsageLimit(candidate.Attributes["status_message"], now); ok {
		return liveLimitReason(until, plan)
	}
	if until, ok := lookupTime(candidate, limits, now); ok {
		return liveLimitReason(until, "")
	}
	if until, cooling := cooldownUntil(candidate.Attributes["next_retry_after"], now); cooling {
		return "In cooldown until " + until.UTC().Format(time.RFC3339)
	}
	if until, ok := lookupTime(candidate, retries, now); ok {
		return "In cooldown until " + until.UTC().Format(time.RFC3339)
	}
	return ""
}

func liveLimitReason(until time.Time, plan string) string {
	reason := "Live usage limit until " + until.UTC().Format(time.RFC3339)
	if plan != "" {
		reason = "Live usage limit (" + plan + ") until " + until.UTC().Format(time.RFC3339)
	}
	return reason
}

func parseUsageLimit(raw string, now time.Time) (time.Time, string, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" || !strings.Contains(strings.ToLower(raw), "usage_limit_reached") {
		return time.Time{}, "", false
	}
	var payload any
	if errUnmarshal := json.Unmarshal([]byte(raw), &payload); errUnmarshal == nil {
		if until, plan, ok := walkUsageLimit(payload, now); ok {
			return until, plan, true
		}
	}
	if start := strings.Index(raw, "{"); start >= 0 {
		if errUnmarshal := json.Unmarshal([]byte(raw[start:]), &payload); errUnmarshal == nil {
			if until, plan, ok := walkUsageLimit(payload, now); ok {
				return until, plan, true
			}
		}
	}
	return now.Add(liveLimitFallback), "", true
}

func walkUsageLimit(value any, now time.Time) (time.Time, string, bool) {
	object, okObject := value.(map[string]any)
	if !okObject {
		list, okList := value.([]any)
		if !okList {
			return time.Time{}, "", false
		}
		for _, item := range list {
			if until, plan, ok := walkUsageLimit(item, now); ok {
				return until, plan, true
			}
		}
		return time.Time{}, "", false
	}
	if strings.EqualFold(jsonString(object["type"]), "usage_limit_reached") {
		return resetFromLimit(object, now)
	}
	if nested, okNested := object["error"]; okNested {
		if until, plan, ok := walkUsageLimit(nested, now); ok {
			return until, plan, true
		}
	}
	return time.Time{}, "", false
}

func resetFromLimit(object map[string]any, now time.Time) (time.Time, string, bool) {
	plan := knownPlan(jsonString(object["plan_type"]))
	if until, ok := unixTime(object["resets_at"]); ok && until.After(now) {
		return until, plan, true
	}
	if seconds, ok := jsonNumber(object["resets_in_seconds"]); ok && seconds > 0 {
		return now.Add(time.Duration(seconds) * time.Second), plan, true
	}
	return now.Add(liveLimitFallback), plan, true
}

func knownPlan(plan string) string {
	plan = strings.ToLower(strings.TrimSpace(plan))
	if _, ok := knownLivePlans[plan]; ok {
		return plan
	}
	return ""
}

func jsonString(value any) string {
	switch typed := value.(type) {
	case string:
		return strings.TrimSpace(typed)
	default:
		return ""
	}
}

func jsonNumber(value any) (int64, bool) {
	switch typed := value.(type) {
	case float64:
		return int64(typed), true
	case json.Number:
		parsed, err := typed.Int64()
		return parsed, err == nil
	case string:
		parsed, err := strconv.ParseInt(strings.TrimSpace(typed), 10, 64)
		return parsed, err == nil
	default:
		return 0, false
	}
}

func unixTime(value any) (time.Time, bool) {
	seconds, ok := jsonNumber(value)
	if !ok || seconds <= 0 {
		return time.Time{}, false
	}
	return time.Unix(seconds, 0).UTC(), true
}
