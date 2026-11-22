package translator

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestAnthropicRequestAndResponseRoundTrip(t *testing.T) {
	reqJSON := `{
		"model": "glm-5.3-flash",
		"max_tokens": 2048,
		"stream": false,
		"system": [{"type": "text", "text": "你是一位代码助手"}],
		"tools": [{
			"name": "read_file",
			"description": "读取文件",
			"input_schema": {"type": "object", "properties": {"path": {"type": "string"}}}
		}],
		"tool_choice": {"type": "any"},
		"messages": [
			{"role": "user", "content": [{"type": "text", "text": "帮我看看 main.go"}]},
			{"role": "assistant", "content": [
				{"type": "thinking", "thinking": "先调用读取工具"},
				{"type": "tool_use", "id": "toolu_1", "name": "read_file", "input": {"path": "main.go"}}
			]},
			{"role": "user", "content": [
				{"type": "tool_result", "tool_use_id": "toolu_1", "content": "package main"}
			]}
		]
	}`

	oaiBytes, model, stream, err := AnthropicToOpenAIRequest([]byte(reqJSON))
	if err != nil {
		t.Fatalf("Anthropic 请求转换失败: %v", err)
	}
	if model != "glm-5.3-flash" || stream {
		t.Fatalf("模型或流式标志异常: model=%s stream=%v", model, stream)
	}

	var oai map[string]any
	if err := json.Unmarshal(oaiBytes, &oai); err != nil {
		t.Fatal(err)
	}
	if oai["tool_choice"] != "required" {
		t.Fatalf("tool_choice=any 应映射为 required，实际: %v", oai["tool_choice"])
	}
	msgs := oai["messages"].([]any)
	if len(msgs) != 4 {
		t.Fatalf("预期 4 条消息（system/user/assistant/tool），实际: %d", len(msgs))
	}
	if msgs[3].(map[string]any)["role"] != "tool" {
		t.Fatalf("tool_result 未转换为 role=tool: %+v", msgs[3])
	}

	// 反向非流式响应测试（含 reasoning_content 与 tool_calls）
	upstreamResp := `{
		"id": "chatcmpl-001",
		"model": "glm-5.3-flash",
		"choices": [{
			"finish_reason": "tool_calls",
			"message": {
				"role": "assistant",
				"reasoning_content": "思考过程",
				"content": "正在调用工具",
				"tool_calls": [{
					"id": "call_abc",
					"type": "function",
					"function": {"name": "read_file", "arguments": "{\"path\":\"go.mod\"}"}
				}]
			}
		}],
		"usage": {"prompt_tokens": 120, "completion_tokens": 45}
	}`
	anthBytes, err := OpenAIToAnthropicResponse([]byte(upstreamResp), "glm-5.3-flash")
	if err != nil {
		t.Fatalf("响应转 Anthropic 失败: %v", err)
	}
	var anth map[string]any
	_ = json.Unmarshal(anthBytes, &anth)
	if anth["stop_reason"] != "tool_use" {
		t.Fatalf("stop_reason 应为 tool_use，实际: %v", anth["stop_reason"])
	}
	blocks := anth["content"].([]any)
	if len(blocks) != 3 {
		t.Fatalf("预期 3 个内容块（thinking + text + tool_use），实际: %d", len(blocks))
	}
	if blocks[0].(map[string]any)["type"] != "thinking" || blocks[2].(map[string]any)["type"] != "tool_use" {
		t.Fatalf("内容块类型不符: %+v", blocks)
	}
}

