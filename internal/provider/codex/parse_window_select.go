package codex

import (
	"strings"
	"time"

	"quota-pacer/internal/core"
)

type effectiveWindow struct {
	resetAt              *time.Time
	remaining            int64
	windowType           WindowType
	windows              []core.QuotaWindow
	longWindowResetAt    *time.Time
	shortWindowRemaining *int64
	shortWindowResetAt   *time.Time
	longWindowRemaining  *int64
}

type parsedWindow struct {
	resetAt   *time.Time
	remaining int64
}

func pickEffectiveWindow(usage whamUsage, observedAt time.Time) (effectiveWindow, bool) {
	planType := inferPlanType(usage.PlanType)
	if planType == core.PlanTypeFree {
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
	// fiveHour + weekly 同时存在：与 Claude/Antigravity 宏观额度口径对齐，以 weekly 为主窗口，
	// 并完整填充 Windows 切片供 PacingScore 多窗口取 min 瓶颈。
	if hasFiveHour && hasWeekly {
		windowsList := []core.QuotaWindow{
			{Name: "5h", Duration: 5 * time.Hour, Remaining: fiveHour.remaining, ResetAt: *fiveHour.resetAt},
			{Name: "weekly", Duration: 7 * 24 * time.Hour, Remaining: weekly.remaining, ResetAt: *weekly.resetAt},
		}
		if weekly.remaining <= 0 {
			return effectiveWindow{
				resetAt:              weekly.resetAt,
				remaining:            0,
				windowType:           WindowWeekly,
				windows:              windowsList,
				longWindowResetAt:    weekly.resetAt,
				shortWindowRemaining: int64Ptr(fiveHour.remaining),
				shortWindowResetAt:   fiveHour.resetAt,
				longWindowRemaining:  int64Ptr(weekly.remaining),
			}, true
		}
		if fiveHour.remaining <= 0 {
			return effectiveWindow{
				resetAt:              fiveHour.resetAt,
				remaining:            0,
				windowType:           WindowFiveHour,
				windows:              windowsList,
				longWindowResetAt:    weekly.resetAt,
				shortWindowRemaining: int64Ptr(fiveHour.remaining),
				shortWindowResetAt:   fiveHour.resetAt,
				longWindowRemaining:  int64Ptr(weekly.remaining),
			}, true
		}
		return effectiveWindow{
			resetAt:              weekly.resetAt,
			remaining:            weekly.remaining,
			windowType:           WindowWeekly,
			windows:              windowsList,
			longWindowResetAt:    weekly.resetAt,
			shortWindowRemaining: int64Ptr(fiveHour.remaining),
			shortWindowResetAt:   fiveHour.resetAt,
			longWindowRemaining:  int64Ptr(weekly.remaining),
		}, true
	}
	// 仅有 weekly 付费窗口
	if hasWeekly {
		windowsList := []core.QuotaWindow{
			{Name: "weekly", Duration: 7 * 24 * time.Hour, Remaining: weekly.remaining, ResetAt: *weekly.resetAt},
		}
		return effectiveWindow{
			resetAt:             weekly.resetAt,
			remaining:           weekly.remaining,
			windowType:          WindowWeekly,
			windows:             windowsList,
			longWindowResetAt:   weekly.resetAt,
			longWindowRemaining: int64Ptr(weekly.remaining),
		}, true
	}
	// 仅有 fiveHour 付费窗口
	if hasFiveHour {
		windowsList := []core.QuotaWindow{
			{Name: "5h", Duration: 5 * time.Hour, Remaining: fiveHour.remaining, ResetAt: *fiveHour.resetAt},
		}
		return effectiveWindow{
			resetAt:              fiveHour.resetAt,
			remaining:            fiveHour.remaining,
			windowType:           WindowFiveHour,
			windows:              windowsList,
			shortWindowRemaining: int64Ptr(fiveHour.remaining),
			shortWindowResetAt:   fiveHour.resetAt,
		}, true
	}
	return effectiveWindow{}, false
}

func pickFreeWindow(usage whamUsage, observedAt time.Time) (effectiveWindow, bool) {
	monthly, ok := pickWindow(usage, observedAt, isFreeLongWindow)
	if !ok {
		return effectiveWindow{}, false
	}
	windowsList := []core.QuotaWindow{
		{Name: "monthly", Duration: 30 * 24 * time.Hour, Remaining: monthly.remaining, ResetAt: *monthly.resetAt},
	}
	return effectiveWindow{
		resetAt:             monthly.resetAt,
		remaining:           monthly.remaining,
		windowType:          WindowMonthly,
		windows:             windowsList,
		longWindowResetAt:   monthly.resetAt,
		longWindowRemaining: int64Ptr(monthly.remaining),
	}, true
}

func pickWindow(usage whamUsage, observedAt time.Time, match func(whamWindow) bool) (parsedWindow, bool) {
	windows := []whamWindow{usage.RateLimit.PrimaryWindow, usage.RateLimit.SecondaryWindow}
	for _, window := range windows {
		if !hasWindowData(window) || !match(window) {
			continue
		}
		resetAt := windowResetTime(observedAt, window)
		remaining, ok := windowRemaining(window)
		if resetAt == nil || !ok {
			return parsedWindow{}, false
		}
		return parsedWindow{resetAt: resetAt, remaining: remaining}, true
	}
	return parsedWindow{}, false
}

func isFiveHourWindow(window whamWindow) bool {
	if seconds, ok := toInt64(window.LimitWindowSeconds); ok && seconds == 5*60*60 {
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
	if seconds, ok := toInt64(window.LimitWindowSeconds); ok && seconds == 7*24*60*60 {
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
	if seconds, ok := toInt64(window.LimitWindowSeconds); ok && seconds >= 28*24*60*60 && seconds <= 31*24*60*60 {
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
	if seconds, ok := toInt64(window.LimitWindowSeconds); ok && seconds >= 24*60*60 {
		return true
	}
	return isMonthlyWindow(window) || isWeeklyWindow(window)
}
