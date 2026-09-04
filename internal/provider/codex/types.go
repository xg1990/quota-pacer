package codex

import (
	"time"

	"quota-pacer/internal/core"
)

// WhamUsageURL 是 ChatGPT wham usage 的宿主代理目标地址。
const WhamUsageURL = "https://chatgpt.com/backend-api/wham/usage"

// WhamResetCreditsURL 是 ChatGPT banked rate-limit reset credits 的姊妹端点地址。
// 未公开文档化（逆向工程确认），承载用户可手动兑换的"重置额度"库存，与被动的
// primary/secondary_window 定时重置是完全独立的两套机制。
const WhamResetCreditsURL = "https://chatgpt.com/backend-api/wham/rate-limit-reset-credits"

// WindowType 标识 wham usage 响应中的额度窗口类型。
type WindowType string

const (
	// WindowUnknown 表示没有可用于排序的可信额度窗口。
	WindowUnknown WindowType = "unknown"
	// WindowFiveHour 表示已识别到 paid 5 小时额度窗口。
	WindowFiveHour WindowType = "5h"
	// WindowWeekly 表示已识别到 weekly quota 窗口。
	WindowWeekly WindowType = "weekly"
	// WindowMonthly 表示已识别到 free monthly quota 窗口。
	WindowMonthly WindowType = "monthly"
)

// Status 标识一次 Codex/ChatGPT fresh probe 的可用性结论。
type Status string

const (
	// StatusReady 表示 fresh probe 产出了可用于排序的周额度信号。
	StatusReady Status = "ready"
	// StatusProbeFailed 表示 probe 未产出可信周额度信号，排序应保持 unknown。
	StatusProbeFailed Status = "probe_failed"
)

// ProbeResult 是 Codex/ChatGPT wham usage fresh probe 的安全输出。
type ProbeResult struct {
	Provider             core.Provider
	AuthIndex            string
	ObservedAt           time.Time
	ResetAt              *time.Time
	Remaining            *int64
	Window               WindowType
	Windows              []core.QuotaWindow
	LongWindowResetAt    *time.Time
	ShortWindowRemaining *int64
	ShortWindowResetAt   *time.Time
	LongWindowRemaining  *int64
	Freshness            core.Freshness
	ProbeStatus          core.ProbeStatus
	Status               Status
	PlanType             core.PlanType
	// AvailableResetCredits: 当前状态为 available 的银行化重置额度数量（best-effort，姊妹端点探测失败时为 0）。
	AvailableResetCredits int
	// NearestResetCreditExpiresAt: 所有 available 额度中最近的过期时间；nil 表示无可用额度或未探测到。
	NearestResetCreditExpiresAt *time.Time
	Error                       string
}

// ProbeRequest 是执行 wham usage fresh probe 所需的宿主凭证上下文。
type ProbeRequest struct {
	Provider    core.Provider
	AuthIndex   string
	AccountID   string
	AccessToken string
}

type clock interface {
	Now() time.Time
}

type realClock struct{}

func (realClock) Now() time.Time {
	return time.Now().UTC()
}
