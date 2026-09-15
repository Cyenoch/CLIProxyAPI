package claude

import (
	"bytes"
	"context"
	"fmt"
	"testing"

	"github.com/tidwall/gjson"
)

func TestConvertClaudeRequestToInteractionsMapsMessagesToolsAndStream(t *testing.T) {
	raw := []byte(`{"model":"gemini-3.1-flash-lite","stream":true,"max_tokens":1024,"tools":[{"name":"get_weather","description":"Weather","input_schema":{"type":"object","properties":{"location":{"type":"string"}},"required":["location"]}}],"messages":[{"role":"user","content":[{"type":"text","text":"今天北京的天气怎么样？"}]}]}`)
	out := ConvertClaudeRequestToInteractions("gemini-3.1-flash-lite", raw, true)
	if got := gjson.GetBytes(out, "model").String(); got != "gemini-3.1-flash-lite" {
		t.Fatalf("model = %q, want gemini-3.1-flash-lite. Output: %s", got, string(out))
	}
	if !gjson.GetBytes(out, "stream").Bool() {
		t.Fatalf("stream should be true. Output: %s", string(out))
	}
	if got := gjson.GetBytes(out, "generation_config.max_output_tokens").Int(); got != 1024 {
		t.Fatalf("max_output_tokens = %d, want 1024. Output: %s", got, string(out))
	}
	if got := gjson.GetBytes(out, "input.0.type").String(); got != "user_input" {
		t.Fatalf("input.0.type = %q, want user_input. Output: %s", got, string(out))
	}
	if got := gjson.GetBytes(out, "input.0.content.0.text").String(); got != "今天北京的天气怎么样？" {
		t.Fatalf("input text = %q. Output: %s", got, string(out))
	}
	if got := gjson.GetBytes(out, "tools.0.parameters.properties.location.type").String(); got != "string" {
		t.Fatalf("tool schema was not mapped. Output: %s", string(out))
	}
	if got := gjson.GetBytes(out, "tools.0.type").String(); got != "function" {
		t.Fatalf("tools.0.type = %q, want function. Output: %s", got, string(out))
	}
}

func TestConvertClaudeRequestToInteractionsMapsToolUseAndResult(t *testing.T) {
	raw := []byte(`{"model":"gemini-3.1-flash-lite","messages":[{"role":"assistant","content":[{"type":"tool_use","id":"toolu_1","name":"get_weather","input":{"location":"北京"}}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_1","is_error":true,"content":"晴"}]}]}`)
	out := ConvertClaudeRequestToInteractions("gemini-3.1-flash-lite", raw, false)
	if got := gjson.GetBytes(out, "input.0.type").String(); got != "function_call" {
		t.Fatalf("input.0.type = %q, want function_call. Output: %s", got, string(out))
	}
	if got := gjson.GetBytes(out, "input.0.call_id").String(); got != "toolu_1" {
		t.Fatalf("call_id = %q, want toolu_1. Output: %s", got, string(out))
	}
	if gjson.GetBytes(out, "input.0.id").Exists() {
		t.Fatalf("function_call id should be omitted. Output: %s", string(out))
	}
	if got := gjson.GetBytes(out, "input.1.type").String(); got != "function_result" {
		t.Fatalf("input.1.type = %q, want function_result. Output: %s", got, string(out))
	}
	if got := gjson.GetBytes(out, "input.1.result").String(); got != "晴" {
		t.Fatalf("result = %q, want 晴. Output: %s", got, string(out))
	}
	if gjson.GetBytes(out, "input.1.id").Exists() {
		t.Fatalf("function_result id should be omitted. Output: %s", string(out))
	}
	if got := gjson.GetBytes(out, "input.1.call_id").String(); got != "toolu_1" {
		t.Fatalf("result call_id = %q, want toolu_1. Output: %s", got, string(out))
	}
	if !gjson.GetBytes(out, "input.1.is_error").Bool() {
		t.Fatalf("tool result error flag missing. Output: %s", string(out))
	}
}

