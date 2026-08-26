package claude

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"quota-pacer/internal/core"
	"quota-pacer/internal/host"
)

type httpDoer interface {
	HTTPDo(ctx context.Context, req host.HTTPRequest) (host.HTTPResponse, error)
}

type httpRawDoer interface {
	HTTPDoRaw(ctx context.Context, req host.HTTPRequest) (host.HTTPResponse, error)
}

// Prober 通过宿主 HTTPDo 执行 Claude fresh probe。
type Prober struct {
	host  httpDoer
	clock clock
}

// NewProber 创建使用宿主 HTTPDo 和注入时钟的 Claude fresh prober。
func NewProber(hostAPI httpDoer, clockSource clock) Prober {
	if clockSource == nil {
		clockSource = realClock{}
	}
	return Prober{host: hostAPI, clock: clockSource}
}

// Probe 请求 Claude 配额端点并返回只包含安全字段的 probe 结果。
func (p Prober) Probe(ctx context.Context, request ProbeRequest) ProbeResult {
	observedAt := p.clock.Now().UTC()
	baseURL := strings.TrimRight(strings.TrimSpace(request.BaseURL), "/")
	if baseURL == "" {
		baseURL = DefaultAPIBaseURL
	}
	headers := probeHeaders(request)
	orgUUID := strings.TrimSpace(request.OrganizationUUID)

	// 如果针对 Web 前端且无 orgUUID，尝试自动发现组织
	if orgUUID == "" && strings.Contains(baseURL, "claude.ai") {
		if discoveredOrg, orgBody, respStatus, err := p.discoverOrganization(ctx, request, baseURL, headers); err == nil && respStatus == http.StatusOK {
			if len(orgBody) > 0 {
				directResult := ParseClaudeUsage(orgBody, nil, observedAt)
				if directResult.Status == StatusReady {
					directResult.Provider = providerOrDefault(request.Provider)
					directResult.AuthIndex = request.AuthIndex
					if directResult.OrganizationUUID == "" && discoveredOrg != "" {
						directResult.OrganizationUUID = discoveredOrg
					}
					return directResult
				}
			}
			if discoveredOrg != "" {
				orgUUID = discoveredOrg
			}
		}
	}

	// 对于 API 接口（非 claude.ai 网页端），优先执行轻量 /v1/messages POST 微探测以获取精确 Unified RateLimit Headers
	if !strings.Contains(baseURL, "claude.ai") {
		microReqBody := []byte(`{"model":"claude-haiku-4-5-20251001","max_tokens":1,"messages":[{"role":"user","content":"hi"}]}`)
		resp, err := p.doRequest(ctx, host.HTTPRequest{
			AuthIndex: request.AuthIndex,
			Method:    http.MethodPost,
			URL:       baseURL + "/v1/messages",
			Headers:   headers,
			Body:      microReqBody,
		})
		if err == nil {
			if resp.StatusCode == http.StatusOK {
				result := ParseClaudeUsage(resp.Body, resp.Headers, observedAt)
				if result.Status == StatusReady {
					result.Provider = providerOrDefault(request.Provider)
					result.AuthIndex = request.AuthIndex
					if result.OrganizationUUID == "" && orgUUID != "" {
						result.OrganizationUUID = orgUUID
					}
					return result
				}
			} else if resp.StatusCode == http.StatusTooManyRequests {
				result := ParseClaudeRateLimitError(resp.Body, resp.Headers, observedAt)
				result.Provider = providerOrDefault(request.Provider)
				result.AuthIndex = request.AuthIndex
				if result.OrganizationUUID == "" && orgUUID != "" {
					result.OrganizationUUID = orgUUID
				}
				return result
			} else if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
				return failedProbe(request, observedAt, fmt.Sprintf("claude probe status %d", resp.StatusCode))
			}
		}
	}

	urls := probeCandidateURLs(baseURL, orgUUID)
	var lastErr string
	var lastStatus int
	var lastBody []byte
	var lastHeaders host.Header

	for _, url := range urls {
		resp, err := p.doRequest(ctx, host.HTTPRequest{
			AuthIndex: request.AuthIndex,
			Method:    http.MethodGet,
			URL:       url,
			Headers:   headers,
		})
		if err != nil && resp.StatusCode == 0 {
			lastErr = "host http do failed"
			continue
		}
		lastStatus = resp.StatusCode
		lastBody = resp.Body
		lastHeaders = resp.Headers

		if resp.StatusCode == http.StatusOK && len(resp.Body) > 0 {
			result := ParseClaudeUsage(resp.Body, resp.Headers, observedAt)
			if result.Status == StatusReady {
				result.Provider = providerOrDefault(request.Provider)
				result.AuthIndex = request.AuthIndex
				if result.OrganizationUUID == "" && orgUUID != "" {
					result.OrganizationUUID = orgUUID
				}
				return result
			}
			lastErr = "parse claude usage not ready"
			continue
		}

		if resp.StatusCode == http.StatusTooManyRequests {
			result := ParseClaudeRateLimitError(resp.Body, resp.Headers, observedAt)
			result.Provider = providerOrDefault(request.Provider)
			result.AuthIndex = request.AuthIndex
			if result.OrganizationUUID == "" && orgUUID != "" {
				result.OrganizationUUID = orgUUID
			}
			return result
		}

		if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
			return failedProbe(request, observedAt, fmt.Sprintf("claude probe status %d", resp.StatusCode))
		}

		lastErr = fmt.Sprintf("claude probe status %d", resp.StatusCode)
	}

	if lastStatus == http.StatusTooManyRequests {
		result := ParseClaudeRateLimitError(lastBody, lastHeaders, observedAt)
		result.Provider = providerOrDefault(request.Provider)
		result.AuthIndex = request.AuthIndex
		return result
	}

	return failedProbe(request, observedAt, safeError(lastErr))
}

