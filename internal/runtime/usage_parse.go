package runtime

import (
	"encoding/json"
	"fmt"
	"strings"
)

// parseUsageRecord 解析 host usage.handle 下发的 pluginapi.UsageRecord JSON。
//
// 真实 schema（已对照 CLIProxyAPI v7.2.151 源码 sdk/pluginapi/types.go 逐字段确认，
// 不是猜测）：UsageRecord 没有任何 json tag，字段名即 JSON key，是扁平顶层结构——
// 不会嵌套在 record/usage/data 之类的外层 key 下；状态码/失败详情在嵌套的
// Failure{StatusCode,Body} 里，不是顶层字段；ResponseHeaders 是顶层 map[string][]string。
// 早期实现按未确认的字段名猜测（顶层 status_code/error/success），与真实 schema 不符，
// 会导致 usage.handle 收到的每条记录都被误判为 StatusCode=0 且 Error/ErrorCode 为空，
// isUsageSuccess 因此恒为 true——本次一并修正。
func parseUsageRecord(raw []byte) (usageRecord, bool) {
	if len(strings.TrimSpace(string(raw))) == 0 {
		return usageRecord{}, false
	}
	var root map[string]any
	if err := json.Unmarshal(raw, &root); err != nil {
		return usageRecord{}, false
	}
	// 真实 UsageRecord 是扁平结构，不会嵌套；这层兼容仅作防御性宽容。
	if nested, ok := firstMap(root, "record", "usage", "data", "UsageRecord", "usage_record"); ok {
		root = nested
	}
	rec := usageRecord{
		AuthIndex: firstString(root, "AuthIndex", "auth_index", "authIndex", "auth"),
		Provider:  firstString(root, "Provider", "provider"),
		Model:     firstString(root, "Model", "model", "model_name", "ModelName"),
	}
	failed, hasFailed := firstBool(root, "Failed", "failed")
	if failureObj, ok := firstMap(root, "Failure", "failure"); ok {
		rec.StatusCode = firstInt(failureObj, "StatusCode", "status_code", "status")
		body := firstString(failureObj, "Body", "body")
		rec.Error = body
		rec.RawBody = body
	}
	if hasFailed {
		rec.Failed = failed
	} else {
		// Failed 字段缺失（极端防御场景）：退化为按是否存在 Failure 细节判断。
		rec.Failed = rec.StatusCode != 0 || rec.Error != ""
	}
	success := !rec.Failed
	rec.Success = &success
	if headers := parseHeadersValue(firstAny(root, "ResponseHeaders", "response_headers")); headers != nil {
		rec.ResponseHeaders = headers
	}
	if strings.TrimSpace(rec.AuthIndex) == "" {
		return usageRecord{}, false
	}
	return rec, true
}

func firstAny(root map[string]any, keys ...string) any {
	for _, key := range keys {
		if v, ok := root[key]; ok {
			return v
		}
	}
	return nil
}

// parseHeadersValue 把 JSON 里的 ResponseHeaders（map[string][]string 序列化后的
// map[string]any，值可能是字符串数组，也兼容单字符串）解析为普通 map[string][]string。
func parseHeadersValue(raw any) map[string][]string {
	m, ok := raw.(map[string]any)
	if !ok {
		return nil
	}
	headers := make(map[string][]string, len(m))
	for key, v := range m {
		switch t := v.(type) {
		case []any:
			values := make([]string, 0, len(t))
			for _, item := range t {
				if s, ok := item.(string); ok && strings.TrimSpace(s) != "" {
					values = append(values, s)
				}
			}
			if len(values) > 0 {
				headers[key] = values
			}
		case string:
			if strings.TrimSpace(t) != "" {
				headers[key] = []string{t}
			}
		}
	}
	if len(headers) == 0 {
		return nil
	}
	return headers
}

func firstMap(root map[string]any, keys ...string) (map[string]any, bool) {
	for _, key := range keys {
		if v, ok := root[key]; ok {
			if m, ok := v.(map[string]any); ok {
				return m, true
			}
		}
	}
	return nil, false
}

func firstString(root map[string]any, keys ...string) string {
	for _, key := range keys {
		v, ok := root[key]
		if !ok || v == nil {
			continue
		}
		switch t := v.(type) {
		case string:
			if s := strings.TrimSpace(t); s != "" {
				return s
			}
		case fmt.Stringer:
			if s := strings.TrimSpace(t.String()); s != "" {
				return s
			}
		}
	}
	return ""
}

func firstInt(root map[string]any, keys ...string) int {
	for _, key := range keys {
		v, ok := root[key]
		if !ok || v == nil {
			continue
		}
		switch t := v.(type) {
		case float64:
			return int(t)
		case json.Number:
			i, err := t.Int64()
			if err == nil {
				return int(i)
			}
		case int:
			return t
		case int64:
			return int(t)
		case string:
			var n int
			if _, err := fmt.Sscanf(strings.TrimSpace(t), "%d", &n); err == nil {
				return n
			}
		}
	}
	return 0
}

func firstBool(root map[string]any, keys ...string) (bool, bool) {
	for _, key := range keys {
		v, ok := root[key]
		if !ok || v == nil {
			continue
		}
		switch t := v.(type) {
		case bool:
			return t, true
		case string:
			switch strings.ToLower(strings.TrimSpace(t)) {
			case "true", "1", "yes", "ok":
				return true, true
			case "false", "0", "no":
				return false, true
			}
		case float64:
			return t != 0, true
		}
	}
	return false, false
}
