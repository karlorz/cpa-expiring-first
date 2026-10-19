package antigravity

import (
	"bytes"
	"encoding/json"
	"math"
	"strings"
	"time"

	"github.com/karlorz/cpa-expiring-first/internal/probe/reading"
	"github.com/karlorz/cpa-expiring-first/internal/probe/wire"
)

type availableModelsResponse struct {
	Models  map[string]availableModel `json:"models"`
	Buckets []quotaBucket             `json:"buckets"`
	Groups  []quotaGroup              `json:"groups"`
}

type quotaGroup struct {
	DisplayName string        `json:"displayName"`
	Description string        `json:"description"`
	Buckets     []quotaBucket `json:"buckets"`
}

type availableModel struct {
	ModelProvider string    `json:"modelProvider"`
	QuotaInfo     quotaInfo `json:"quotaInfo"`
}

type quotaInfo struct {
	RemainingFraction any           `json:"remainingFraction"`
	ResetTime         any           `json:"resetTime"`
	Windows           []quotaWindow `json:"windows"`
}

type quotaWindow struct {
	Name              string `json:"name"`
	RemainingFraction any    `json:"remainingFraction"`
	ResetTime         any    `json:"resetTime"`
}

type quotaBucket struct {
	ModelID           string `json:"modelId"`
	Window            string `json:"window"`
	RemainingFraction any    `json:"remainingFraction"`
	ResetTime         any    `json:"resetTime"`
}

type candidateWindow struct {
	resetAt   *time.Time
	remaining int64
	window    WindowType
}

// ParseAvailableModels decodes an Antigravity quota summary into evidence for the target model
// group. The endpoint reports fractions as 0..1 or 0..100 depending on deployment, and window
// names as free text, so both are normalized here.
func ParseAvailableModels(raw []byte, observedAt time.Time, group ModelGroup) ProbeResult {
	var response availableModelsResponse
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if errDecode := decoder.Decode(&response); errDecode != nil {
		return failedResult(observedAt, group, "parse antigravity quota failed")
	}
	windows := collectGroupWindows(response, group)
	selected, okWindow := pickEffectiveWindow(windows)
	if !okWindow {
		return failedResult(observedAt, group, "target model group quota unavailable")
	}
	result := ProbeResult{
		ModelGroup: group,
		ObservedAt: observedAt.UTC(),
		ResetAt:    selected.resetAt,
		Remaining:  int64Ptr(selected.remaining),
		Window:     selected.window,
		Status:     reading.StatusReady,
		PlanType:   inferPlanType(windows),
	}
	var quotaWindows []reading.QuotaWindow
	if fiveHour, okFive := firstWindow(windows, WindowFiveHour); okFive && fiveHour.resetAt != nil {
		result.ShortWindowRemaining = int64Ptr(fiveHour.remaining)
		result.ShortWindowResetAt = fiveHour.resetAt
		quotaWindows = append(quotaWindows, reading.QuotaWindow{Duration: 5 * time.Hour, Remaining: fiveHour.remaining, ResetAt: *fiveHour.resetAt})
	}
	if weekly, okWeekly := firstWindow(windows, WindowWeekly); okWeekly && weekly.resetAt != nil {
		result.LongWindowResetAt = weekly.resetAt
		result.LongWindowRemaining = int64Ptr(weekly.remaining)
		quotaWindows = append(quotaWindows, reading.QuotaWindow{Duration: 7 * 24 * time.Hour, Remaining: weekly.remaining, ResetAt: *weekly.resetAt})
	}
	if len(quotaWindows) == 0 && selected.resetAt != nil {
		duration := 7 * 24 * time.Hour
		if selected.window == WindowFiveHour {
			duration = 5 * time.Hour
		}
		quotaWindows = append(quotaWindows, reading.QuotaWindow{Duration: duration, Remaining: selected.remaining, ResetAt: *selected.resetAt})
	}
	result.Windows = quotaWindows
	return result
}

func collectGroupWindows(response availableModelsResponse, group ModelGroup) []candidateWindow {
	windows := make([]candidateWindow, 0)
	for modelID, model := range response.Models {
		if !modelBelongsToGroup(modelID, model.ModelProvider, group) {
			continue
		}
		if len(model.QuotaInfo.Windows) == 0 {
			if window, okWindow := quotaFieldsToWindow(model.QuotaInfo.RemainingFraction, model.QuotaInfo.ResetTime, WindowUnknown); okWindow {
				windows = append(windows, window)
			}
			continue
		}
		for _, item := range model.QuotaInfo.Windows {
			if window, okWindow := quotaFieldsToWindow(item.RemainingFraction, item.ResetTime, classifyWindow(item.Name)); okWindow {
				windows = append(windows, window)
			}
		}
	}
	for _, bucket := range response.Buckets {
		if !modelBelongsToGroup(bucket.ModelID, "", group) {
			continue
		}
		if window, okWindow := quotaFieldsToWindow(bucket.RemainingFraction, bucket.ResetTime, WindowUnknown); okWindow {
			windows = append(windows, window)
		}
	}
	for _, quotaGroup := range response.Groups {
		if !quotaGroupBelongsToModelGroup(quotaGroup, group) {
			continue
		}
		for _, bucket := range quotaGroup.Buckets {
			if window, okWindow := quotaFieldsToWindow(bucket.RemainingFraction, bucket.ResetTime, classifyWindow(bucket.Window)); okWindow {
				windows = append(windows, window)
			}
		}
	}
	return windows
}

