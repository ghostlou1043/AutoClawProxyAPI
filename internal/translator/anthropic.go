package translator

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"autoclaw2api/internal/protocol"
)

// AnthropicToOpenAIRequest 将 Anthropic /v1/messages 请求转换为 OpenAI /v1/chat/completions 请求。
// 返回转换后的请求体、模型名称与是否开启流式输出。
func AnthropicToOpenAIRequest(raw []byte) (outBody []byte, model string, stream bool, err error) {
	var req map[string]any
	if err := json.Unmarshal(raw, &req); err != nil {
		return nil, "", false, fmt.Errorf("解析 Anthropic 请求失败: %w", err)
	}

	model, _ = req["model"].(string)
	model = strings.TrimSpace(model)
	stream, _ = req["stream"].(bool)

	messages := make([]map[string]any, 0, 8)

	// 1. 解析 system（支持字符串或 text 内容块数组）
	if sysRaw, exists := req["system"]; exists && sysRaw != nil {
		sysText := extractAnthropicSystemText(sysRaw)
		if sysText != "" {
			messages = append(messages, map[string]any{
				"role":    "system",
				"content": sysText,
			})
		}
	}

	// 2. 解析 messages
	if rawMsgs, ok := req["messages"].([]any); ok {
		for _, item := range rawMsgs {
			m, ok := item.(map[string]any)
			if !ok {
				continue
			}
			role, _ := m["role"].(string)
			role = strings.ToLower(strings.TrimSpace(role))
			if role == "" {
				role = "user"
			}
			converted := convertAnthropicMessage(role, m["content"])
			messages = append(messages, converted...)
		}
	}

	out := map[string]any{
		"model":    model,
		"messages": messages,
		"stream":   stream,
	}
	if stream {
		out["stream_options"] = map[string]any{"include_usage": true}
	}
	if v, ok := req["max_tokens"]; ok && v != nil {
		out["max_tokens"] = v
	}
	if v, ok := req["temperature"]; ok && v != nil {
		out["temperature"] = v
	}
	if v, ok := req["top_p"]; ok && v != nil {
		out["top_p"] = v
	}
	if v, ok := req["stop_sequences"]; ok && v != nil {
		out["stop"] = v
	}

	// 3. 转换 tools 定义
	if rawTools, ok := req["tools"].([]any); ok && len(rawTools) > 0 {
		oaiTools := make([]map[string]any, 0, len(rawTools))
		for _, item := range rawTools {
			t, ok := item.(map[string]any)
			if !ok {
				continue
			}
			name, _ := t["name"].(string)
			if strings.TrimSpace(name) == "" {
				continue
			}
			fn := map[string]any{
				"name": name,
			}
			if desc, ok := t["description"].(string); ok && desc != "" {
				fn["description"] = desc
			}
			if schema, ok := t["input_schema"]; ok && schema != nil {
				fn["parameters"] = schema
			} else {
				fn["parameters"] = map[string]any{"type": "object", "properties": map[string]any{}}
			}
			oaiTools = append(oaiTools, map[string]any{
				"type":     "function",
				"function": fn,
			})
		}
		if len(oaiTools) > 0 {
			out["tools"] = oaiTools
		}
	}

	// 4. 转换 tool_choice
	if tc, ok := req["tool_choice"].(map[string]any); ok {
		tcType, _ := tc["type"].(string)
		switch strings.ToLower(strings.TrimSpace(tcType)) {
		case "auto":
			out["tool_choice"] = "auto"
		case "any":
			out["tool_choice"] = "required"
		case "none":
			out["tool_choice"] = "none"
		case "tool":
			if name, _ := tc["name"].(string); strings.TrimSpace(name) != "" {
				out["tool_choice"] = map[string]any{
					"type":     "function",
					"function": map[string]any{"name": name},
				}
			}
		}
	}

	encoded, err := json.Marshal(out)
	if err != nil {
		return nil, "", false, err
	}
	return encoded, model, stream, nil
}