func (p Prober) doRequest(ctx context.Context, req host.HTTPRequest) (host.HTTPResponse, error) {
	if rawDoer, ok := p.host.(httpRawDoer); ok {
		return rawDoer.HTTPDoRaw(ctx, req)
	}
	return p.host.HTTPDo(ctx, req)
}

func (p Prober) discoverOrganization(ctx context.Context, request ProbeRequest, baseURL string, headers host.Header) (string, []byte, int, error) {
	orgURL := baseURL + "/organizations"
	resp, err := p.doRequest(ctx, host.HTTPRequest{
		AuthIndex: request.AuthIndex,
		Method:    http.MethodGet,
		URL:       orgURL,
		Headers:   headers,
	})
	if err != nil && resp.StatusCode == 0 {
		return "", nil, 0, err
	}
	if resp.StatusCode != http.StatusOK {
		return "", resp.Body, resp.StatusCode, nil
	}
	var orgList []struct {
		UUID string `json:"uuid"`
		ID   string `json:"id"`
	}
	trimmed := bytes.TrimSpace(resp.Body)
	if len(trimmed) > 0 && trimmed[0] == '[' {
		if err := json.Unmarshal(trimmed, &orgList); err == nil && len(orgList) > 0 {
			uuid := orgList[0].UUID
			if uuid == "" {
				uuid = orgList[0].ID
			}
			return uuid, resp.Body, resp.StatusCode, nil
		}
	}
	var singleOrg struct {
		UUID string `json:"uuid"`
		ID   string `json:"id"`
	}
	if err := json.Unmarshal(trimmed, &singleOrg); err == nil {
		uuid := singleOrg.UUID
		if uuid == "" {
			uuid = singleOrg.ID
		}
		return uuid, resp.Body, resp.StatusCode, nil
	}
	return "", resp.Body, resp.StatusCode, nil
}

func probeCandidateURLs(baseURL string, orgUUID string) []string {
	base := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	urls := make([]string, 0, 4)

	// 若为官方 API 或通用 API 形式，优先请求 /v1/models
	if strings.Contains(base, "api.anthropic.com") || !strings.Contains(base, "claude.ai") {
		urls = append(urls, base+"/v1/models")
		if orgUUID != "" {
			urls = append(urls, fmt.Sprintf("%s/organizations/%s/usage", base, orgUUID))
		}
		urls = append(urls, base+"/v1/messages")
	} else {
		// 网页端或反代 API 形式
		if orgUUID != "" {
			urls = append(urls,
				fmt.Sprintf("%s/organizations/%s/usage", base, orgUUID),
				fmt.Sprintf("%s/organizations/%s/rate_limits", base, orgUUID),
				fmt.Sprintf("%s/organizations/%s/stats", base, orgUUID),
				fmt.Sprintf("%s/organizations/%s", base, orgUUID),
			)
		} else {
			urls = append(urls,
				base+"/organizations",
				base+"/organizations/usage",
				base+"/usage",
			)
		}
	}
	return urls
}

func probeHeaders(request ProbeRequest) host.Header {
	token := "$TOKEN$"
	if accessToken := strings.TrimSpace(request.AccessToken); accessToken != "" {
		token = accessToken
	}
	headers := host.Header{
		"Accept":            []string{"application/json"},
		"Content-Type":      []string{"application/json"},
		"User-Agent":        []string{"claude-code/0.2.29 (darwin; arm64)"},
		"Anthropic-Version": []string{"2023-06-01"},
		"Authorization":     []string{"Bearer " + token},
	}
	if strings.HasPrefix(token, "sk-ant-api") {
		headers["X-Api-Key"] = []string{token}
	}
	if strings.HasPrefix(token, "sk-ant-sid") || strings.HasPrefix(token, "sessionKey=") {
		cookieVal := token
		if !strings.HasPrefix(cookieVal, "sessionKey=") {
			cookieVal = "sessionKey=" + token
		}
		headers["Cookie"] = []string{cookieVal}
	}
	return headers
}

func providerOrDefault(p core.Provider) core.Provider {
	if p != "" {
		return p
	}
	return core.ProviderClaude
}

func failedProbe(request ProbeRequest, observedAt time.Time, message string) ProbeResult {
	result := failedResult(observedAt, safeError(message))
	result.Provider = providerOrDefault(request.Provider)
	result.AuthIndex = request.AuthIndex
	return result
}

func safeError(message string) string {
	trimmed := strings.TrimSpace(message)
	if trimmed == "" {
		return "probe failed"
	}
	return trimmed
}