func TestConvertClaudeRequestToInteractions_PreservesToolAdjacencyWithInterveningSystemMessage(t *testing.T) {
	raw := []byte(`{
		"model": "gemini-3.1-flash-lite",
		"messages": [
			{"role": "user", "content": [{"type": "text", "text": "Execute tools"}]},
			{
				"role": "assistant",
				"content": [
					{"type": "tool_use", "id": "call_1", "name": "tool_one", "input": {"a": 1}},
					{"type": "tool_use", "id": "call_2", "name": "tool_two", "input": {"b": 2}}
				]
			},
			{"role": "system", "content": "Context reminder between tool_use and tool_result"},
			{
				"role": "user",
				"content": [
					{"type": "tool_result", "tool_use_id": "call_2", "content": "result 2"},
					{"type": "tool_result", "tool_use_id": "call_1", "content": "result 1"},
					{"type": "text", "text": "Now summarize"}
				]
			}
		]
	}`)
	out := ConvertClaudeRequestToInteractions("gemini-3.1-flash-lite", raw, false)
	inputs := gjson.GetBytes(out, "input").Array()

	// Expected types:
	// 0: user_input ("Execute tools")
	// 1: function_call (call_1)
	// 2: function_call (call_2)
	// 3: function_result (call_1)
	// 4: function_result (call_2)
	// 5: user_input (<system-reminder>...)
	// 6: user_input ("Now summarize")
	types := make([]string, 0, len(inputs))
	for _, item := range inputs {
		types = append(types, item.Get("type").String())
	}
	wantTypes := []string{"user_input", "function_call", "function_call", "function_result", "function_result", "user_input", "user_input"}
	if len(types) != len(wantTypes) {
		t.Fatalf("unexpected step count %d: got %v, want %v. Output: %s", len(types), types, wantTypes, string(out))
	}
	for i, wt := range wantTypes {
		if types[i] != wt {
			t.Fatalf("step %d type = %q, want %q", i, types[i], wt)
		}
	}

	if inputs[3].Get("call_id").String() != "call_1" {
		t.Fatalf("expected result 0 to respond to call_1, got %q", inputs[3].Get("call_id").String())
	}
	if inputs[4].Get("call_id").String() != "call_2" {
		t.Fatalf("expected result 1 to respond to call_2, got %q", inputs[4].Get("call_id").String())
	}
	if inputs[5].Get("content.0.text").String() != "<system-reminder>\nContext reminder between tool_use and tool_result\n</system-reminder>" {
		t.Fatalf("unexpected system reminder content: %q", inputs[5].Get("content.0.text").String())
	}
	if inputs[6].Get("content.0.text").String() != "Now summarize" {
		t.Fatalf("unexpected user text: %q", inputs[6].Get("content.0.text").String())
	}
}

func TestConvertInteractionsResponseToClaudeStream(t *testing.T) {
	var param any
	var out [][]byte
	chunks := [][]byte{
		[]byte(`event: interaction.created
data: {"interaction":{"id":"interaction_1","model":"gemini-3.1-flash-lite"},"event_type":"interaction.created"}`),
		[]byte(`event: step.start
data: {"index":0,"step":{"type":"model_output"},"event_type":"step.start"}`),
		[]byte(`event: step.delta
data: {"index":0,"delta":{"type":"text","text":"北京今天晴"},"event_type":"step.delta"}`),
		[]byte(`event: step.stop
data: {"index":0,"event_type":"step.stop"}`),
		[]byte(`event: interaction.completed
data: {"interaction":{"id":"interaction_1","model":"gemini-3.1-flash-lite","usage":{"total_input_tokens":3,"total_output_tokens":4}},"event_type":"interaction.completed"}`),
		[]byte(`event: done
data: [DONE]`),
	}
	for _, chunk := range chunks {
		out = append(out, ConvertInteractionsResponseToClaude(context.Background(), "gemini-3.1-flash-lite", nil, nil, chunk, &param)...)
	}
	if payload := findClaudeEventPayload(out, "message_start"); gjson.GetBytes(payload, "message.model").String() != "gemini-3.1-flash-lite" {
		t.Fatalf("message_start payload = %s", payload)
	}
	if payload := findClaudeEventPayload(out, "content_block_delta"); gjson.GetBytes(payload, "delta.text").String() != "北京今天晴" {
		t.Fatalf("content_block_delta payload = %s", payload)
	}
	if payload := findClaudeEventPayload(out, "message_delta"); gjson.GetBytes(payload, "usage.output_tokens").Int() != 4 {
		t.Fatalf("message_delta payload = %s", payload)
	}
	if payload := findClaudeEventPayload(out, "message_stop"); gjson.GetBytes(payload, "type").String() != "message_stop" {
		t.Fatalf("message_stop payload = %s", payload)
	}
}

