package codex

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"quota-pacer/internal/host"
)

type httpDoer interface {
	HTTPDo(ctx context.Context, req host.HTTPRequest) (host.HTTPResponse, error)
}

// Prober 通过宿主 HTTPDo 执行 Codex/ChatGPT wham usage fresh probe。
type Prober struct {
	host  httpDoer
	clock clock
}

// NewProber 创建使用宿主 HTTPDo 和注入时钟的 Codex/ChatGPT fresh prober。
func NewProber(hostAPI httpDoer, clockSource clock) Prober {
	if clockSource == nil {
		clockSource = realClock{}
	}
	return Prober{host: hostAPI, clock: clockSource}
}

// Probe 请求 ChatGPT wham usage 并返回只包含安全字段的 probe 结果。
func (p Prober) Probe(ctx context.Context, request ProbeRequest) ProbeResult {
	observedAt := p.clock.Now().UTC()
	response, err := p.host.HTTPDo(ctx, host.HTTPRequest{
		AuthIndex: request.AuthIndex,
		Method:    http.MethodGet,
		URL:       WhamUsageURL,
		Headers:   probeHeaders(request),
	})
	if err != nil {
		return failedProbe(request, observedAt, "host http do failed")
	}
	if response.StatusCode != http.StatusOK {
		return failedProbe(request, observedAt, fmt.Sprintf("wham usage status %d", response.StatusCode))
	}
	result := ParseWhamUsage(response.Body, observedAt)
	result.Provider = request.Provider
	result.AuthIndex = request.AuthIndex
	if result.Status == StatusReady {
		p.attachResetCredits(ctx, request, &result)
	}
	return result
}

// attachResetCredits best-effort 拉取姊妹端点 wham/rate-limit-reset-credits，把银行化重置额度
// 信号合入主 usage probe 结果。该端点探测失败或解析不出信号时静默跳过，不影响主 probe 的
// StatusReady 结论——重置额度只是打分时的加分信号，不是排序必需的核心证据。
func (p Prober) attachResetCredits(ctx context.Context, request ProbeRequest, result *ProbeResult) {
	response, err := p.host.HTTPDo(ctx, host.HTTPRequest{
		AuthIndex: request.AuthIndex,
		Method:    http.MethodGet,
		URL:       WhamResetCreditsURL,
		Headers:   probeHeaders(request),
	})
	if err != nil || response.StatusCode != http.StatusOK {
		return
	}
	summary, ok := parseWhamResetCredits(response.Body)
	if !ok {
		return
	}
	result.AvailableResetCredits = int(summary.availableCount)
	result.NearestResetCreditExpiresAt = summary.nearestExpiresAt
}

func failedProbe(request ProbeRequest, observedAt time.Time, message string) ProbeResult {
	result := failedResult(observedAt, safeError(message))
	result.Provider = request.Provider
	result.AuthIndex = request.AuthIndex
	return result
}

func probeHeaders(request ProbeRequest) host.Header {
	token := "$TOKEN$"
	if accessToken := strings.TrimSpace(request.AccessToken); accessToken != "" {
		token = accessToken
	}
	headers := host.Header{
		"Accept":        []string{"application/json"},
		"Authorization": []string{"Bearer " + token},
		"Content-Type":  []string{"application/json"},
		"User-Agent":    []string{"codex_cli_rs/0.76.0 (Debian 13.0.0; x86_64) WindowsTerminal"},
	}
	accountID := strings.TrimSpace(request.AccountID)
	if accountID != "" {
		headers["Chatgpt-Account-Id"] = []string{accountID}
	}
	return headers
}

func safeError(message string) string {
	trimmed := strings.TrimSpace(message)
	if trimmed == "" {
		return "probe failed"
	}
	return trimmed
}