func extractAnthropicSystemText(raw any) string {
	switch v := raw.(type) {
	case string:
		return v
	case []any:
		parts := make([]string, 0, len(v))
		for _, item := range v {
			m, ok := item.(map[string]any)
			if !ok {
				continue
			}
			if text, ok := m["text"].(string); ok && text != "" {
				parts = append(parts, text)
			}
		}
		return strings.Join(parts, "\n\n")
	default:
		return ""
	}
}

func convertAnthropicMessage(role string, content any) []map[string]any {
	switch c := content.(type) {
	case string:
		return []map[string]any{{"role": role, "content": c}}
	case []any:
		if role == "assistant" {
			return convertAnthropicAssistantBlocks(c)
		}
		return convertAnthropicUserBlocks(role, c)
	default:
		return []map[string]any{{"role": role, "content": ""}}
	}
}

func convertAnthropicAssistantBlocks(blocks []any) []map[string]any {
	var textParts []string
	var thinkingParts []string
	toolCalls := make([]map[string]any, 0)

	for _, item := range blocks {
		blk, ok := item.(map[string]any)
		if !ok {
			continue
		}
		typ, _ := blk["type"].(string)
		switch typ {
		case "text":
			if t, ok := blk["text"].(string); ok && t != "" {
				textParts = append(textParts, t)
			}
		case "thinking":
			if t, ok := blk["thinking"].(string); ok && t != "" {
				thinkingParts = append(thinkingParts, t)
			}
		case "tool_use":
			id, _ := blk["id"].(string)
			name, _ := blk["name"].(string)
			if id == "" {
				id = "call_" + protocol.RandomHex(12)
			}
			argsBytes, _ := json.Marshal(blk["input"])
			if len(argsBytes) == 0 || string(argsBytes) == "null" {
				argsBytes = []byte("{}")
			}
			toolCalls = append(toolCalls, map[string]any{
				"id":   id,
				"type": "function",
				"function": map[string]any{
					"name":      name,
					"arguments": string(argsBytes),
				},
			})
		}
	}

	msg := map[string]any{"role": "assistant"}
	joinedText := strings.Join(textParts, "")
	if joinedText != "" || len(toolCalls) == 0 {
		msg["content"] = joinedText
	} else {
		msg["content"] = nil
	}
	if len(thinkingParts) > 0 {
		msg["reasoning_content"] = strings.Join(thinkingParts, "\n")
	}
	if len(toolCalls) > 0 {
		msg["tool_calls"] = toolCalls
	}
	return []map[string]any{msg}
}

func convertAnthropicUserBlocks(role string, blocks []any) []map[string]any {
	results := make([]map[string]any, 0, len(blocks))
	userParts := make([]map[string]any, 0)

	flushUser := func() {
		if len(userParts) == 0 {
			return
		}
		// 若全部为纯文本则合并为字符串，提高上游兼容性
		allText := true
		var sb strings.Builder
		for _, p := range userParts {
			if p["type"] != "text" {
				allText = false
				break
			}
			if sb.Len() > 0 {
				sb.WriteString("\n")
			}
			sb.WriteString(fmt.Sprint(p["text"]))
		}
		if allText {
			results = append(results, map[string]any{
				"role":    role,
				"content": sb.String(),
			})
		} else {
			results = append(results, map[string]any{
				"role":    role,
				"content": userParts,
			})
		}
		userParts = nil
	}

	for _, item := range blocks {
		blk, ok := item.(map[string]any)
		if !ok {
			continue
		}
		typ, _ := blk["type"].(string)
		switch typ {
		case "tool_result":
			flushUser()
			callID, _ := blk["tool_use_id"].(string)
			contentText := flattenAnthropicToolResult(blk["content"])
			results = append(results, map[string]any{
				"role":         "tool",
				"tool_call_id": callID,
				"content":      contentText,
			})
		case "text":
			if t, ok := blk["text"].(string); ok {
				userParts = append(userParts, map[string]any{
					"type": "text",
					"text": t,
				})
			}
		case "image":
			if src, ok := blk["source"].(map[string]any); ok {
				srcType, _ := src["type"].(string)
				if srcType == "base64" {
					mediaType, _ := src["media_type"].(string)
					if mediaType == "" {
						mediaType = "image/png"
					}
					data, _ := src["data"].(string)
					userParts = append(userParts, map[string]any{
						"type": "image_url",
						"image_url": map[string]any{
							"url": fmt.Sprintf("data:%s;base64,%s", mediaType, data),
						},
					})
				} else if srcType == "url" {
					u, _ := src["url"].(string)
					userParts = append(userParts, map[string]any{
						"type":      "image_url",
						"image_url": map[string]any{"url": u},
					})
				}
			}
		}
	}
	flushUser()
	if len(results) == 0 {
		results = append(results, map[string]any{"role": role, "content": ""})
	}
	return results
}

