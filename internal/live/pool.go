package live

import (
	"strconv"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

// PoolRow is one live_auth_ids credential as shown on the status page.
type PoolRow struct {
	Order        int    `json:"order"`
	AuthID       string `json:"auth_id"`
	Present      bool   `json:"present"`
	Disabled     bool   `json:"disabled"`
	Unavailable  bool   `json:"unavailable"`
	Cooling      bool   `json:"cooling"`
	Eligible     bool   `json:"eligible"`
	NextPick     bool   `json:"next_pick"`
	LastSelected bool   `json:"last_selected"`
	Priority     int    `json:"account_priority"`
	Reason       string `json:"reason"`
}

// DescribePool returns live_auth_ids in configured fill-first order, joined with host candidate
// state. Weekly/five-hour text quota is not Live remaining and is not included here.
func DescribePool(config Config, candidates []pluginapi.SchedulerAuthCandidate, lastAuthID string, now time.Time) []PoolRow {
	rows := make([]PoolRow, 0, len(config.AuthIDs))
	lastAuthID = strings.TrimSpace(lastAuthID)
	nextMarked := false
	for i, configured := range config.AuthIDs {
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
		row.Disabled = parseBoolAttr(candidate.Attributes["disabled"])
		row.Unavailable = parseBoolAttr(candidate.Attributes["unavailable"])
		retryAfter, cooling := cooldownUntil(candidate.Attributes["next_retry_after"], now)
		row.Cooling = cooling
		row.Eligible = !row.Disabled && !row.Unavailable && !row.Cooling
		row.LastSelected = lastAuthID != "" && (strings.EqualFold(row.AuthID, lastAuthID) || strings.EqualFold(id, lastAuthID))
		switch {
		case row.Disabled:
			row.Reason = "Account disabled"
		case row.Unavailable:
			row.Reason = "Unavailable"
		case row.Cooling:
			row.Reason = "In cooldown until " + retryAfter.Format(time.RFC3339)
		default:
			row.Reason = "Eligible"
		}
		if row.Eligible && !nextMarked {
			row.NextPick = true
			nextMarked = true
		}
		rows = append(rows, row)
	}
	return rows
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
