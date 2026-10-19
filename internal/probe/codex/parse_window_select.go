package codex

import (
	"strings"
	"time"

	"github.com/karlorz/cpa-expiring-first/internal/probe/reading"
	"github.com/karlorz/cpa-expiring-first/internal/probe/wire"
)

type effectiveWindow struct {
	resetAt              *time.Time
	remaining            int64
	windowType           WindowType
	windows              []reading.QuotaWindow
	longWindowResetAt    *time.Time
	shortWindowRemaining *int64
	shortWindowResetAt   *time.Time
	longWindowRemaining  *int64
}

type parsedWindow struct {
	resetAt   *time.Time
	remaining int64
}

// pickEffectiveWindow selects the window that decides when the credential is usable again: the
// weekly window for paid plans, the monthly window for free plans.
func pickEffectiveWindow(usage whamUsage, observedAt time.Time) (effectiveWindow, bool) {
	planType := inferPlanType(usage.PlanType)
	if planType == reading.PlanTypeFree {
		return pickFreeWindow(usage, observedAt)
	}
	if paidPlan(planType) {
		return pickPaidWindow(usage, observedAt)
	}
	return effectiveWindow{}, false
}

func pickPaidWindow(usage whamUsage, observedAt time.Time) (effectiveWindow, bool) {
	fiveHour, hasFiveHour := pickWindow(usage, observedAt, isFiveHourWindow)
	weekly, hasWeekly := pickWindow(usage, observedAt, isWeeklyWindow)
	if hasFiveHour && hasWeekly {
		windowsList := []reading.QuotaWindow{
			{Duration: 5 * time.Hour, Remaining: fiveHour.remaining, ResetAt: *fiveHour.resetAt},
			{Duration: 7 * 24 * time.Hour, Remaining: weekly.remaining, ResetAt: *weekly.resetAt},
		}
		short, shortReset := int64Ptr(fiveHour.remaining), fiveHour.resetAt
		long := int64Ptr(weekly.remaining)
		base := effectiveWindow{
			windows:              windowsList,
			longWindowResetAt:    weekly.resetAt,
			shortWindowRemaining: short,
			shortWindowResetAt:   shortReset,
			longWindowRemaining:  long,
		}
		switch {
		case weekly.remaining <= 0:
			base.resetAt, base.remaining, base.windowType = weekly.resetAt, 0, WindowWeekly
			return base, true
		case fiveHour.remaining <= 0:
			base.resetAt, base.remaining, base.windowType = fiveHour.resetAt, 0, WindowFiveHour
			return base, true
		default:
			base.resetAt, base.remaining, base.windowType = weekly.resetAt, weekly.remaining, WindowWeekly
			return base, true
		}
	}
	if hasWeekly {
		return effectiveWindow{
			resetAt:             weekly.resetAt,
			remaining:           weekly.remaining,
			windowType:          WindowWeekly,
			windows:             []reading.QuotaWindow{{Duration: 7 * 24 * time.Hour, Remaining: weekly.remaining, ResetAt: *weekly.resetAt}},
			longWindowResetAt:   weekly.resetAt,
			longWindowRemaining: int64Ptr(weekly.remaining),
		}, true
	}
	if hasFiveHour {
		return effectiveWindow{
			resetAt:              fiveHour.resetAt,
			remaining:            fiveHour.remaining,
			windowType:           WindowFiveHour,
			windows:              []reading.QuotaWindow{{Duration: 5 * time.Hour, Remaining: fiveHour.remaining, ResetAt: *fiveHour.resetAt}},
			shortWindowRemaining: int64Ptr(fiveHour.remaining),
			shortWindowResetAt:   fiveHour.resetAt,
		}, true
	}
	return effectiveWindow{}, false
}

func pickFreeWindow(usage whamUsage, observedAt time.Time) (effectiveWindow, bool) {
	monthly, okMonthly := pickWindow(usage, observedAt, isFreeLongWindow)
	if !okMonthly {
		return effectiveWindow{}, false
	}
	return effectiveWindow{
		resetAt:             monthly.resetAt,
		remaining:           monthly.remaining,
		windowType:          WindowMonthly,
		windows:             []reading.QuotaWindow{{Duration: 30 * 24 * time.Hour, Remaining: monthly.remaining, ResetAt: *monthly.resetAt}},
		longWindowResetAt:   monthly.resetAt,
		longWindowRemaining: int64Ptr(monthly.remaining),
	}, true
}

func pickWindow(usage whamUsage, observedAt time.Time, match func(whamWindow) bool) (parsedWindow, bool) {
	for _, window := range []whamWindow{usage.RateLimit.PrimaryWindow, usage.RateLimit.SecondaryWindow} {
		if !hasWindowData(window) || !match(window) {
			continue
		}
		resetAt := windowResetTime(observedAt, window)
		remaining, okRemaining := windowRemaining(window)
		if resetAt == nil || !okRemaining {
			return parsedWindow{}, false
		}
		return parsedWindow{resetAt: resetAt, remaining: remaining}, true
	}
	return parsedWindow{}, false
}

func isFiveHourWindow(window whamWindow) bool {
	if seconds, okSeconds := wire.ToInt64(window.LimitWindowSeconds); okSeconds && seconds == 5*60*60 {
		return true
	}
	for _, field := range windowMetadataStrings(window) {
		if strings.Contains(field, "5h") || strings.Contains(field, "5 h") || strings.Contains(field, "5 hour") || strings.Contains(field, "5 hr") {
			return true
		}
	}
	return false
}

func isWeeklyWindow(window whamWindow) bool {
	if seconds, okSeconds := wire.ToInt64(window.LimitWindowSeconds); okSeconds && seconds == 7*24*60*60 {
		return true
	}
	for _, field := range windowMetadataStrings(window) {
		if strings.Contains(field, "weekly") || strings.Contains(field, "week") || strings.Contains(field, "7d") || strings.Contains(field, "7 days") || strings.Contains(field, "7 day") {
			return true
		}
	}
	return false
}

func isMonthlyWindow(window whamWindow) bool {
	if seconds, okSeconds := wire.ToInt64(window.LimitWindowSeconds); okSeconds && seconds >= 28*24*60*60 && seconds <= 31*24*60*60 {
		return true
	}
	for _, field := range windowMetadataStrings(window) {
		if strings.Contains(field, "monthly") || strings.Contains(field, "month") || strings.Contains(field, "30d") || strings.Contains(field, "30 days") || strings.Contains(field, "30 day") {
			return true
		}
	}
	return false
}

func isFreeLongWindow(window whamWindow) bool {
	if isFiveHourWindow(window) {
		return false
	}
	if seconds, okSeconds := wire.ToInt64(window.LimitWindowSeconds); okSeconds && seconds >= 24*60*60 {
		return true
	}
	return isMonthlyWindow(window) || isWeeklyWindow(window)
}