func TestConvertInteractionsResponseToClaudeStreamToolCall(t *testing.T) {
	var param any
	var out [][]byte
	chunks := [][]byte{
		[]byte(`data: {"interaction":{"id":"interaction_1","model":"gemini-3.1-flash-lite"},"event_type":"interaction.created"}`),
		[]byte(`data: {"index":0,"step":{"type":"function_call","id":"toolu_1","signature":"sig_1","name":"get_weather","arguments":{}},"event_type":"step.start"}`),
		[]byte(`data: {"index":0,"delta":{"type":"arguments_delta","arguments":"{\"location\":\"北京\"}"},"event_type":"step.delta"}`),
		[]byte(`data: {"index":0,"event_type":"step.stop"}`),
		[]byte(`data: {"interaction":{"usage":{"total_input_tokens":1,"total_output_tokens":2}},"event_type":"interaction.completed"}`),
	}
	for _, chunk := range chunks {
		out = append(out, ConvertInteractionsResponseToClaude(context.Background(), "gemini-3.1-flash-lite", nil, nil, chunk, &param)...)
	}
	if payload := findClaudeEventPayload(out, "content_block_start"); gjson.GetBytes(payload, "content_block.type").String() != "tool_use" {
		t.Fatalf("content_block_start payload = %s", payload)
	}
	if payload := findClaudeEventPayload(out, "content_block_start"); gjson.GetBytes(payload, "content_block.signature").String() != "sig_1" {
		t.Fatalf("content_block_start signature payload = %s", payload)
	}
	if payload := findClaudeEventPayload(out, "content_block_delta"); gjson.GetBytes(payload, "delta.partial_json").String() != `{"location":"北京"}` {
		t.Fatalf("content_block_delta payload = %s", payload)
	}
	if payload := findClaudeEventPayload(out, "message_delta"); gjson.GetBytes(payload, "delta.stop_reason").String() != "tool_use" {
		t.Fatalf("message_delta payload = %s", payload)
	}
}

func TestConvertInteractionsResponseToClaudeStreamFinishMetadataUsage(t *testing.T) {
	var param any
	out := ConvertInteractionsResponseToClaude(context.Background(), "claude-test", nil, nil, []byte(`data: {"event_type":"finish","metadata":{"total_usage":{"total_input_tokens":2,"total_output_tokens":6,"total_tokens":8}}}`), &param)
	payload := findClaudeEventPayload(out, "message_delta")
	if len(payload) == 0 {
		t.Fatalf("message_delta payload not found")
	}
	if got := gjson.GetBytes(payload, "usage.input_tokens").Int(); got != 2 {
		t.Fatalf("input_tokens = %d, want 2. Payload: %s", got, string(payload))
	}
	if got := gjson.GetBytes(payload, "usage.output_tokens").Int(); got != 6 {
		t.Fatalf("output_tokens = %d, want 6. Payload: %s", got, string(payload))
	}
}

func TestConvertInteractionsResponseToClaudeNonStream(t *testing.T) {
	raw := []byte(`{"id":"interaction_1","model":"gemini-3.1-flash-lite","steps":[{"type":"model_output","content":[{"type":"text","text":"ok"}]},{"type":"function_call","call_id":"toolu_1","signature":"sig_1","name":"lookup","arguments":{"q":"x"}}],"usage":{"total_input_tokens":3,"total_output_tokens":4}}`)
	out := ConvertInteractionsResponseToClaudeNonStream(context.Background(), "gemini-3.1-flash-lite", nil, nil, raw, nil)
	if got := gjson.GetBytes(out, "content.0.text").String(); got != "ok" {
		t.Fatalf("text = %q, want ok. Output: %s", got, string(out))
	}
	if got := gjson.GetBytes(out, "content.1.type").String(); got != "tool_use" {
		t.Fatalf("tool block type = %q, want tool_use. Output: %s", got, string(out))
	}
	if got := gjson.GetBytes(out, "content.1.signature").String(); got != "sig_1" {
		t.Fatalf("tool signature = %q, want sig_1. Output: %s", got, string(out))
	}
	if got := gjson.GetBytes(out, "stop_reason").String(); got != "tool_use" {
		t.Fatalf("stop_reason = %q, want tool_use. Output: %s", got, string(out))
	}
	if got := gjson.GetBytes(out, "usage.input_tokens").Int(); got != 3 {
		t.Fatalf("input_tokens = %d, want 3. Output: %s", got, string(out))
	}
}

func TestConvertInteractionsResponseToClaudePreservesMaxTokensStopReason(t *testing.T) {
	raw := []byte(`{"id":"interaction_1","model":"claude-test","status":"incomplete","stop_reason":"max_tokens","steps":[{"type":"model_output","content":[{"type":"text","text":"partial"}]}]}`)
	out := ConvertInteractionsResponseToClaudeNonStream(context.Background(), "claude-test", nil, nil, raw, nil)
	if got := gjson.GetBytes(out, "stop_reason").String(); got != "max_tokens" {
		t.Fatalf("stop_reason = %q, want max_tokens. Output: %s", got, string(out))
	}

	var param any
	chunks := ConvertInteractionsResponseToClaude(context.Background(), "claude-test", nil, nil, []byte(`data: {"event_type":"interaction.completed","interaction":{"id":"interaction_1","status":"incomplete","stop_reason":"max_tokens"}}`), &param)
	payload := findClaudeEventPayload(chunks, "message_delta")
	if got := gjson.GetBytes(payload, "delta.stop_reason").String(); got != "max_tokens" {
		t.Fatalf("stream stop_reason = %q, want max_tokens. Payload: %s", got, string(payload))
	}
}

