package live

import (
	"strconv"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

// PoolRow is one live_auth_ids credential as shown on the status page.
type PoolRow struct {
	Order          int        `json:"order"`
	AuthID         string     `json:"auth_id"`
	Present        bool       `json:"present"`
	Disabled       bool       `json:"disabled"`
	Unavailable    bool       `json:"unavailable"`
	Cooling        bool       `json:"cooling"`
	Eligible       bool       `json:"eligible"`
	NextPick       bool       `json:"next_pick"`
	FillFirstNext  bool       `json:"fill_first_next"`
	LastSelected   bool       `json:"last_selected"`
	InUse          bool       `json:"in_use"`
	LiveLimit      bool       `json:"live_limit"`
	LiveLimitUntil *time.Time `json:"live_limit_until,omitempty"`
	Priority       int        `json:"account_priority"`
	Reason         string     `json:"reason"`
}

// DescribePool returns the Live pool in fill-first order, joined with host candidate state.
// An explicit live_auth_ids list wins; otherwise available Codex OAuth credentials are used.
// Weekly/five-hour text quota is not Live remaining and is not included here.
func DescribePool(
	config Config,
	candidates []pluginapi.SchedulerAuthCandidate,
	lastAuthID string,
	lastAt time.Time,
	limits map[string]time.Time,
	now time.Time,
) []PoolRow {
	return DescribeIDs(ResolvePool(candidates, config).IDs, candidates, lastAuthID, lastAt, limits, now)
}

// DescribeIDs joins a resolved Live ID list with host candidate state.
func DescribeIDs(
	authIDs []string,
	candidates []pluginapi.SchedulerAuthCandidate,
	lastAuthID string,
	lastAt time.Time,
	limits map[string]time.Time,
	now time.Time,
) []PoolRow {
	rows := make([]PoolRow, 0, len(authIDs))
	lastAuthID = strings.TrimSpace(lastAuthID)
	inUse := lastAuthID != "" && !lastAt.IsZero() && !now.Before(lastAt) && now.Sub(lastAt) <= InUseWindow
	nextMarked := false
	for i, configured := range authIDs {
		id := strings.TrimSpace(configured)
		if id == "" {
			continue
		}
		row := PoolRow{Order: i + 1, AuthID: id}
		candidate, ok := findAllowlistedCandidate(candidates, id)
		if !ok {
			row.Reason = "Not in the host credential list"
			rows = append(rows, row)
			continue
		}
		row.Present = true
		row.AuthID = strings.TrimSpace(candidate.ID)
		if row.AuthID == "" {
			row.AuthID = id
		}
		row.Priority = accountPriority(candidate)
		row.Disabled = parseBoolAttr(candidate.Attributes["disabled"]) || isDisabledStatus(candidate.Status)
		row.Unavailable = parseBoolAttr(candidate.Attributes["unavailable"])
		retryAfter, cooling := cooldownUntil(candidate.Attributes["next_retry_after"], now)
		row.Cooling = cooling
		limitPlan := ""
		if until, okLimit := lookupTime(candidate, limits, now); okLimit {
			limitUntil := until.UTC()
			row.LiveLimit = true
			row.LiveLimitUntil = &limitUntil
			row.Cooling = true
		} else if until, plan, okLimit := parseUsageLimit(candidate.Attributes["status_message"], now); okLimit {
			limitUntil := until.UTC()
			row.LiveLimit = true
			row.LiveLimitUntil = &limitUntil
			row.Cooling = true
			limitPlan = plan
		}
		row.Eligible = !row.Disabled && !row.Unavailable && !row.Cooling
		row.LastSelected = lastAuthID != "" && (strings.EqualFold(row.AuthID, lastAuthID) || strings.EqualFold(id, lastAuthID))
		row.InUse = inUse && row.LastSelected
		switch {
		case row.Disabled:
			row.Reason = "Account disabled"
		case row.Unavailable:
			row.Reason = "Unavailable"
		case row.LiveLimit && row.LiveLimitUntil != nil:
			row.Reason = liveLimitReason(*row.LiveLimitUntil, limitPlan)
		case row.Cooling:
			row.Reason = "In cooldown until " + retryAfter.UTC().Format(time.RFC3339)
		default:
			row.Reason = "Eligible"
		}
		if row.Eligible && !nextMarked {
			row.NextPick = true
			nextMarked = true
		}
		rows = append(rows, row)
	}
	markFillFirstNext(rows)
	return rows
}

func markFillFirstNext(rows []PoolRow) {
	maxPriority := -1
	for _, row := range rows {
		if row.Eligible && row.Priority > maxPriority {
			maxPriority = row.Priority
		}
	}
	if maxPriority < 0 {
		return
	}
	marked := false
	for i := range rows {
		if !rows[i].Eligible {
			continue
		}
		if rows[i].Priority == maxPriority && !marked {
			rows[i].FillFirstNext = true
			marked = true
		}
		if rows[i].Priority < maxPriority && rows[i].Reason == "Eligible" {
			rows[i].Reason = "Live-eligible; CPA fill-first skips this while a higher-priority pool account exists"
		}
	}
}

func isDisabledStatus(status string) bool {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "disabled", "statusdisabled":
		return true
	default:
		return false
	}
}

func findAllowlistedCandidate(candidates []pluginapi.SchedulerAuthCandidate, authID string) (pluginapi.SchedulerAuthCandidate, bool) {
	allowlist := map[string]struct{}{authID: {}}
	for _, candidate := range candidates {
		if candidateMatchesAllowlist(candidate, allowlist) {
			return candidate, true
		}
	}
	return pluginapi.SchedulerAuthCandidate{}, false
}

func accountPriority(candidate pluginapi.SchedulerAuthCandidate) int {
	if candidate.Priority != 0 {
		return candidate.Priority
	}
	raw := strings.TrimSpace(candidate.Attributes["account_priority"])
	if raw == "" {
		return 0
	}
	priority, err := strconv.Atoi(raw)
	if err != nil {
		return 0
	}
	return priority
}

func parseBoolAttr(raw string) bool {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "1", "t", "true", "yes":
		return true
	default:
		return false
	}
}

func cooldownUntil(raw string, now time.Time) (time.Time, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return time.Time{}, false
	}
	retryAfter, err := time.Parse(time.RFC3339Nano, raw)
	if err != nil {
		retryAfter, err = time.Parse(time.RFC3339, raw)
	}
	if err != nil || retryAfter.IsZero() || !retryAfter.After(now) {
		return time.Time{}, false
	}
	return retryAfter, true
}
