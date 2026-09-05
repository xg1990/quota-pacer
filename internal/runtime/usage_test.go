package runtime

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"
)

// mimicUsageFailure/mimicUsageRecord 镜像 CLIProxyAPI v7.2.151
// sdk/pluginapi/types.go 里 UsageRecord/UsageFailure 的真实字段（没有任何 json
// tag，字段名即 JSON key，扁平顶层结构）。用真实的 json.Marshal 序列化路径构造
// 测试输入，而不是直接手写 parseUsageRecord 期望的 map，能在 schema 再次漂移时
// 真正捕获回归，而不是自证式地验证解析器认同自己的假设。
type mimicUsageFailure struct {
	StatusCode int
	Body       string
}

type mimicUsageRecord struct {
	Provider        string
	AuthIndex       string
	Model           string
	Failed          bool
	Failure         mimicUsageFailure
	ResponseHeaders http.Header
}

func TestParseUsageRecord_RealSchemaSuccess(t *testing.T) {
	rec := mimicUsageRecord{
		Provider:  "claude",
		AuthIndex: "auth-1",
		Model:     "claude-sonnet-5",
		Failed:    false,
		ResponseHeaders: http.Header{
			"Anthropic-Ratelimit-Unified-5h-Utilization": []string{"0.4"},
		},
	}
	raw, err := json.Marshal(rec)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	parsed, ok := parseUsageRecord(raw)
	if !ok {
		t.Fatalf("expected parseUsageRecord to succeed")
	}
	if parsed.AuthIndex != "auth-1" || parsed.Provider != "claude" {
		t.Errorf("expected AuthIndex/Provider threaded through, got %+v", parsed)
	}
	if parsed.Failed {
		t.Errorf("expected Failed=false, got true")
	}
	if parsed.Success == nil || !*parsed.Success {
		t.Errorf("expected Success=true, got %v", parsed.Success)
	}
	if parsed.StatusCode != 0 {
		t.Errorf("expected StatusCode=0 on success, got %d", parsed.StatusCode)
	}
	if len(parsed.ResponseHeaders["Anthropic-Ratelimit-Unified-5h-Utilization"]) != 1 {
		t.Errorf("expected ResponseHeaders threaded through, got %+v", parsed.ResponseHeaders)
	}
}

func TestParseUsageRecord_RealSchemaFailure429(t *testing.T) {
	rec := mimicUsageRecord{
		Provider:  "claude",
		AuthIndex: "auth-2",
		Failed:    true,
		Failure:   mimicUsageFailure{StatusCode: 429, Body: "rate limited"},
	}
	raw, err := json.Marshal(rec)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	parsed, ok := parseUsageRecord(raw)
	if !ok {
		t.Fatalf("expected parseUsageRecord to succeed")
	}
	if !parsed.Failed {
		t.Errorf("expected Failed=true")
	}
	if parsed.StatusCode != 429 {
		t.Errorf("expected StatusCode=429 (from nested Failure.StatusCode), got %d", parsed.StatusCode)
	}
	if parsed.Error != "rate limited" || parsed.RawBody != "rate limited" {
		t.Errorf("expected Error/RawBody populated from Failure.Body, got Error=%q RawBody=%q", parsed.Error, parsed.RawBody)
	}
	if parsed.Success == nil || *parsed.Success {
		t.Errorf("expected Success=false, got %v", parsed.Success)
	}
}

func TestParseUsageRecord_MissingAuthIndexRejected(t *testing.T) {
	raw := []byte(`{"Provider":"claude","Failed":false}`)
	if _, ok := parseUsageRecord(raw); ok {
		t.Errorf("expected parseUsageRecord to reject records without AuthIndex")
	}
}

func TestParseUsageRecord_EmptyPayloadRejected(t *testing.T) {
	if _, ok := parseUsageRecord(nil); ok {
		t.Errorf("expected parseUsageRecord to reject empty payload")
	}
}

// TestClassifyXAIUsage_RealSchema429NoLongerMisclassifiedAsSuccess 锁定本次修复
// 的核心回归：修复前 usageRecord.StatusCode/Error 是按未确认的顶层字段名
// （status_code/error）猜测解析的，真实 pluginapi.UsageRecord 里这些细节其实嵌套
// 在 Failure{StatusCode,Body} 下——导致每一条真实的 usage.handle 记录都会被解析成
// StatusCode=0 且 Error=""，isUsageSuccess 因此恒为 true，即使响应其实是 429。
// 本测试用真实 json.Marshal 序列化路径构造一条 429 记录，验证现在能被正确分类为
// usageDecisionFreeDepleted，而不是被误判为成功。
func TestClassifyXAIUsage_RealSchema429NoLongerMisclassifiedAsSuccess(t *testing.T) {
	rec := mimicUsageRecord{
		Provider:  "xai",
		AuthIndex: "auth-xai-1",
		Failed:    true,
		Failure:   mimicUsageFailure{StatusCode: 429, Body: "rate limit exceeded"},
	}
	raw, err := json.Marshal(rec)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	parsed, ok := parseUsageRecord(raw)
	if !ok {
		t.Fatalf("expected parseUsageRecord to succeed")
	}

	now := time.Date(2026, 8, 20, 10, 0, 0, 0, time.UTC)
	decision := classifyXAIUsage(parsed, now)
	if decision.kind != usageDecisionFreeDepleted {
		t.Errorf("expected usageDecisionFreeDepleted for a real-schema 429 record, got %v (this would previously misclassify as success)", decision.kind)
	}
}

func TestClassifyXAIUsage_RealSchemaSuccessStillClassifiedAsSuccess(t *testing.T) {
	rec := mimicUsageRecord{
		Provider:  "xai",
		AuthIndex: "auth-xai-2",
		Failed:    false,
	}
	raw, err := json.Marshal(rec)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	parsed, ok := parseUsageRecord(raw)
	if !ok {
		t.Fatalf("expected parseUsageRecord to succeed")
	}

	now := time.Date(2026, 8, 20, 10, 0, 0, 0, time.UTC)
	decision := classifyXAIUsage(parsed, now)
	if decision.kind != usageDecisionSuccess {
		t.Errorf("expected usageDecisionSuccess for a real-schema successful record, got %v", decision.kind)
	}
}