func quotaGroupBelongsToModelGroup(quotaGroup quotaGroup, group ModelGroup) bool {
	text := strings.ToLower(strings.TrimSpace(quotaGroup.DisplayName + " " + quotaGroup.Description))
	if group == ModelGroupClaudeGPT {
		return strings.Contains(text, "claude") || strings.Contains(text, "gpt")
	}
	return strings.Contains(text, "gemini") && !strings.Contains(text, "claude") && !strings.Contains(text, "gpt")
}

// pickEffectiveWindow prefers the weekly window, except when a window is already used up: an
// exhausted window is the one that decides when the credential becomes usable again.
func pickEffectiveWindow(windows []candidateWindow) (candidateWindow, bool) {
	fiveHour, hasFiveHour := firstWindow(windows, WindowFiveHour)
	weekly, hasWeekly := firstWindow(windows, WindowWeekly)
	if hasFiveHour && hasWeekly {
		if weekly.remaining <= 0 {
			return weekly, true
		}
		if fiveHour.remaining <= 0 {
			return fiveHour, true
		}
		return weekly, true
	}
	if hasWeekly {
		return weekly, true
	}
	if hasFiveHour {
		return fiveHour, true
	}
	for _, window := range windows {
		return window, true
	}
	return candidateWindow{}, false
}

func firstWindow(windows []candidateWindow, windowType WindowType) (candidateWindow, bool) {
	for _, window := range windows {
		if window.window == windowType {
			return window, true
		}
	}
	return candidateWindow{}, false
}

func quotaFieldsToWindow(rawRemaining any, rawReset any, windowType WindowType) (candidateWindow, bool) {
	resetAt, okReset := wire.ParseAnyTime(rawReset)
	if !okReset {
		return candidateWindow{}, false
	}
	remaining, okRemaining := remainingPercent(rawRemaining)
	if !okRemaining {
		return candidateWindow{}, false
	}
	return candidateWindow{resetAt: resetAt, remaining: remaining, window: windowType}, true
}

func modelBelongsToGroup(modelID string, provider string, group ModelGroup) bool {
	text := strings.ToLower(strings.TrimSpace(modelID + " " + provider))
	if group == ModelGroupClaudeGPT {
		return strings.Contains(text, "claude") || strings.Contains(text, "gpt") || strings.Contains(text, "openai")
	}
	return strings.Contains(text, "gemini") && !strings.Contains(text, "claude") && !strings.Contains(text, "gpt")
}

func classifyWindow(name string) WindowType {
	text := strings.ToLower(strings.TrimSpace(name))
	if text == "" {
		return WindowUnknown
	}
	// Long windows first, so a mixed label like "5d 15h" is not read as a short window.
	if strings.Contains(text, "week") || strings.Contains(text, "7d") || hasDayToken(text) {
		return WindowWeekly
	}
	if hasFiveHourToken(text) {
		return WindowFiveHour
	}
	return WindowUnknown
}

// hasDayToken reports an Nd day unit, as in "5d", used by weekly labels.
func hasDayToken(text string) bool {
	for i := 0; i < len(text); i++ {
		if text[i] < '0' || text[i] > '9' {
			continue
		}
		j := i + 1
		for j < len(text) && text[j] >= '0' && text[j] <= '9' {
			j++
		}
		if j < len(text) && text[j] == 'd' && (j+1 == len(text) || text[j+1] < 'a' || text[j+1] > 'z') {
			return true
		}
		i = j
	}
	return false
}

// hasFiveHourToken reports a standalone five-hour token: 5h, 5hr or 5 hour(s). The number must
// be exactly 5 with no digit neighbours, so 15h and 25hr do not match.
func hasFiveHourToken(text string) bool {
	for i := 0; i < len(text); i++ {
		if text[i] < '0' || text[i] > '9' {
			continue
		}
		j := i + 1
		for j < len(text) && text[j] >= '0' && text[j] <= '9' {
			j++
		}
		if j-i != 1 || text[i] != '5' {
			i = j
			continue
		}
		k := j
		for k < len(text) && text[k] == ' ' {
			k++
		}
		if isHourUnitAt(text, k) {
			return true
		}
		i = j
	}
	return false
}

// isHourUnitAt matches an hour unit at offset, longest first.
func isHourUnitAt(text string, offset int) bool {
	if offset >= len(text) {
		return false
	}
	for _, unit := range []string{"hours", "hour", "hrs", "hr", "h"} {
		end := offset + len(unit)
		if end > len(text) || text[offset:end] != unit {
			continue
		}
		if end == len(text) || text[end] < 'a' || text[end] > 'z' {
			return true
		}
	}
	return false
}

func inferPlanType(windows []candidateWindow) reading.PlanType {
	if _, okFive := firstWindow(windows, WindowFiveHour); okFive {
		return reading.PlanTypePro
	}
	if _, okWeekly := firstWindow(windows, WindowWeekly); okWeekly {
		return reading.PlanTypeFree
	}
	return reading.PlanTypeUnknown
}

// remainingPercent normalizes a remaining fraction to a percentage. Values at or below 1 are
// fractions of a whole; larger values are already percentages.
func remainingPercent(raw any) (int64, bool) {
	value, okFloat := wire.ToFloat64(raw)
	if !okFloat {
		return 0, false
	}
	if value <= 1 {
		value *= 100
	}
	if value <= 0 {
		return 0, true
	}
	return int64(math.Ceil(value)), true
}

func failedResult(observedAt time.Time, group ModelGroup, message string) ProbeResult {
	return ProbeResult{
		ModelGroup: group,
		ObservedAt: observedAt.UTC(),
		Window:     WindowUnknown,
		Status:     reading.StatusProbeFailed,
		PlanType:   reading.PlanTypeUnknown,
		Error:      message,
	}
}

func int64Ptr(value int64) *int64 {
	return &value
}
