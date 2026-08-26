package claude

import (
	"time"

	"quota-pacer/internal/core"
)

// DefaultAPIBaseURL 是 Anthropic 官方 API 根路径。
const DefaultAPIBaseURL = "https://api.anthropic.com"

// DefaultWebBaseURL 是 Claude 官方 Web API 根路径（备选）。
const DefaultWebBaseURL = "https://claude.ai/api"

// WindowType 标识 Claude 额度响应中的窗口类型。
type WindowType string

const (
	// WindowUnknown 表示未识别到明确额度窗口。
	WindowUnknown WindowType = "unknown"
	// WindowFiveHour 表示 5 小时滑动/会话限额窗口。
	WindowFiveHour WindowType = "5h"
	// WindowDaily 表示日限额窗口。
	WindowDaily WindowType = "daily"
	// WindowWeekly 表示周限额窗口。
	WindowWeekly WindowType = "weekly"
	// WindowSession 表示会话限额窗口。
	WindowSession WindowType = "session"
)

// Status 标识一次 Claude fresh probe 的可用性结论。
type Status string

const (
	// StatusReady 表示 fresh probe 产出了可用于排序的额度信号。
	StatusReady Status = "ready"
	// StatusProbeFailed 表示 probe 未产出可信额度信号。
	StatusProbeFailed Status = "probe_failed"
	// StatusDepleted 表示额度已耗尽。
	StatusDepleted Status = "depleted"
	// StatusCooldown 表示处于冷却期。
	StatusCooldown Status = "cooldown"
)

// ProbeResult 是 Claude fresh probe 的安全输出。
type ProbeResult struct {
	Provider          core.Provider
	AuthIndex         string
	ObservedAt        time.Time
	ResetAt           *time.Time
	Remaining         *int64
	Window            WindowType
	LongWindowResetAt *time.Time
	Freshness         core.Freshness
	ProbeStatus       core.ProbeStatus
	Status            Status
	PlanType          core.PlanType
	OrganizationUUID  string
	Error             string
}

// ProbeRequest 是执行 Claude fresh probe 所需的凭据上下文。
type ProbeRequest struct {
	Provider         core.Provider
	AuthIndex        string
	AccessToken      string
	OrganizationUUID string
	BaseURL          string
}

type clock interface {
	Now() time.Time
}

type realClock struct{}

func (realClock) Now() time.Time {
	return time.Now().UTC()
}