func TestAnthropicStreamConversion(t *testing.T) {
	conv := NewAnthropicStreamConverter("glm-5.3-flash")
	chunks := []string{
		`{"id":"cmpl-1","model":"glm-5.3-flash","choices":[{"delta":{"reasoning_content":"先想一想"}}]}`,
		`{"id":"cmpl-1","model":"glm-5.3-flash","choices":[{"delta":{"content":"答案是 42"}}]}`,
		`{"id":"cmpl-1","model":"glm-5.3-flash","choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_1","function":{"name":"echo","arguments":"{\"msg\":"}}]}}]}`,
		`{"id":"cmpl-1","model":"glm-5.3-flash","choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\"ok\"}"}}]},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":10,"completion_tokens":20}}`,
		`[DONE]`,
	}
	var all strings.Builder
	for _, ch := range chunks {
		for _, frame := range conv.ProcessSSEData([]byte(ch)) {
			all.Write(frame)
		}
	}
	out := all.String()
	for _, want := range []string{
		"event: message_start",
		"thinking_delta",
		"text_delta",
		`"type":"tool_use"`,
		"input_json_delta",
		"event: message_delta",
		`"stop_reason":"tool_use"`,
		"event: message_stop",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("Anthropic 流式输出缺少关键片段 %q\n实际输出:\n%s", want, out)
		}
	}
}

func TestResponsesRequestResponseAndStream(t *testing.T) {
	reqJSON := `{
		"model": "openclaw",
		"instructions": "保持简洁",
		"max_output_tokens": 1024,
		"stream": true,
		"tools": [{"type": "function", "name": "get_time", "description": "获取时间", "parameters": {"type": "object"}}],
		"input": [
			{"type": "message", "role": "user", "content": [{"type": "input_text", "text": "几点了"}]},
			{"type": "function_call", "call_id": "call_9", "name": "get_time", "arguments": "{}"},
			{"type": "function_call_output", "call_id": "call_9", "output": "12:00"}
		]
	}`
	oaiBytes, model, stream, err := ResponsesToOpenAIRequest([]byte(reqJSON))
	if err != nil {
		t.Fatalf("Responses 请求转换失败: %v", err)
	}
	if model != "openclaw" || !stream {
		t.Fatalf("模型或 stream 解析异常: %s, %v", model, stream)
	}
	var oai map[string]any
	_ = json.Unmarshal(oaiBytes, &oai)
	if len(oai["messages"].([]any)) != 4 {
		t.Fatalf("预期 4 条消息，实际: %d", len(oai["messages"].([]any)))
	}

	// 非流式转换验证
	upstreamResp := `{"id":"c1","model":"openclaw","choices":[{"finish_reason":"stop","message":{"content":"现在 12:00"}}],"usage":{"prompt_tokens":15,"completion_tokens":5,"total_tokens":20}}`
	respBytes, err := OpenAIToResponsesResponse([]byte(upstreamResp), "openclaw")
	if err != nil {
		t.Fatalf("Responses 非流式转换失败: %v", err)
	}
	var respObj map[string]any
	_ = json.Unmarshal(respBytes, &respObj)
	if respObj["object"] != "response" || respObj["output_text"] != "现在 12:00" {
		t.Fatalf("Responses 响应对象字段不符: %+v", respObj)
	}

	// 流式转换验证
	conv := NewResponsesStreamConverter("openclaw")
	var streamOut strings.Builder
	for _, ch := range []string{
		`{"id":"c2","model":"openclaw","choices":[{"delta":{"content":"你好"}}]}`,
		`{"id":"c2","model":"openclaw","choices":[{"delta":{"content":"世界"},"finish_reason":"stop"}],"usage":{"prompt_tokens":5,"completion_tokens":4,"total_tokens":9}}`,
		`[DONE]`,
	} {
		for _, frame := range conv.ProcessSSEData([]byte(ch)) {
			streamOut.Write(frame)
		}
	}
	s := streamOut.String()
	for _, want := range []string{
		"event: response.created",
		"event: response.in_progress",
		"event: response.output_text.delta",
		"event: response.output_text.done",
		"event: response.completed",
	} {
		if !strings.Contains(s, want) {
			t.Fatalf("Responses 流式事件缺少 %q\n实际:\n%s", want, s)
		}
	}
}
