package protocol

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// ModelMeta 描述单个上游模型的元信息与计费档位。
type ModelMeta struct {
	ID              string `json:"id"`
	Name            string `json:"name"`
	Region          Region `json:"region"`
	Description     string `json:"description,omitempty"`
	ContextWindow   int64  `json:"context_window,omitempty"`
	MaxOutputTokens int64  `json:"max_output_tokens,omitempty"`
	IsReasoning     bool   `json:"is_reasoning"`
	SupportsImages  bool   `json:"supports_images"`
	SupportsTools   bool   `json:"supports_tools"`
	CreditLevel     string `json:"credit_level,omitempty"`
}

type rawModelEntry struct {
	ID                     string          `json:"id"`
	Name                   string          `json:"name"`
	Tooltip                string          `json:"tooltip"`
	ContextWindow          int64           `json:"contextWindow"`
	MaxTokens              int64           `json:"maxTokens"`
	Reasoning              bool            `json:"reasoning"`
	Input                  []string        `json:"input"`
	CreditConsumptionLevel json.RawMessage `json:"creditConsumptionLevel"`
}

type rawModelConfigResp struct {
	Code   *int64          `json:"code"`
	Msg    string          `json:"msg"`
	Models []rawModelEntry `json:"models"`
	Data   struct {
		Models []rawModelEntry `json:"models"`
	} `json:"data"`
}

// FetchModelsConfig 实时向指定区域上游请求 GET {base}/autoclaw-proxy/proxy/autoclaw-model-config 同步模型目录。
// 兼容顶层 models 与 data.models 两种响应结构；探测失败时直接返回明确错误，不编造任何静态模型。
func FetchModelsConfig(ctx context.Context, client *http.Client, baseURL, token string, region Region) ([]ModelMeta, error) {
	if strings.TrimSpace(token) == "" {
		return nil, fmt.Errorf("缺少访问令牌，无法同步 %s 模型目录", region.DisplayName())
	}
	if strings.TrimSpace(baseURL) == "" {
		return nil, fmt.Errorf("未配置 %s 上游基地址", region.DisplayName())
	}
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	endpoint := strings.TrimRight(baseURL, "/") + PathModelsConfig
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("构造模型目录请求失败: %w", err)
	}
	AttachUserHeaders(req, token)
	req.Header.Set("Accept", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return nil, &UpstreamError{
			Kind:    ErrorKindTransient,
			Message: fmt.Sprintf("请求 %s 模型目录失败: %v", region.DisplayName(), err),
		}
	}
	defer func() { _ = resp.Body.Close() }()

	retryAfter := ParseRetryAfter(resp.Header.Get("Retry-After"))
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, &UpstreamError{
			HTTPStatus: resp.StatusCode,
			Kind:       ErrorKindTransient,
			Message:    fmt.Sprintf("读取 %s 模型目录响应失败: %v", region.DisplayName(), err),
		}
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, ClassifyError(resp.StatusCode, 0, string(body), retryAfter)
	}
	return ParseModelsConfigJSON(body, resp.StatusCode, region)
}

// ParseModelsConfigJSON 解析上游模型配置响应 JSON（兼容顶层 models 与 data.models）。
func ParseModelsConfigJSON(raw []byte, httpStatus int, region Region) ([]ModelMeta, error) {
	var parsed rawModelConfigResp
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return nil, fmt.Errorf("解析模型配置 JSON 失败: %w", err)
	}
	if parsed.Code != nil && *parsed.Code != 0 {
		return nil, ClassifyError(httpStatus, *parsed.Code, parsed.Msg, 0)
	}
	entries := parsed.Models
	if len(entries) == 0 {
		entries = parsed.Data.Models
	}
	if len(entries) == 0 {
		return nil, fmt.Errorf("上游 %s 模型目录返回 0 个模型", region.DisplayName())
	}
	out := make([]ModelMeta, 0, len(entries))
	for _, e := range entries {
		id := strings.TrimSpace(e.ID)
		if id == "" {
			continue
		}
		name := strings.TrimSpace(e.Name)
		if name == "" {
			name = id
		}
		ctxWin := e.ContextWindow
		if ctxWin <= 0 {
			ctxWin = 128000
		}
		maxOut := e.MaxTokens
		if maxOut <= 0 {
			maxOut = 16384
		}
		supportsImg := false
		for _, in := range e.Input {
			if strings.EqualFold(strings.TrimSpace(in), "image") {
				supportsImg = true
				break
			}
		}
		out = append(out, ModelMeta{
			ID:              id,
			Name:            name,
			Region:          region,
			Description:     strings.TrimSpace(e.Tooltip),
			ContextWindow:   ctxWin,
			MaxOutputTokens: maxOut,
			IsReasoning:     e.Reasoning,
			SupportsImages:  supportsImg,
			SupportsTools:   true,
			CreditLevel:     parseCreditLevel(e.CreditConsumptionLevel),
		})
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("上游 %s 模型目录有效条目为空", region.DisplayName())
	}
	return out, nil
}

func parseCreditLevel(raw json.RawMessage) string {
	s := strings.TrimSpace(string(raw))
	if s == "" || s == "null" {
		return ""
	}
	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		return strings.TrimSpace(text)
	}
	var num json.Number
	if err := json.Unmarshal(raw, &num); err == nil {
		return "档位 " + num.String()
	}
	return ""
}