func flattenAnthropicToolResult(raw any) string {
	switch v := raw.(type) {
	case string:
		return v
	case []any:
		var parts []string
		for _, item := range v {
			if m, ok := item.(map[string]any); ok {
				if t, ok := m["text"].(string); ok {
					parts = append(parts, t)
				}
			}
		}
		return strings.Join(parts, "\n")
	case nil:
		return ""
	default:
		b, _ := json.Marshal(v)
		return string(b)
	}
}

// OpenAIToAnthropicResponse 将 OpenAI 非流式 JSON 响应转换为 Anthropic Messages 格式。
func OpenAIToAnthropicResponse(raw []byte, fallbackModel string) ([]byte, error) {
	var resp struct {
		ID      string `json:"id"`
		Model   string `json:"model"`
		Choices []struct {
			FinishReason string `json:"finish_reason"`
			Message      struct {
				Role             string `json:"role"`
				Content          any    `json:"content"`
				ReasoningContent string `json:"reasoning_content"`
				Reasoning        string `json:"reasoning"`
				ToolCalls        []struct {
					ID       string `json:"id"`
					Type     string `json:"type"`
					Function struct {
						Name      string `json:"name"`
						Arguments string `json:"arguments"`
					} `json:"function"`
				} `json:"tool_calls"`
			} `json:"message"`
		} `json:"choices"`
		Usage struct {
			PromptTokens        int64 `json:"prompt_tokens"`
			CompletionTokens    int64 `json:"completion_tokens"`
			PromptTokensDetails struct {
				CachedTokens int64 `json:"cached_tokens"`
			} `json:"prompt_tokens_details"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		return nil, fmt.Errorf("解析 OpenAI 响应失败: %w", err)
	}

	msgID := resp.ID
	if msgID == "" {
		msgID = "msg_" + protocol.RandomHex(16)
	} else if !strings.HasPrefix(msgID, "msg_") {
		msgID = "msg_" + msgID
	}
	model := resp.Model
	if model == "" {
		model = fallbackModel
	}

	contentBlocks := make([]map[string]any, 0, 4)
	stopReason := "end_turn"

	if len(resp.Choices) > 0 {
		ch := resp.Choices[0]
		thinking := ch.Message.ReasoningContent
		if thinking == "" {
			thinking = ch.Message.Reasoning
		}
		if thinking != "" {
			contentBlocks = append(contentBlocks, map[string]any{
				"type":     "thinking",
				"thinking": thinking,
			})
		}
		if text := extractOpenAIContentText(ch.Message.Content); text != "" {
			contentBlocks = append(contentBlocks, map[string]any{
				"type": "text",
				"text": text,
			})
		}
		for _, tc := range ch.Message.ToolCalls {
			id := tc.ID
			if id == "" {
				id = "toolu_" + protocol.RandomHex(12)
			}
			var inputObj any = map[string]any{}
			if strings.TrimSpace(tc.Function.Arguments) != "" {
				var parsed any
				if err := json.Unmarshal([]byte(tc.Function.Arguments), &parsed); err == nil && parsed != nil {
					inputObj = parsed
				}
			}
			contentBlocks = append(contentBlocks, map[string]any{
				"type":  "tool_use",
				"id":    id,
				"name":  tc.Function.Name,
				"input": inputObj,
			})
		}
		stopReason = mapOpenAIFinishToAnthropic(ch.FinishReason, len(ch.Message.ToolCalls) > 0)
	}

	usage := map[string]any{
		"input_tokens":  resp.Usage.PromptTokens,
		"output_tokens": resp.Usage.CompletionTokens,
	}
	if resp.Usage.PromptTokensDetails.CachedTokens > 0 {
		usage["cache_read_input_tokens"] = resp.Usage.PromptTokensDetails.CachedTokens
	}

	out := map[string]any{
		"id":            msgID,
		"type":          "message",
		"role":          "assistant",
		"model":         model,
		"content":       contentBlocks,
		"stop_reason":   stopReason,
		"stop_sequence": nil,
		"usage":         usage,
	}
	return json.Marshal(out)
}

func extractOpenAIContentText(v any) string {
	switch c := v.(type) {
	case string:
		return c
	case []any:
		var parts []string
		for _, item := range c {
			if m, ok := item.(map[string]any); ok {
				if t, ok := m["text"].(string); ok {
					parts = append(parts, t)
				}
			}
		}
		return strings.Join(parts, "")
	default:
		return ""
	}
}

func mapOpenAIFinishToAnthropic(reason string, hasTools bool) string {
	switch strings.ToLower(strings.TrimSpace(reason)) {
	case "tool_calls", "function_call":
		return "tool_use"
	case "length":
		return "max_tokens"
	case "stop":
		if hasTools {
			return "tool_use"
		}
		return "end_turn"
	default:
		if hasTools {
			return "tool_use"
		}
		return "end_turn"
	}
}

// AnthropicStreamConverter 是 OpenAI SSE → Anthropic SSE 的流式状态机。
type AnthropicStreamConverter struct {
	msgID        string
	model        string
	started      bool
	finished     bool
	nextBlockIdx int

	thinkingOpen bool
	thinkingIdx  int
	textOpen     bool
	textIdx      int

	openToolUpstreamIdx int
	toolStates          map[int]*anthropicToolState
	sawToolCall         bool
	finishReason        string

	inputTokens  int64
	outputTokens int64
	cachedTokens int64
}

type anthropicToolState struct {
	blockIdx int
	id       string
	name     string
	started  bool
	closed   bool
}

// NewAnthropicStreamConverter 创建 Anthropic 流式转换器。
func NewAnthropicStreamConverter(model string) *AnthropicStreamConverter {
	return &AnthropicStreamConverter{
		msgID:               "msg_" + protocol.RandomHex(16),
		model:               model,
		thinkingIdx:         -1,
		textIdx:             -1,
		openToolUpstreamIdx: -1,
		toolStates:          make(map[int]*anthropicToolState),
	}
}

// ProcessSSEData 处理单条 OpenAI SSE 的 data 载荷（不含 "data:" 前缀），返回生成的 Anthropic SSE 帧列表。
func (c *AnthropicStreamConverter) ProcessSSEData(data []byte) [][]byte {
	trimmed := strings.TrimSpace(string(data))
	if trimmed == "" {
		return nil
	}
	if trimmed == "[DONE]" {
		return c.Finish()
	}

	var chunk struct {
		ID      string `json:"id"`
		Model   string `json:"model"`
		Choices []struct {
			FinishReason string `json:"finish_reason"`
			Delta        struct {
				Content          string `json:"content"`
				ReasoningContent string `json:"reasoning_content"`
				Reasoning        string `json:"reasoning"`
				ToolCalls        []struct {
					Index    *int   `json:"index"`
					ID       string `json:"id"`
					Function struct {
						Name      string `json:"name"`
						Arguments string `json:"arguments"`
					} `json:"function"`
				} `json:"tool_calls"`
			} `json:"delta"`
		} `json:"choices"`
		Usage *struct {
			PromptTokens        int64 `json:"prompt_tokens"`
			CompletionTokens    int64 `json:"completion_tokens"`
			PromptTokensDetails struct {
				CachedTokens int64 `json:"cached_tokens"`
			} `json:"prompt_tokens_details"`
		} `json:"usage"`
	}
	if err := json.Unmarshal([]byte(trimmed), &chunk); err != nil {
		return nil
	}

	if chunk.ID != "" && !c.started {
		if strings.HasPrefix(chunk.ID, "msg_") {
			c.msgID = chunk.ID
		} else {
			c.msgID = "msg_" + chunk.ID
		}
	}
	if chunk.Model != "" && !c.started {
		c.model = chunk.Model
	}
	if chunk.Usage != nil {
		if chunk.Usage.PromptTokens > 0 {
			c.inputTokens = chunk.Usage.PromptTokens
		}
		if chunk.Usage.CompletionTokens > 0 {
			c.outputTokens = chunk.Usage.CompletionTokens
		}
		if chunk.Usage.PromptTokensDetails.CachedTokens > 0 {
			c.cachedTokens = chunk.Usage.PromptTokensDetails.CachedTokens
		}
	}

	var frames [][]byte
	if !c.started {
		frames = append(frames, c.emitMessageStart())
	}

	if len(chunk.Choices) > 0 {
		ch := chunk.Choices[0]
		if ch.FinishReason != "" {
			c.finishReason = ch.FinishReason
		}

		// 1. Thinking 增量
		reasoning := ch.Delta.ReasoningContent
		if reasoning == "" {
			reasoning = ch.Delta.Reasoning
		}
		if reasoning != "" {
			frames = append(frames, c.closeOpenTools()...)
			frames = append(frames, c.closeTextIfOpen()...)
			if !c.thinkingOpen {
				c.thinkingIdx = c.nextBlockIdx
				c.nextBlockIdx++
				c.thinkingOpen = true
				frames = append(frames, formatSSE("content_block_start", map[string]any{
					"type":  "content_block_start",
					"index": c.thinkingIdx,
					"content_block": map[string]any{
						"type":     "thinking",
						"thinking": "",
					},
				}))
			}
			frames = append(frames, formatSSE("content_block_delta", map[string]any{
				"type":  "content_block_delta",
				"index": c.thinkingIdx,
				"delta": map[string]any{
					"type":     "thinking_delta",
					"thinking": reasoning,
				},
			}))
		}

		// 2. Text 增量
		if ch.Delta.Content != "" {
			frames = append(frames, c.closeThinkingIfOpen()...)
			frames = append(frames, c.closeOpenTools()...)
			if !c.textOpen {
				c.textIdx = c.nextBlockIdx
				c.nextBlockIdx++
				c.textOpen = true
				frames = append(frames, formatSSE("content_block_start", map[string]any{
					"type":  "content_block_start",
					"index": c.textIdx,
					"content_block": map[string]any{
						"type": "text",
						"text": "",
					},
				}))
			}
			frames = append(frames, formatSSE("content_block_delta", map[string]any{
				"type":  "content_block_delta",
				"index": c.textIdx,
				"delta": map[string]any{
					"type": "text_delta",
					"text": ch.Delta.Content,
				},
			}))
		}

		// 3. Tool Call 增量
		for i, tc := range ch.Delta.ToolCalls {
			idx := i
			if tc.Index != nil {
				idx = *tc.Index
			}
			st, ok := c.toolStates[idx]
			if !ok {
				st = &anthropicToolState{}
				c.toolStates[idx] = st
			}
			if tc.ID != "" {
				st.id = tc.ID
			}
			if tc.Function.Name != "" {
				st.name = tc.Function.Name
			}

			if !st.started && (st.name != "" || st.id != "") {
				frames = append(frames, c.closeThinkingIfOpen()...)
				frames = append(frames, c.closeTextIfOpen()...)
				if c.openToolUpstreamIdx >= 0 && c.openToolUpstreamIdx != idx {
					frames = append(frames, c.closeOpenTools()...)
				}
				if st.id == "" {
					st.id = fmt.Sprintf("toolu_%s_%d", protocol.RandomHex(8), idx)
				}
				st.blockIdx = c.nextBlockIdx
				c.nextBlockIdx++
				st.started = true
				c.sawToolCall = true
				c.openToolUpstreamIdx = idx
				frames = append(frames, formatSSE("content_block_start", map[string]any{
					"type":  "content_block_start",
					"index": st.blockIdx,
					"content_block": map[string]any{
						"type":  "tool_use",
						"id":    st.id,
						"name":  st.name,
						"input": map[string]any{},
					},
				}))
			}

			if st.started && tc.Function.Arguments != "" {
				frames = append(frames, formatSSE("content_block_delta", map[string]any{
					"type":  "content_block_delta",
					"index": st.blockIdx,
					"delta": map[string]any{
						"type":         "input_json_delta",
						"partial_json": tc.Function.Arguments,
					},
				}))
			}
		}
	}
	return frames
}

// Finish 结束流式响应，补齐所有关闭块、message_delta 与 message_stop。
func (c *AnthropicStreamConverter) Finish() [][]byte {
	if c.finished {
		return nil
	}
	c.finished = true

	var frames [][]byte
	if !c.started {
		frames = append(frames, c.emitMessageStart())
	}
	frames = append(frames, c.closeThinkingIfOpen()...)
	frames = append(frames, c.closeTextIfOpen()...)
	frames = append(frames, c.closeOpenTools()...)

	usage := map[string]any{
		"output_tokens": c.outputTokens,
	}
	if c.inputTokens > 0 {
		usage["input_tokens"] = c.inputTokens
	}
	if c.cachedTokens > 0 {
		usage["cache_read_input_tokens"] = c.cachedTokens
	}

	frames = append(frames, formatSSE("message_delta", map[string]any{
		"type": "message_delta",
		"delta": map[string]any{
			"stop_reason":   mapOpenAIFinishToAnthropic(c.finishReason, c.sawToolCall),
			"stop_sequence": nil,
		},
		"usage": usage,
	}))
	frames = append(frames, formatSSE("message_stop", map[string]any{
		"type": "message_stop",
	}))
	return frames
}

func (c *AnthropicStreamConverter) emitMessageStart() []byte {
	c.started = true
	return formatSSE("message_start", map[string]any{
		"type": "message_start",
		"message": map[string]any{
			"id":            c.msgID,
			"type":          "message",
			"role":          "assistant",
			"model":         c.model,
			"content":       []any{},
			"stop_reason":   nil,
			"stop_sequence": nil,
			"usage": map[string]any{
				"input_tokens":  c.inputTokens,
				"output_tokens": 0,
			},
		},
	})
}

func (c *AnthropicStreamConverter) closeThinkingIfOpen() [][]byte {
	if !c.thinkingOpen {
		return nil
	}
	c.thinkingOpen = false
	return [][]byte{formatSSE("content_block_stop", map[string]any{
		"type":  "content_block_stop",
		"index": c.thinkingIdx,
	})}
}

func (c *AnthropicStreamConverter) closeTextIfOpen() [][]byte {
	if !c.textOpen {
		return nil
	}
	c.textOpen = false
	return [][]byte{formatSSE("content_block_stop", map[string]any{
		"type":  "content_block_stop",
		"index": c.textIdx,
	})}
}

func (c *AnthropicStreamConverter) closeOpenTools() [][]byte {
	if c.openToolUpstreamIdx < 0 {
		return nil
	}
	st, ok := c.toolStates[c.openToolUpstreamIdx]
	c.openToolUpstreamIdx = -1
	if !ok || !st.started || st.closed {
		return nil
	}
	st.closed = true
	return [][]byte{formatSSE("content_block_stop", map[string]any{
		"type":  "content_block_stop",
		"index": st.blockIdx,
	})}
}

func formatSSE(event string, payload any) []byte {
	b, _ := json.Marshal(payload)
	return []byte(fmt.Sprintf("event: %s\ndata: %s\n\n", event, string(b)))
}

// NowUnix 返回当前秒级时间戳（便于测试与构造默认响应）。
func NowUnix() int64 {
	return time.Now().Unix()
}
