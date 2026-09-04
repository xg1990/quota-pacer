package codex

import (
	"bytes"
	"encoding/json"
	"strings"
	"time"
)

// whamResetCreditsResponse 是 wham/rate-limit-reset-credits 响应的宽松映射。
// 该端点未公开文档化（逆向工程确认），不同报告对字段命名/包裹容器存在 snake_case 与
// camelCase 混用、列表键名不一致的情况，因此所有字段用 any 承接，交由 convert.go 的
// toInt64/toString/parseAnyTime 统一容错解析。
type whamResetCreditsResponse struct {
	AvailableCount      any                     `json:"available_count"`
	AvailableCountCamel any                     `json:"availableCount"`
	Credits             []whamResetCreditRecord `json:"credits"`
	Details             []whamResetCreditRecord `json:"details"`
	Data                []whamResetCreditRecord `json:"data"`
}

type whamResetCreditRecord struct {
	Status         any `json:"status"`
	ExpiresAt      any `json:"expires_at"`
	ExpiresAtCamel any `json:"expiresAt"`
}

// resetCreditsSummary 是排序所需的最小信号：available 状态额度的数量与最近过期时间。
type resetCreditsSummary struct {
	availableCount   int64
	nearestExpiresAt *time.Time
}

// parseWhamResetCredits 解析 wham/rate-limit-reset-credits 响应。
// 第二个返回值表示是否解析出任何可用信号；响应体为空、无法解析或没有 available 额度且
// 顶层 available_count 也缺失时返回 false，调用方应静默跳过（不影响主 usage probe 的可用性）。
func parseWhamResetCredits(raw []byte) (resetCreditsSummary, bool) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return resetCreditsSummary{}, false
	}
	var response whamResetCreditsResponse
	decoder := json.NewDecoder(bytes.NewReader(trimmed))
	decoder.UseNumber()
	if err := decoder.Decode(&response); err != nil {
		return resetCreditsSummary{}, false
	}

	entries := response.Credits
	if len(entries) == 0 {
		entries = response.Details
	}
	if len(entries) == 0 {
		entries = response.Data
	}

	var nearest *time.Time
	availableFromEntries := int64(0)
	for _, entry := range entries {
		status, _ := toString(entry.Status)
		if !strings.EqualFold(status, "available") {
			continue
		}
		availableFromEntries++
		expiresAt, ok := parseAnyTime(entry.ExpiresAt)
		if !ok {
			expiresAt, ok = parseAnyTime(entry.ExpiresAtCamel)
		}
		if !ok || expiresAt == nil {
			continue
		}
		if nearest == nil || expiresAt.Before(*nearest) {
			nearest = expiresAt
		}
	}

	summary := resetCreditsSummary{nearestExpiresAt: nearest}
	if count, ok := toInt64(response.AvailableCount); ok {
		summary.availableCount = count
	} else if count, ok := toInt64(response.AvailableCountCamel); ok {
		summary.availableCount = count
	} else {
		summary.availableCount = availableFromEntries
	}

	if summary.availableCount <= 0 && summary.nearestExpiresAt == nil {
		return resetCreditsSummary{}, false
	}
	return summary, true
}