func findClaudeEventPayload(events [][]byte, eventName string) []byte {
	prefix := []byte("data:")
	for _, event := range events {
		if !bytes.Contains(event, []byte("event: "+eventName)) {
			continue
		}
		for _, line := range bytes.Split(event, []byte("\n")) {
			line = bytes.TrimSpace(line)
			if bytes.HasPrefix(line, prefix) {
				return bytes.TrimSpace(line[len(prefix):])
			}
		}
	}
	return nil
}

func TestConvertInteractionsResponseToClaudeInterleavedTools(t *testing.T) {
	for _, explicitStops := range []bool{true, false} {
		t.Run(fmt.Sprint(explicitStops), func(t *testing.T) {
			chunks := []string{
				`{"event_type":"step.start","index":0,"step":{"type":"model_output"}}`,
				`{"event_type":"step.delta","index":0,"delta":{"type":"text","text":"Calling tools"}}`,
				`{"event_type":"step.stop","index":0}`,
				`{"event_type":"step.start","index":1,"step":{"type":"function_call","id":"call_a","name":"a"}}`,
				`{"event_type":"step.delta","index":1,"delta":{"type":"arguments_delta","arguments":"{\"a\":"}}`,
				`{"event_type":"step.start","index":2,"step":{"type":"function_call","id":"call_b","name":"b"}}`,
				`{"event_type":"step.delta","index":2,"delta":{"type":"arguments_delta","arguments":"{\"b\":"}}`,
				`{"event_type":"step.delta","index":1,"delta":{"type":"arguments_delta","arguments":"1}"}}`,
				`{"event_type":"step.delta","index":2,"delta":{"type":"arguments_delta","arguments":"2}"}}`,
			}
			if explicitStops {
				chunks = append(chunks, `{"event_type":"step.stop","index":2}`, `{"event_type":"step.stop","index":1}`)
			}
			chunks = append(chunks, `{"event_type":"interaction.completed"}`, `[DONE]`)
			var param any
			starts := map[int]string{}
			args := map[int]string{}
			closed := map[int]bool{}
			for _, chunk := range chunks {
				for _, event := range ConvertInteractionsResponseToClaude(context.Background(), "devin/swe-2", nil, nil, []byte(chunk), &param) {
					root := gjson.ParseBytes(interactionsSSEPayload(event))
					index := int(root.Get("index").Int())
					switch root.Get("type").String() {
					case "content_block_start":
						if _, exists := starts[index]; exists {
							t.Fatalf("duplicate block %d", index)
						}
						starts[index] = root.Get("content_block.id").String()
					case "content_block_delta":
						if _, exists := starts[index]; !exists || closed[index] {
							t.Fatalf("delta outside open block %d: %s", index, event)
						}
						args[index] += root.Get("delta.partial_json").String()
					case "content_block_stop":
						if _, exists := starts[index]; !exists || closed[index] {
							t.Fatalf("invalid stop %d", index)
						}
						closed[index] = true
					}
				}
			}
			if len(starts) != 3 || len(closed) != 3 || starts[1] != "call_a" || starts[2] != "call_b" || args[1] != `{"a":1}` || args[2] != `{"b":2}` {
				t.Fatalf("starts=%v closed=%v arguments=%v", starts, closed, args)
			}
		})
	}
}

func TestInteractionsClaudeCacheUsage(t *testing.T) {
	for _, usage := range []string{`{"input_tokens":11,"output_tokens":7,"cached_tokens":100,"cache_write_tokens":13,"total_input_tokens":124,"total_output_tokens":7,"total_tokens":131}`, `{"total_input_tokens":124,"total_output_tokens":7,"total_cached_tokens":100,"total_cache_write_tokens":13,"total_tokens":131}`} {
		raw := []byte(`{"id":"i1","steps":[],"usage":` + usage + `}`)
		nonstream := ConvertInteractionsResponseToClaudeNonStream(context.Background(), "devin/swe-2", nil, nil, raw, nil)
		var param any
		out := ConvertInteractionsResponseToClaude(context.Background(), "devin/swe-2", nil, nil, []byte(`data: {"event_type":"interaction.completed","interaction":`+string(raw)+`}`), &param)
		for _, result := range []gjson.Result{gjson.GetBytes(nonstream, "usage"), gjson.GetBytes(findClaudeEventPayload(out, "message_delta"), "usage")} {
			for path, want := range map[string]int64{"input_tokens": 11, "output_tokens": 7, "cache_read_input_tokens": 100, "cache_creation_input_tokens": 13} {
				if got := result.Get(path).Int(); got != want {
					t.Errorf("%s = %d, want %d; usage=%s", path, got, want, result.Raw)
				}
			}
		}
	}

}
