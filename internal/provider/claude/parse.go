package claude

import (
	"bytes"
	"encoding/json"
	"math"
	"strconv"
	"strings"
	"time"

	"quota-pacer/internal/core"
	"quota-pacer/internal/host"
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

// ParseClaudeUsage 将 Claude 响应 JSON 与 Headers 解析为可信额度 fresh probe 结果。
func ParseClaudeUsage(raw []byte, headers host.Header, observedAt time.Time) ProbeResult {
	orgUUIDFromHeader := extractOrgFromHeaders(headers)

	// 0. 优先从 Anthropic Unified Rate Limit 响应头中解析高精配额 (5h & 7d utilization & reset)
	if res, ok := parseUnifiedRateLimitHeaders(headers, observedAt); ok {
		if res.OrganizationUUID == "" && orgUUIDFromHeader != "" {
			res.OrganizationUUID = orgUUIDFromHeader
		}
		return res
	}

	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return failedResult(observedAt, "empty claude usage response")
	}

	// 1. 数组形态：可能为 /organizations 列表
	if trimmed[0] == '[' {
		var orgList []claudeUsageResponse
		if err := json.Unmarshal(trimmed, &orgList); err == nil && len(orgList) > 0 {
			for _, org := range orgList {
				if res, ok := parseSingleUsage(org, observedAt); ok {
					if org.UUID != "" {
						res.OrganizationUUID = org.UUID
					} else if org.OrganizationUUID != "" {
						res.OrganizationUUID = org.OrganizationUUID
					} else if orgUUIDFromHeader != "" {
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
	if err := decoder.Decode(&usage); err != nil {
		return failedResult(observedAt, "parse claude usage failed")
	}

	// 2. 尝试从标准 usage/rate_limit 字段解析
	if res, ok := parseSingleUsage(usage, observedAt); ok {
		if res.OrganizationUUID == "" && orgUUIDFromHeader != "" {
			res.OrganizationUUID = orgUUIDFromHeader
		}
		return res
	}

	// 3. 检查是否为 /v1/models 响应（官方 API 凭据有效性证据）
	if len(usage.Data) > 0 {
		// 默认 7 天（周窗口）后刷新，假定额度可用（100）
		defaultReset := observedAt.UTC().Add(7 * 24 * time.Hour)
		planType := core.PlanTypePro
		orgUUID := usage.UUID
		if orgUUID == "" {
			orgUUID = usage.OrganizationUUID
		}
		if orgUUID == "" {
			orgUUID = orgUUIDFromHeader
		}
		return makeReadyResult(observedAt, &defaultReset, 100, WindowWeekly, &defaultReset, planType, orgUUID, nil, nil, nil)
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
		if win, ok := pickFromRateLimits(*usage.RateLimits, observedAt); ok {
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
		if win, ok := pickFromRateLimits(limits, observedAt); ok {
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
		if candidate.win != nil && hasWindowData(*candidate.win) {
			resetAt := windowResetTime(observedAt, *candidate.win)
			remaining, ok := windowRemaining(*candidate.win)
			if resetAt != nil && ok {
				var longReset *time.Time
				if usage.Weekly != nil && hasWindowData(*usage.Weekly) {
					longReset = windowResetTime(observedAt, *usage.Weekly)
				}
				return makeReadyResult(observedAt, resetAt, remaining, candidate.wtyp, longReset, planType, orgUUID, nil, nil, nil), true
			}
		}
	}

	return ProbeResult{}, false
}

func makeReadyResult(observedAt time.Time, resetAt *time.Time, remaining int64, winType WindowType, longReset *time.Time, planType core.PlanType, orgUUID string, shortRem *int64, shortReset *time.Time, longRem *int64) ProbeResult {
	var windows []core.QuotaWindow
	if shortRem != nil && shortReset != nil {
		windows = append(windows, core.QuotaWindow{Name: "5h", Duration: 5 * time.Hour, Remaining: *shortRem, ResetAt: *shortReset})
	}
	if longRem != nil && longReset != nil {
		windows = append(windows, core.QuotaWindow{Name: "weekly", Duration: 7 * 24 * time.Hour, Remaining: *longRem, ResetAt: *longReset})
	} else if winType == WindowWeekly && resetAt != nil {
		if shortReset == nil || !shortReset.Equal(*resetAt) {
			windows = append(windows, core.QuotaWindow{Name: "weekly", Duration: 7 * 24 * time.Hour, Remaining: remaining, ResetAt: *resetAt})
		}
	} else if winType == WindowFiveHour && resetAt != nil {
		if shortReset == nil {
			windows = append(windows, core.QuotaWindow{Name: "5h", Duration: 5 * time.Hour, Remaining: remaining, ResetAt: *resetAt})
		}
	} else if winType == WindowDaily && resetAt != nil {
		windows = append(windows, core.QuotaWindow{Name: "daily", Duration: 24 * time.Hour, Remaining: remaining, ResetAt: *resetAt})
	}
	return ProbeResult{
		Provider:             core.ProviderClaude,
		ObservedAt:           observedAt.UTC(),
		ResetAt:              resetAt,
		Remaining:            int64Ptr(remaining),
		Window:               winType,
		Windows:              windows,
		LongWindowResetAt:    longReset,
		ShortWindowRemaining: shortRem,
		ShortWindowResetAt:   shortReset,
		LongWindowRemaining:  longRem,
		Freshness:            core.FreshnessFresh,
		ProbeStatus:          core.ProbeStatusReady,
		Status:               StatusReady,
		PlanType:             planType,
		OrganizationUUID:     orgUUID,
	}
}

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
		if weeklyRem <= 0 {
			return effectiveWindow{
				resetAt:              weeklyReset,
				remaining:            0,
				windowType:           WindowWeekly,
				longWindowResetAt:    weeklyReset,
				shortWindowRemaining: int64Ptr(fiveHourRem),
				shortWindowResetAt:   fiveHourReset,
				longWindowRemaining:  int64Ptr(weeklyRem),
			}, true
		}
		if fiveHourRem <= 0 {
			return effectiveWindow{
				resetAt:              fiveHourReset,
				remaining:            0,
				windowType:           WindowFiveHour,
				longWindowResetAt:    weeklyReset,
				shortWindowRemaining: int64Ptr(fiveHourRem),
				shortWindowResetAt:   fiveHourReset,
				longWindowRemaining:  int64Ptr(weeklyRem),
			}, true
		}
		return effectiveWindow{
			resetAt:              weeklyReset,
			remaining:            weeklyRem,
			windowType:           WindowWeekly,
			longWindowResetAt:    weeklyReset,
			shortWindowRemaining: int64Ptr(fiveHourRem),
			shortWindowResetAt:   fiveHourReset,
			longWindowRemaining:  int64Ptr(weeklyRem),
		}, true
	}
	if okWeek && weeklyReset != nil {
		return effectiveWindow{resetAt: weeklyReset, remaining: weeklyRem, windowType: WindowWeekly, longWindowResetAt: weeklyReset}, true
	}
	if okFive && fiveHourReset != nil {
		return effectiveWindow{resetAt: fiveHourReset, remaining: fiveHourRem, windowType: WindowFiveHour}, true
	}

	return effectiveWindow{}, false
}

// ParseClaudeRateLimitError 解析 HTTP 429 或 rate limit error 正文与响应头。
func ParseClaudeRateLimitError(raw []byte, headers host.Header, observedAt time.Time) ProbeResult {
	orgUUIDFromHeader := extractOrgFromHeaders(headers)

	// 0. 优先从 Anthropic Unified Rate Limit 响应头中解析精确重置信息
	if res, ok := parseUnifiedRateLimitHeaders(headers, observedAt); ok {
		res.Remaining = int64Ptr(0)
		res.Error = "rate limit reached"
		if res.OrganizationUUID == "" && orgUUIDFromHeader != "" {
			res.OrganizationUUID = orgUUIDFromHeader
		}
		return res
	}

	var resetAt *time.Time
	planType := core.PlanTypePro
	zeroRemaining := int64(0)

	if len(raw) > 0 {
		var generic map[string]any
		if err := json.Unmarshal(raw, &generic); err == nil {
			resetAt = extractResetFromGenericMap(generic, observedAt)
			if pt := inferPlanFromGenericMap(generic); pt != core.PlanTypeUnknown {
				planType = pt
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

	return ProbeResult{
		Provider:         core.ProviderClaude,
		AuthIndex:        "",
		ObservedAt:       observedAt.UTC(),
		ResetAt:          resetAt,
		Remaining:        &zeroRemaining,
		Window:           WindowFiveHour,
		Freshness:        core.FreshnessFresh,
		ProbeStatus:      core.ProbeStatusReady,
		Status:           StatusReady,
		PlanType:         planType,
		OrganizationUUID: orgUUIDFromHeader,
		Error:            "rate limit reached",
	}
}

func parseUnifiedRateLimitHeaders(headers host.Header, observedAt time.Time) (ProbeResult, bool) {
	if headers == nil {
		return ProbeResult{}, false
	}

	orgUUID := extractOrgFromHeaders(headers)

	util7dStr := getHeaderCaseInsensitive(headers, "anthropic-ratelimit-unified-7d-utilization")
	reset7dStr := getHeaderCaseInsensitive(headers, "anthropic-ratelimit-unified-7d-reset")
	status7d := strings.ToLower(strings.TrimSpace(getHeaderCaseInsensitive(headers, "anthropic-ratelimit-unified-7d-status")))

	util5hStr := getHeaderCaseInsensitive(headers, "anthropic-ratelimit-unified-5h-utilization")
	reset5hStr := getHeaderCaseInsensitive(headers, "anthropic-ratelimit-unified-5h-reset")
	status5h := strings.ToLower(strings.TrimSpace(getHeaderCaseInsensitive(headers, "anthropic-ratelimit-unified-5h-status")))

	unifiedStatus := strings.ToLower(strings.TrimSpace(getHeaderCaseInsensitive(headers, "anthropic-ratelimit-unified-status")))
	utilUnifiedStr := getHeaderCaseInsensitive(headers, "anthropic-ratelimit-unified-utilization")
	resetUnifiedStr := getHeaderCaseInsensitive(headers, "anthropic-ratelimit-unified-reset")

	hasUnified := util7dStr != "" || reset7dStr != "" || status7d != "" ||
		util5hStr != "" || reset5hStr != "" || status5h != "" ||
		unifiedStatus != "" || utilUnifiedStr != "" || resetUnifiedStr != ""

	if !hasUnified {
		return ProbeResult{}, false
	}

	var reset7d *time.Time
	if reset7dStr != "" {
		if t, ok := parseTimeString(reset7dStr); ok {
			reset7d = t
		}
	}

	var reset5h *time.Time
	if reset5hStr != "" {
		if t, ok := parseTimeString(reset5hStr); ok {
			reset5h = t
		}
	}

	var resetUnified *time.Time
	if resetUnifiedStr != "" {
		if t, ok := parseTimeString(resetUnifiedStr); ok {
			resetUnified = t
		}
	}

	var util7d float64
	var hasUtil7d bool
	if util7dStr != "" {
		if u, err := strconv.ParseFloat(util7dStr, 64); err == nil {
			util7d = u
			hasUtil7d = true
		}
	}

	var util5h float64
	var hasUtil5h bool
	if util5hStr != "" {
		if u, err := strconv.ParseFloat(util5hStr, 64); err == nil {
			util5h = u
			hasUtil5h = true
		}
	}

	var utilUnified float64
	var hasUtilUnified bool
	if utilUnifiedStr != "" {
		if u, err := strconv.ParseFloat(utilUnifiedStr, 64); err == nil {
			utilUnified = u
			hasUtilUnified = true
		}
	}

	planType := core.PlanTypePro

	// 1. 显式限流或额度已用尽 (utilization >= 1.0 或 status == rejected)
	if status7d == "rejected" || (hasUtil7d && util7d >= 1.0) {
		resetAt := reset7d
		if resetAt == nil {
			resetAt = resetUnified
		}
		if resetAt == nil {
			t := observedAt.UTC().Add(7 * 24 * time.Hour)
			resetAt = &t
		}
		return makeReadyResult(observedAt, resetAt, 0, WindowWeekly, reset7d, planType, orgUUID, nil, nil, nil), true
	}

	if status5h == "rejected" || (hasUtil5h && util5h >= 1.0) {
		resetAt := reset5h
		if resetAt == nil {
			resetAt = resetUnified
		}
		if resetAt == nil {
			t := observedAt.UTC().Add(5 * time.Hour)
			resetAt = &t
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
			t := observedAt.UTC().Add(5 * time.Hour)
			resetAt = &t
		}
		return makeReadyResult(observedAt, resetAt, 0, WindowWeekly, reset7d, planType, orgUUID, nil, nil, nil), true
	}

	// 2. 同时存在 7d 与 5h utilization：7d 决定账户总体周剩余额度，5h 为短期窗口
	if hasUtil7d && hasUtil5h {
		remaining7d := int64(math.Max(0, math.Min(100, math.Round((1.0-util7d)*100))))
		remaining5h := int64(math.Max(0, math.Min(100, math.Round((1.0-util5h)*100))))

		if remaining7d <= 0 {
			resetAt := reset7d
			if resetAt == nil {
				t := observedAt.UTC().Add(7 * 24 * time.Hour)
				resetAt = &t
			}
			return makeReadyResult(observedAt, resetAt, 0, WindowWeekly, reset7d, planType, orgUUID, int64Ptr(remaining5h), reset5h, int64Ptr(remaining7d)), true
		}
		if remaining5h <= 0 {
			resetAt := reset5h
			if resetAt == nil {
				t := observedAt.UTC().Add(5 * time.Hour)
				resetAt = &t
			}
			return makeReadyResult(observedAt, resetAt, 0, WindowFiveHour, reset7d, planType, orgUUID, int64Ptr(remaining5h), reset5h, int64Ptr(remaining7d)), true
		}

		resetAt := reset7d
		if resetAt == nil {
			t := observedAt.UTC().Add(7 * 24 * time.Hour)
			resetAt = &t
		}
		return makeReadyResult(observedAt, resetAt, remaining7d, WindowWeekly, reset7d, planType, orgUUID, int64Ptr(remaining5h), reset5h, int64Ptr(remaining7d)), true
	}

	// 3. 仅存在 7d utilization
	if hasUtil7d {
		remaining7d := int64(math.Max(0, math.Min(100, math.Round((1.0-util7d)*100))))
		resetAt := reset7d
		if resetAt == nil {
			t := observedAt.UTC().Add(7 * 24 * time.Hour)
			resetAt = &t
		}
		return makeReadyResult(observedAt, resetAt, remaining7d, WindowWeekly, reset7d, planType, orgUUID, nil, nil, nil), true
	}

	// 4. 仅存在 5h utilization
	if hasUtil5h {
		remaining5h := int64(math.Max(0, math.Min(100, math.Round((1.0-util5h)*100))))
		resetAt := reset5h
		if resetAt == nil {
			t := observedAt.UTC().Add(5 * time.Hour)
			resetAt = &t
		}
		return makeReadyResult(observedAt, resetAt, remaining5h, WindowFiveHour, reset7d, planType, orgUUID, nil, nil, nil), true
	}

	// 5. 通用 unified utilization
	if hasUtilUnified {
		remaining := int64(math.Max(0, math.Min(100, math.Round((1.0-utilUnified)*100))))
		resetAt := resetUnified
		if resetAt == nil {
			t := observedAt.UTC().Add(7 * 24 * time.Hour)
			resetAt = &t
		}
		return makeReadyResult(observedAt, resetAt, remaining, WindowWeekly, resetUnified, planType, orgUUID, nil, nil, nil), true
	}

	// 6. 仅有 allowed 状态响应头
	if unifiedStatus == "allowed" || status7d == "allowed" || status5h == "allowed" {
		resetAt := reset7d
		if resetAt == nil {
			resetAt = reset5h
		}
		if resetAt == nil {
			resetAt = resetUnified
		}
		if resetAt == nil {
			t := observedAt.UTC().Add(7 * 24 * time.Hour)
			resetAt = &t
		}
		return makeReadyResult(observedAt, resetAt, 100, WindowWeekly, reset7d, planType, orgUUID, nil, nil, nil), true
	}

	return ProbeResult{}, false
}

func getHeaderCaseInsensitive(h host.Header, target string) string {
	if h == nil {
		return ""
	}
	for k, v := range h {
		if strings.EqualFold(strings.TrimSpace(k), target) {
			for _, val := range v {
				if trimmed := strings.TrimSpace(val); trimmed != "" {
					return trimmed
				}
			}
		}
	}
	return ""
}

func extractOrgFromHeaders(headers host.Header) string {
	if headers == nil {
		return ""
	}
	return getHeaderCaseInsensitive(headers, "anthropic-organization-id")
}

func extractResetFromGenericMap(m map[string]any, observedAt time.Time) *time.Time {
	for _, key := range []string{"resets_at", "resetsAt", "reset_at", "reset_time", "reset_after_seconds"} {
		if v, ok := m[key]; ok {
			if t, okTime := parseAnyTime(v); okTime {
				return t
			}
			if secs, okSecs := toInt64(v); okSecs && secs > 0 {
				t := observedAt.UTC().Add(time.Duration(secs) * time.Second)
				return &t
			}
		}
	}
	for _, nestedKey := range []string{"error", "rate_limit", "rate_limits", "detail"} {
		if nested, ok := m[nestedKey].(map[string]any); ok {
			if t := extractResetFromGenericMap(nested, observedAt); t != nil {
				return t
			}
		}
	}
	return nil
}

func extractResetFromHeaders(headers host.Header, observedAt time.Time) *time.Time {
	for key, values := range headers {
		lowerKey := strings.ToLower(strings.TrimSpace(key))
		for _, val := range values {
			trimmed := strings.TrimSpace(val)
			if trimmed == "" {
				continue
			}
			switch lowerKey {
			case "anthropic-ratelimit-unified-7d-reset", "anthropic-ratelimit-unified-5h-reset", "anthropic-ratelimit-unified-reset",
				"anthropic-ratelimit-requests-reset", "anthropic-ratelimit-tokens-reset", "x-ratelimit-reset":
				if t, ok := parseTimeString(trimmed); ok {
					return t
				}
			case "retry-after":
				if secs, err := strconv.ParseInt(trimmed, 10, 64); err == nil && secs > 0 {
					t := observedAt.UTC().Add(time.Duration(secs) * time.Second)
					return &t
				}
				if t, ok := parseTimeString(trimmed); ok {
					return t
				}
			}
		}
	}
	return nil
}

func inferPlanFromGenericMap(m map[string]any) core.PlanType {
	for _, key := range []string{"plan_type", "plan", "tier", "subscription"} {
		if v, ok := m[key]; ok {
			if s, okStr := toString(v); okStr {
				if pt := inferPlanType(s); pt != core.PlanTypeUnknown {
					return pt
				}
			}
		}
	}
	if caps, ok := m["capabilities"].([]any); ok {
		for _, c := range caps {
			if s, okStr := toString(c); okStr {
				if pt := inferPlanType(s); pt != core.PlanTypeUnknown {
					return pt
				}
			}
		}
	}
	return core.PlanTypeUnknown
}

func hasWindowData(window claudeWindow) bool {
	if _, ok := parseAnyTime(window.ResetsAt); ok {
		return true
	}
	if _, ok := parseAnyTime(window.ResetTime); ok {
		return true
	}
	if seconds, ok := toInt64(window.ResetAfterSeconds); ok && seconds > 0 {
		return true
	}
	if _, ok := toFloat64(window.Remaining); ok {
		return true
	}
	if _, ok := toFloat64(window.RemainingQueries); ok {
		return true
	}
	if _, ok := toFloat64(window.Limit); ok {
		return true
	}
	if _, ok := toFloat64(window.Used); ok {
		return true
	}
	if _, ok := toFloat64(window.UsedPercent); ok {
		return true
	}
	if reached, ok := toBool(window.LimitReached); ok && reached {
		return true
	}
	return false
}

func windowResetTime(observedAt time.Time, window claudeWindow) *time.Time {
	for _, candidate := range []any{window.ResetsAt, window.ResetTime} {
		if resetAt, ok := parseAnyTime(candidate); ok {
			return resetAt
		}
	}
	if seconds, ok := toInt64(window.ResetAfterSeconds); ok && seconds > 0 {
		resetAt := observedAt.UTC().Add(time.Duration(seconds) * time.Second)
		return &resetAt
	}
	return nil
}

func windowRemaining(window claudeWindow) (int64, bool) {
	if remaining, ok := toFloat64(window.Remaining); ok {
		return nonNegativeCeil(remaining), true
	}
	if remainingQueries, ok := toFloat64(window.RemainingQueries); ok {
		return nonNegativeCeil(remainingQueries), true
	}
	limit, okLimit := toFloat64(window.Limit)
	used, okUsed := toFloat64(window.Used)
	if okLimit && okUsed {
		return nonNegativeCeil(limit - used), true
	}
	if reached, ok := toBool(window.LimitReached); ok && reached {
		return 0, true
	}
	if usedPercent, ok := toFloat64(window.UsedPercent); ok {
		return nonNegativeCeil(100 - usedPercent), true
	}
	return 0, false
}

func inferClaudePlanType(usage claudeUsageResponse) core.PlanType {
	for _, capStr := range usage.Capabilities {
		lower := strings.ToLower(strings.TrimSpace(capStr))
		if strings.Contains(lower, "pro") {
			return core.PlanTypePro
		}
		if strings.Contains(lower, "team") || strings.Contains(lower, "enterprise") {
			return core.PlanTypeTeam
		}
	}
	for _, raw := range []string{usage.PlanType, usage.Tier} {
		if pt := inferPlanType(raw); pt != core.PlanTypeUnknown {
			return pt
		}
	}
	return core.PlanTypeUnknown
}

func inferPlanType(value string) core.PlanType {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "free", "claude_free", "none":
		return core.PlanTypeFree
	case "plus":
		return core.PlanTypePlus
	case "pro", "claude_pro", "claude-pro":
		return core.PlanTypePro
	case "team", "claude_team", "enterprise":
		return core.PlanTypeTeam
	default:
		return core.PlanTypeUnknown
	}
}

func failedResult(observedAt time.Time, message string) ProbeResult {
	return ProbeResult{
		Provider:    core.ProviderClaude,
		ObservedAt:  observedAt.UTC(),
		Window:      WindowUnknown,
		Freshness:   core.FreshnessUnknown,
		ProbeStatus: core.ProbeStatusUnknown,
		Status:      StatusProbeFailed,
		PlanType:    core.PlanTypeUnknown,
		Error:       message,
	}
}

func parseAnyTime(raw any) (*time.Time, bool) {
	switch value := raw.(type) {
	case nil:
		return nil, false
	case string:
		return parseTimeString(value)
	case float64:
		return parseUnix(int64(value))
	case json.Number:
		integer, err := value.Int64()
		if err != nil {
			return nil, false
		}
		return parseUnix(integer)
	default:
		return nil, false
	}
}

func parseTimeString(value string) (*time.Time, bool) {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return nil, false
	}
	if integer, err := strconv.ParseInt(trimmed, 10, 64); err == nil {
		return parseUnix(integer)
	}
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02 15:04:05"} {
		if parsed, err := time.Parse(layout, trimmed); err == nil {
			utc := parsed.UTC()
			return &utc, true
		}
	}
	return nil, false
}

func parseUnix(value int64) (*time.Time, bool) {
	if value <= 0 {
		return nil, false
	}
	if value > 1_000_000_000_000 {
		parsed := time.UnixMilli(value).UTC()
		return &parsed, true
	}
	parsed := time.Unix(value, 0).UTC()
	return &parsed, true
}

func toString(raw any) (string, bool) {
	if s, ok := raw.(string); ok {
		trimmed := strings.TrimSpace(s)
		return trimmed, trimmed != ""
	}
	return "", false
}

func toInt64(raw any) (int64, bool) {
	switch value := raw.(type) {
	case float64:
		return int64(value), true
	case json.Number:
		integer, err := value.Int64()
		return integer, err == nil
	case string:
		trimmed := strings.TrimSpace(value)
		if trimmed == "" {
			return 0, false
		}
		integer, err := strconv.ParseInt(trimmed, 10, 64)
		return integer, err == nil
	default:
		return 0, false
	}
}

func toFloat64(raw any) (float64, bool) {
	switch value := raw.(type) {
	case float64:
		return value, true
	case json.Number:
		floatValue, err := value.Float64()
		return floatValue, err == nil
	case string:
		trimmed := strings.TrimSpace(value)
		if trimmed == "" {
			return 0, false
		}
		floatValue, err := strconv.ParseFloat(trimmed, 64)
		return floatValue, err == nil
	default:
		return 0, false
	}
}

func toBool(raw any) (bool, bool) {
	switch value := raw.(type) {
	case bool:
		return value, true
	case string:
		parsed, err := strconv.ParseBool(strings.TrimSpace(value))
		return parsed, err == nil
	default:
		return false, false
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
