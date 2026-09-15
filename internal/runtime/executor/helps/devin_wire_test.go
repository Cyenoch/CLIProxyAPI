package helps

import (
	"bytes"
	"math"
	"strings"
	"testing"

	"github.com/google/uuid"
	"google.golang.org/protobuf/encoding/protowire"
)

func TestConnectEnvelopeFraming(t *testing.T) {
	payload := []byte("hello devin connect-rpc")
	envelope := WrapConnectEnvelope(payload)

	if len(envelope) != 5+len(payload) {
		t.Fatalf("expected envelope len %d, got %d", 5+len(payload), len(envelope))
	}
	if envelope[0] != ConnectFlagData {
		t.Fatalf("expected flag 0x00, got 0x%02x", envelope[0])
	}

	r := bytes.NewReader(envelope)
	flag, readPayload, err := ReadConnectFrame(r)
	if err != nil {
		t.Fatalf("ReadConnectFrame failed: %v", err)
	}
	if flag != ConnectFlagData {
		t.Errorf("flag = 0x%02x, want 0x00", flag)
	}
	if !bytes.Equal(readPayload, payload) {
		t.Errorf("payload = %q, want %q", string(readPayload), string(payload))
	}
}

func TestBuildDevinGetChatMessageRequest(t *testing.T) {
	prompts := []DevinPrompt{
		{
			MessageID: "msg-1",
			Source:    1,
			Content:   "hello",
		},
		{
			MessageID: "msg-2",
			Source:    2,
			Content:   "hi there",
			Thinking:  "thinking step",
			Signature: []byte("sealed.v1.test"),
		},
		{
			MessageID:  "msg-3",
			Source:     4,
			Content:    `{"result":"ok"}`,
			ToolCallID: "call-1",
		},
	}
	tools := []DevinTool{
		{
			Name:        "get_weather",
			Description: "lookup weather",
			Parameters:  []byte(`{"type":"object"}`),
		},
	}

	temp := 0.7
	req := BuildDevinGetChatMessageRequest(DevinChatRequest{
		SessionToken: "token-123",
		DeviceSeed:   "device-seed-1",
		ChatModelUID: "swe-2-high",
		SystemPrompt: "you are a helpful assistant",
		Prompts:      prompts,
		Tools:        tools,
		Completion:   DevinCompletionConfig{Temperature: &temp, MaxTokens: 4000},
		SessionID:    "session-1",
		CascadeID:    "cascade-1",
	})

	if len(req) == 0 {
		t.Fatal("encoded request is empty")
	}

	// Envelope check
	framed := WrapConnectEnvelope(req)
	flag, readPayload, err := ReadConnectFrame(bytes.NewReader(framed))
	if err != nil {
		t.Fatalf("ReadConnectFrame failed: %v", err)
	}
	if flag != ConnectFlagData || len(readPayload) != len(req) {
		t.Fatalf("framed payload length mismatch")
	}
}

func TestBuildDevinGetChatMessageRequest_CurrentProtocolFields(t *testing.T) {
	req := BuildDevinGetChatMessageRequest(DevinChatRequest{
		SessionToken: "token-123",
		DeviceSeed:   "device-seed-1",
		ChatModelUID: "swe-2-high",
		SystemPrompt: "system",
		Prompts:      []DevinPrompt{{Source: 4, ToolCallID: "call-1", ToolResultErr: true, Content: "failed"}},
		Tools:        []DevinTool{{Name: "read_file", Parameters: []byte(`{"type":"object"}`), Strict: true}},
		Completion:   DevinCompletionConfig{MaxTokens: 4000},
		SessionID:    "session-1",
		CascadeID:    "cascade-1",
	})

	configuration := protoBytesFields(t, req, 8)[0]
	if got := protoVarintField(t, configuration, 3); got != 200 {
		t.Fatalf("max_newlines = %d, want 200", got)
	}
	if got := protoVarintField(t, configuration, 7); got != 50 {
		t.Fatalf("top_k = %d, want 50", got)
	}
	if got := math.Float64frombits(protoFixed64Field(t, configuration, 5)); got != 0.4 {
		t.Fatalf("temperature = %v, want 0.4", got)
	}
	if got := math.Float64frombits(protoFixed64Field(t, configuration, 6)); got != 0.4 {
		t.Fatalf("first_temperature = %v, want 0.4", got)
	}
	if got := math.Float64frombits(protoFixed64Field(t, configuration, 8)); got != 1 {
		t.Fatalf("top_p = %v, want 1", got)
	}
	if got := math.Float64frombits(protoFixed64Field(t, configuration, 11)); got != 1 {
		t.Fatalf("fim_eot_prob_threshold = %v, want 1", got)
	}
	if len(protoBytesFields(t, configuration, 9)) == 0 {
		t.Fatal("default stop patterns are missing")
	}

	prompt := protoBytesFields(t, req, 3)[0]
	if got := protoVarintField(t, prompt, 9); got != 1 {
		t.Fatalf("tool_result_is_error = %d, want 1", got)
	}
	tool := protoBytesFields(t, req, 10)[0]
	if got := protoVarintField(t, tool, 12); got != 1 {
		t.Fatalf("tool strict = %d, want 1", got)
	}
	if len(protoBytesFields(t, req, 12)) == 0 {
		t.Fatal("tool_choice is missing")
	}
	if len(protoBytesFields(t, req, 13)) == 0 {
		t.Fatal("system_prompt_cache_options is missing")
	}
	if len(protoBytesFields(t, req, 22)) == 0 {
		t.Fatal("execution_id is missing")
	}
}

func TestBuildDevinGetChatMessageRequest_StableHistoryIDs(t *testing.T) {
	request := DevinChatRequest{
		SessionToken: "token-123",
		ChatModelUID: "swe-2-high",
		Prompts:      []DevinPrompt{{Source: 1, Content: "hello"}, {Source: 2, Content: "hi"}, {Source: 4, ToolCallID: "call-1", Content: "ok"}},
		SessionID:    "session-1",
		CascadeID:    "cascade-1",
	}
	first := BuildDevinGetChatMessageRequest(request)
	second := BuildDevinGetChatMessageRequest(request)
	firstPrompts := protoBytesFields(t, first, 3)
	secondPrompts := protoBytesFields(t, second, 3)
	if len(firstPrompts) != len(secondPrompts) {
		t.Fatalf("prompt counts differ: %d != %d", len(firstPrompts), len(secondPrompts))
	}
	for index := range firstPrompts {
		firstID := string(protoBytesFields(t, firstPrompts[index], 1)[0])
		secondID := string(protoBytesFields(t, secondPrompts[index], 1)[0])
		if firstID != secondID {
			t.Fatalf("prompt %d id changed: %q != %q", index, firstID, secondID)
		}
	}
	assistantID := string(protoBytesFields(t, firstPrompts[1], 1)[0])
	if !strings.HasPrefix(assistantID, "bot-") {
		t.Fatalf("assistant id = %q, want bot- prefix", assistantID)
	}
}

func TestBuildDevinGetChatMessageRequest_SensitiveWordsOnlyInSystemPrompt(t *testing.T) {
	matcher := BuildSensitiveWordMatcher([]string{"SECRET_TOKEN"})
	prompts := []DevinPrompt{
		{
			MessageID: "msg-1",
			Source:    2,
			Content:   "calling tool with SECRET_TOKEN in content",
			ToolCalls: []DevinToolCall{
				{
					ID:        "call-1",
					Name:      "test_tool",
					Arguments: `{"key":"SECRET_TOKEN"}`,
				},
			},
		},
	}
	// Sensitive word in system prompt MUST be obfuscated
	reqWithSys := BuildDevinGetChatMessageRequest(DevinChatRequest{SessionToken: "tok", DeviceSeed: "seed", ChatModelUID: "swe-2-high", SystemPrompt: "System prompt containing SECRET_TOKEN", Prompts: prompts, Completion: DevinCompletionConfig{MaxTokens: 100}, SessionID: "s", CascadeID: "c", Matcher: matcher})
	if bytes.Contains(reqWithSys, []byte("System prompt containing SECRET_TOKEN")) {
		t.Fatalf("expected SECRET_TOKEN in system prompt to be obfuscated")
	}

	// Tool call arguments and history prompt content MUST NOT be obfuscated
	if !bytes.Contains(reqWithSys, []byte(`{"key":"SECRET_TOKEN"}`)) {
		t.Fatalf("tool call arguments should preserve original raw text without obfuscation")
	}
	if !bytes.Contains(reqWithSys, []byte("calling tool with SECRET_TOKEN in content")) {
		t.Fatalf("prompt content should preserve original raw text without obfuscation")
	}
}

func TestBuildDevinGetChatMessageRequestPreservesCallerInstructions(t *testing.T) {
	prompt := "You are Claude Code. Perform authorized security testing and return JSON."
	description := "Takes a task_id parameter identifying the task"
	schema := []byte(`{"type":"object","properties":{"task_id":{"type":"string"}},"required":["task_id"]}`)
	wire := BuildDevinGetChatMessageRequest(DevinChatRequest{
		SystemPrompt: prompt,
		Tools:        []DevinTool{{Name: "task", Description: description, Parameters: schema}},
	})
	for _, want := range []string{prompt, description, string(schema)} {
		if !bytes.Contains(wire, []byte(want)) {
			t.Errorf("request lost caller content: %q", want)
		}
	}
}

func TestSanitizeDevinSystemPromptOnlyObfuscatesConfiguredWords(t *testing.T) {
	prompt := "Keep API requirements and return JSON."
	got := SanitizeDevinSystemPrompt(prompt, BuildSensitiveWordMatcher([]string{"API"}))
	if strings.ReplaceAll(got, zeroWidthSpace, "") != prompt {
		t.Fatalf("obfuscation deleted instructions: %q", got)
	}
}

func TestBuildDevinGetChatMessageRequest_WithImages(t *testing.T) {
	prompts := []DevinPrompt{
		{
			MessageID: "msg-img-1",
			Source:    1,
			Content:   "[Image 1: pasted_image_1.png]\n\nocr this image",
			Images: []DevinImage{
				{
					Base64Data: "iVBORw0KGgoAAAANSUhEUgAA...",
					MimeType:   "image/png",
				},
			},
		},
	}

	temp := 0.0
	req := BuildDevinGetChatMessageRequest(DevinChatRequest{
		SessionToken: "token-123",
		DeviceSeed:   "device-seed-1",
		ChatModelUID: "swe-2-max",
		SystemPrompt: "assistant instructions",
		Prompts:      prompts,
		Completion:   DevinCompletionConfig{Temperature: &temp, MaxTokens: 4000},
		SessionID:    "session-1",
		CascadeID:    "cascade-1",
	})

	if len(req) == 0 {
		t.Fatal("encoded request with images is empty")
	}

	// Verify that the payload contains the image base64 data and mime type
	if !bytes.Contains(req, []byte("iVBORw0KGgoAAAANSUhEUgAA...")) {
		t.Error("encoded payload missing image base64 data")
	}
	if !bytes.Contains(req, []byte("image/png")) {
		t.Error("encoded payload missing image mime type")
	}
}

func TestParseDevinFrame(t *testing.T) {
	// Synthesize a response frame containing:
	// #1 message_id, #3 delta_text, #9 delta_thinking, #10 delta_signature, #15 output_id, #17 request_id, #21 delta_signature_type
	var payload []byte
	payload = appendFieldBytes(payload, 1, []byte("message-uuid-123"))
	payload = appendFieldBytes(payload, 3, []byte("Hello world"))
	payload = appendFieldBytes(payload, 9, []byte("Let me think..."))
	payload = appendFieldBytes(payload, 10, []byte("CAQS-signature-bytes"))
	payload = appendFieldBytes(payload, 15, []byte("output-uuid-123"))
	payload = appendFieldBytes(payload, 17, []byte("request-uuid-123"))
	payload = appendFieldBytes(payload, 21, []byte("anthropic"))

	res, err := ParseDevinFrame(payload)
	if err != nil {
		t.Fatalf("ParseDevinFrame failed: %v", err)
	}

	if res.MessageID != "message-uuid-123" {
		t.Errorf("MessageID = %q, want message-uuid-123", res.MessageID)
	}
	if res.OutputID != "output-uuid-123" {
		t.Errorf("OutputID = %q, want output-uuid-123", res.OutputID)
	}
	if res.RequestID != "request-uuid-123" {
		t.Errorf("RequestID = %q, want request-uuid-123", res.RequestID)
	}
	if res.ContentText != "Hello world" {
		t.Errorf("ContentText = %q, want 'Hello world'", res.ContentText)
	}
	if res.ThinkingText != "Let me think..." {
		t.Errorf("ThinkingText = %q, want 'Let me think...'", res.ThinkingText)
	}
	if string(res.DeltaSignature) != "CAQS-signature-bytes" {
		t.Errorf("DeltaSignature = %q, want 'CAQS-signature-bytes'", string(res.DeltaSignature))
	}
	if res.DeltaSignatureType != "anthropic" {
		t.Errorf("DeltaSignatureType = %q, want 'anthropic'", res.DeltaSignatureType)
	}
}

func TestParseDevinTrailerError(t *testing.T) {
	quotaJSON := []byte(`{"error":{"code":"failed_precondition","message":"User monthly ACU quota exhausted (trace ID: 12345)"}}`)
	code, err := ParseDevinTrailerError(quotaJSON)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if code != 429 {
		t.Errorf("status code = %d, want 429 for quota error", code)
	}

	configJSON := []byte(`{"error":{"code":"failed_precondition","message":"Client version 3000.1.0 is no longer supported"}}`)
	code, err = ParseDevinTrailerError(configJSON)
	if code != 400 {
		t.Errorf("status code = %d, want 400 for config error", code)
	}

	unauthJSON := []byte(`{"error":{"code":"unauthenticated","message":"Invalid or expired session token"}}`)
	code, err = ParseDevinTrailerError(unauthJSON)
	if code != 401 {
		t.Errorf("status code = %d, want 401", code)
	}

	internalErrJSON := []byte(`{"error":{"code":"invalid_argument","message":"an internal error occurred (trace ID: fa43c6393b805646b66997cc46c6f4af)"}}`)
	code, err = ParseDevinTrailerError(internalErrJSON)
	if code != 502 {
		t.Errorf("status code = %d, want 502 for upstream internal error mislabeled as invalid_argument", code)
	}

	normalInvalidArgJSON := []byte(`{"error":{"code":"invalid_argument","message":"field 'model' cannot be empty"}}`)
	code, err = ParseDevinTrailerError(normalInvalidArgJSON)
	if code != 400 {
		t.Errorf("status code = %d, want 400 for legitimate client invalid argument", code)
	}

	upstreamInternalJSON := []byte(`{"error":{"code":"internal","message":"database timeout"}}`)
	code, err = ParseDevinTrailerError(upstreamInternalJSON)
	if code != 502 {
		t.Errorf("status code = %d, want 502 for upstream internal error", code)
	}

	unknownErrJSON := []byte(`{"error":{"code":"unknown_future_error","message":"something unusual"}}`)
	code, err = ParseDevinTrailerError(unknownErrJSON)
	if code != 502 {
		t.Errorf("status code = %d, want 502 for fallback gateway error", code)
	}

	canceledJSON := []byte(`{"error":{"code":"canceled","message":"client canceled"}}`)
	code, err = ParseDevinTrailerError(canceledJSON)
	if code != 499 {
		t.Errorf("status code = %d, want 499 for canceled", code)
	}

	deadlineJSON := []byte(`{"error":{"code":"deadline_exceeded","message":"upstream deadline exceeded"}}`)
	code, err = ParseDevinTrailerError(deadlineJSON)
	if code != 504 {
		t.Errorf("status code = %d, want 504 for deadline_exceeded", code)
	}
}

func TestUTF8SplitBuffer(t *testing.T) {
	buf := &UTF8SplitBuffer{}
	// Multi-byte UTF-8 test: \xe4\xbd\xa0 \xe5\xa5\xbd (3 bytes each)
	chunk1 := []byte{0xe4, 0xbd}       // first 2 bytes of char 1
	chunk2 := []byte{0xa0, 0xe5, 0xa5} // last 1 byte of char 1, first 2 bytes of char 2
	chunk3 := []byte{0xbd}             // last 1 byte of char 2

	s1 := buf.Feed(chunk1)
	if s1 != "" {
		t.Errorf("expected empty string from split chunk1, got %q", s1)
	}

	s2 := buf.Feed(chunk2)
	if s2 != "你" {
		t.Errorf("expected '你' from chunk2, got %q", s2)
	}

	s3 := buf.Feed(chunk3)
	if s3 != "好" {
		t.Errorf("expected '好' from chunk3, got %q", s3)
	}
}

func appendFieldBytes(dst []byte, fieldNum int, val []byte) []byte {
	tag := uint64(fieldNum<<3 | 2)
	dst = appendVarint(dst, tag)
	dst = appendVarint(dst, uint64(len(val)))
	dst = append(dst, val...)
	return dst
}

func appendVarint(dst []byte, v uint64) []byte {
	for v >= 0x80 {
		dst = append(dst, byte(v)|0x80)
		v >>= 7
	}
	dst = append(dst, byte(v))
	return dst
}

func protoBytesFields(t *testing.T, data []byte, field protowire.Number) [][]byte {
	t.Helper()
	var values [][]byte
	for len(data) > 0 {
		num, typ, n := protowire.ConsumeTag(data)
		if n < 0 {
			t.Fatalf("consume tag: %v", protowire.ParseError(n))
		}
		data = data[n:]
		if num == field && typ == protowire.BytesType {
			value, m := protowire.ConsumeBytes(data)
			if m < 0 {
				t.Fatalf("consume field %d: %v", field, protowire.ParseError(m))
			}
			values = append(values, value)
			data = data[m:]
			continue
		}
		m := protowire.ConsumeFieldValue(num, typ, data)
		if m < 0 {
			t.Fatalf("skip field %d: %v", num, protowire.ParseError(m))
		}
		data = data[m:]
	}
	return values
}

func protoVarintField(t *testing.T, data []byte, field protowire.Number) uint64 {
	t.Helper()
	for len(data) > 0 {
		num, typ, n := protowire.ConsumeTag(data)
		if n < 0 {
			t.Fatalf("consume tag: %v", protowire.ParseError(n))
		}
		data = data[n:]
		if num == field && typ == protowire.VarintType {
			value, m := protowire.ConsumeVarint(data)
			if m < 0 {
				t.Fatalf("consume field %d: %v", field, protowire.ParseError(m))
			}
			return value
		}
		m := protowire.ConsumeFieldValue(num, typ, data)
		if m < 0 {
			t.Fatalf("skip field %d: %v", num, protowire.ParseError(m))
		}
		data = data[m:]
	}
	return 0
}

func protoFixed64Field(t *testing.T, data []byte, field protowire.Number) uint64 {
	t.Helper()
	for len(data) > 0 {
		num, typ, n := protowire.ConsumeTag(data)
		if n < 0 {
			t.Fatalf("consume tag: %v", protowire.ParseError(n))
		}
		data = data[n:]
		if num == field && typ == protowire.Fixed64Type {
			value, m := protowire.ConsumeFixed64(data)
			if m < 0 {
				t.Fatalf("consume field %d: %v", field, protowire.ParseError(m))
			}
			return value
		}
		m := protowire.ConsumeFieldValue(num, typ, data)
		if m < 0 {
			t.Fatalf("skip field %d: %v", num, protowire.ParseError(m))
		}
		data = data[m:]
	}
	return 0
}

func TestBuildDevinUpstreamLogBody(t *testing.T) {
	interactions := []byte(`{"model":"devin/swe-2","input":[{"type":"user_input","content":[{"type":"text","text":"hello"}]}]}`)
	prompts := []DevinPrompt{
		{
			MessageID: "msg-1",
			Source:    1,
			Content:   "hello",
		},
		{
			MessageID:     "msg-2",
			Source:        2,
			Content:       "hi there",
			Thinking:      "thinking steps",
			Signature:     []byte("sealed.v1.abc123xyz"),
			SignatureType: "sealed",
		},
	}
	tools := []DevinTool{
		{
			Name:        "get_weather",
			Description: "Get weather info",
			Parameters:  []byte(`{"type":"object"}`),
		},
	}
	temp := 0.5

	// Case 1: from non-interactions source format (e.g. OpenAI chat completions)
	logBody := BuildDevinUpstreamLogBody(
		interactions,
		false,
		"swe-2-high",
		"system instruction",
		prompts,
		tools,
		&temp,
		2048,
		"sess-123",
		"casc-456",
	)

	bodyStr := string(logBody)
	if !strings.Contains(bodyStr, "=== INTERMEDIATE INTERACTIONS ===") {
		t.Errorf("expected body to contain intermediate interactions header")
	}
	if !strings.Contains(bodyStr, "=== DEVIN UPSTREAM REQUEST ===") {
		t.Errorf("expected body to contain devin upstream request header")
	}
	if !strings.Contains(bodyStr, `"model": "swe-2-high"`) {
		t.Errorf("expected body to contain model UID swe-2-high")
	}
	if !strings.Contains(bodyStr, `"thinking": "thinking steps"`) {
		t.Errorf("expected body to contain thinking steps")
	}
	if !strings.Contains(bodyStr, `"signature": "sealed.v1.abc123xyz"`) {
		t.Errorf("expected body to contain sealed signature")
	}
	if !strings.Contains(bodyStr, `"name": "get_weather"`) {
		t.Errorf("expected body to contain tool get_weather")
	}

	// Case 2: direct interactions source
	logBodyDirect := BuildDevinUpstreamLogBody(
		interactions,
		true,
		"swe-2-high",
		"system instruction",
		prompts,
		tools,
		&temp,
		2048,
		"sess-123",
		"casc-456",
	)
	bodyDirectStr := string(logBodyDirect)
	if strings.Contains(bodyDirectStr, "=== INTERMEDIATE INTERACTIONS ===") {
		t.Errorf("direct interactions source should NOT have separate intermediate header")
	}
	if !strings.Contains(bodyDirectStr, `"model": "swe-2-high"`) {
		t.Errorf("expected direct body to contain model UID")
	}
}

func TestGenerateDevinSentryTrace(t *testing.T) {
	st1 := GenerateDevinSentryTrace()
	st2 := GenerateDevinSentryTrace()
	if st1 == st2 {
		t.Fatalf("traces should be randomly generated: %s == %s", st1, st2)
	}
	parts := strings.Split(st1, "-")
	if len(parts) != 3 {
		t.Fatalf("sentry-trace should have 3 parts separated by hyphen, got %q", st1)
	}
	if len(parts[0]) != 32 {
		t.Errorf("traceID len = %d, want 32", len(parts[0]))
	}
	if len(parts[1]) != 16 {
		t.Errorf("spanID len = %d, want 16", len(parts[1]))
	}
	if parts[2] != "1" {
		t.Errorf("sampled = %q, want 1", parts[2])
	}
}

func TestBuildDevinGetChatMessageRequest_Field15TurnIndex(t *testing.T) {
	sessID0 := "sess-turn0-" + uuid.New().String()
	defer ResetDevinSessionTurnIndex(sessID0)

	// 1. Turn 0 with user prompt: turnIndex=0 (omitted), 15.4=14 emitted on user boundary
	promptsTurn0 := []DevinPrompt{
		{MessageID: "u1", Source: 1, Content: "hello"},
	}
	req0 := BuildDevinGetChatMessageRequest(DevinChatRequest{SessionToken: "tok", DeviceSeed: "seed", ChatModelUID: "swe-2-high", Prompts: promptsTurn0, Completion: DevinCompletionConfig{MaxTokens: 1000}, SessionID: sessID0, CascadeID: "casc-turn0"})
	gotSess0, f15Sub0 := extractField15Subfields(t, req0)
	if gotSess0 != sessID0 {
		t.Errorf("Field 1 sessionID = %q, want %q", gotSess0, sessID0)
	}
	if _, hasF2 := f15Sub0[2]; hasF2 {
		t.Errorf("turn 0 should omit Field 2, got %v", f15Sub0[2])
	}
	if f15Sub0[3] != 4 {
		t.Errorf("Field 3 = %d, want 4", f15Sub0[3])
	}
	if f15Sub0[4] != 14 {
		t.Errorf("Field 4 = %d, want 14 on user turn boundary", f15Sub0[4])
	}

	sessIDTool := "sess-tool-" + uuid.New().String()
	defer ResetDevinSessionTurnIndex(sessIDTool)

	// 2. Tool-result continuation (source=4): 15.4 should be omitted
	promptsTool := []DevinPrompt{
		{MessageID: "u1", Source: 1, Content: "read file"},
		{MessageID: "a1", Source: 2, Content: "calling tool"},
		{MessageID: "t1", Source: 4, Content: "file content", ToolCallID: "call_1"},
	}
	reqTool := BuildDevinGetChatMessageRequest(DevinChatRequest{SessionToken: "tok", DeviceSeed: "seed", ChatModelUID: "swe-2-high", Prompts: promptsTool, Completion: DevinCompletionConfig{MaxTokens: 1000}, SessionID: sessIDTool, CascadeID: "casc-tool"})
	_, f15SubTool := extractField15Subfields(t, reqTool)
	if _, hasF4 := f15SubTool[4]; hasF4 {
		t.Errorf("Field 4 should be omitted on tool result continuation, got %v", f15SubTool[4])
	}
}

func TestBuildDevinGetChatMessageRequest_Field15SequentialCounter(t *testing.T) {
	sessionID := "sess-sequential-test-" + uuid.New().String()
	defer ResetDevinSessionTurnIndex(sessionID)

	prompts := []DevinPrompt{
		{MessageID: "u1", Source: 1, Content: "hi"},
	}

	// Request 1: fresh session -> turnIndex 0 (omitted from wire)
	req1 := BuildDevinGetChatMessageRequest(DevinChatRequest{SessionToken: "tok", DeviceSeed: "seed", ChatModelUID: "swe-2-high", Prompts: prompts, Completion: DevinCompletionConfig{MaxTokens: 1000}, SessionID: sessionID, CascadeID: "casc-1"})
	_, sub1 := extractField15Subfields(t, req1)
	if _, hasF2 := sub1[2]; hasF2 {
		t.Errorf("Request 1 in fresh session should omit 15.2, got %v", sub1[2])
	}

	// Request 2: turnIndex 1
	req2 := BuildDevinGetChatMessageRequest(DevinChatRequest{SessionToken: "tok", DeviceSeed: "seed", ChatModelUID: "swe-2-high", Prompts: prompts, Completion: DevinCompletionConfig{MaxTokens: 1000}, SessionID: sessionID, CascadeID: "casc-1"})
	_, sub2 := extractField15Subfields(t, req2)
	if sub2[2] != 1 {
		t.Errorf("Request 2 should have 15.2 = 1, got %v", sub2[2])
	}

	// Request 3: turnIndex 2
	req3 := BuildDevinGetChatMessageRequest(DevinChatRequest{SessionToken: "tok", DeviceSeed: "seed", ChatModelUID: "swe-2-high", Prompts: prompts, Completion: DevinCompletionConfig{MaxTokens: 1000}, SessionID: sessionID, CascadeID: "casc-1"})
	_, sub3 := extractField15Subfields(t, req3)
	if sub3[2] != 2 {
		t.Errorf("Request 3 should have 15.2 = 2, got %v", sub3[2])
	}
}

func extractField15Subfields(t *testing.T, reqBytes []byte) (string, map[int]uint64) {
	t.Helper()
	b := reqBytes
	var f15Bytes []byte
	for len(b) > 0 {
		num, typ, n := protowire.ConsumeTag(b)
		if n < 0 {
			break
		}
		b = b[n:]
		if num == 15 && typ == protowire.BytesType {
			sub, m := protowire.ConsumeBytes(b)
			if m < 0 {
				t.Fatalf("failed to consume field 15 bytes")
			}
			f15Bytes = sub
			break
		}
		m := protowire.ConsumeFieldValue(num, typ, b)
		if m < 0 {
			break
		}
		b = b[m:]
	}
	if len(f15Bytes) == 0 {
		t.Fatalf("field 15 not found in request")
	}

	var sessionID string
	subfields := make(map[int]uint64)
	sb := f15Bytes
	for len(sb) > 0 {
		num, typ, n := protowire.ConsumeTag(sb)
		if n < 0 {
			break
		}
		sb = sb[n:]
		if typ == protowire.VarintType {
			val, m := protowire.ConsumeVarint(sb)
			if m < 0 {
				break
			}
			subfields[int(num)] = val
			sb = sb[m:]
		} else if typ == protowire.BytesType {
			val, m := protowire.ConsumeBytes(sb)
			if m < 0 {
				break
			}
			if num == 1 {
				sessionID = string(val)
			}
			sb = sb[m:]
		} else {
			m := protowire.ConsumeFieldValue(num, typ, sb)
			if m < 0 {
				break
			}
			sb = sb[m:]
		}
	}
	return sessionID, subfields
}

func TestParseDevinUsageField_HeadersAndField4(t *testing.T) {
	var f7Bytes []byte
	f7Bytes = protowire.AppendTag(f7Bytes, 2, protowire.VarintType)
	f7Bytes = protowire.AppendVarint(f7Bytes, 3)

	f7Bytes = protowire.AppendTag(f7Bytes, 4, protowire.VarintType)
	f7Bytes = protowire.AppendVarint(f7Bytes, 58)

	f7Bytes = protowire.AppendTag(f7Bytes, 3, protowire.VarintType)
	f7Bytes = protowire.AppendVarint(f7Bytes, 39)

	f7Bytes = protowire.AppendTag(f7Bytes, 5, protowire.VarintType)
	f7Bytes = protowire.AppendVarint(f7Bytes, 19179)

	f7Bytes = protowire.AppendTag(f7Bytes, 6, protowire.VarintType)
	f7Bytes = protowire.AppendVarint(f7Bytes, 66)

	// Submessage 1: openai-version
	var h1 []byte
	h1 = protowire.AppendTag(h1, 1, protowire.BytesType)
	h1 = protowire.AppendString(h1, "openai-version")
	h1 = protowire.AppendTag(h1, 2, protowire.BytesType)
	h1 = protowire.AppendString(h1, "2020-10-01")
	f7Bytes = protowire.AppendTag(f7Bytes, 8, protowire.BytesType)
	f7Bytes = protowire.AppendBytes(f7Bytes, h1)

	// Submessage 2: x-request-id
	var h2 []byte
	h2 = protowire.AppendTag(h2, 1, protowire.BytesType)
	h2 = protowire.AppendString(h2, "x-request-id")
	h2 = protowire.AppendTag(h2, 2, protowire.BytesType)
	h2 = protowire.AppendString(h2, "req_5bb00ad48ae048119e3420bddf36257f")
	f7Bytes = protowire.AppendTag(f7Bytes, 8, protowire.BytesType)
	f7Bytes = protowire.AppendBytes(f7Bytes, h2)

	// Submessage 3: openai-processing-ms
	var h3 []byte
	h3 = protowire.AppendTag(h3, 1, protowire.BytesType)
	h3 = protowire.AppendString(h3, "openai-processing-ms")
	h3 = protowire.AppendTag(h3, 2, protowire.BytesType)
	h3 = protowire.AppendString(h3, "419")
	f7Bytes = protowire.AppendTag(f7Bytes, 8, protowire.BytesType)
	f7Bytes = protowire.AppendBytes(f7Bytes, h3)

	f7Bytes = protowire.AppendTag(f7Bytes, 9, protowire.BytesType)
	f7Bytes = protowire.AppendString(f7Bytes, "gpt-5-6-luna-low")

	usage := parseDevinUsageField(f7Bytes)
	if usage == nil {
		t.Fatal("expected non-nil usage")
	}

	// Field 2 is input and field 4 is cache-write usage.
	if usage.InputTokens != 3 {
		t.Errorf("InputTokens = %d, want 3", usage.InputTokens)
	}
	if usage.OutputTokens != 39 {
		t.Errorf("OutputTokens = %d, want 39", usage.OutputTokens)
	}
	if usage.CacheWriteTokens != 58 {
		t.Errorf("CacheWriteTokens = %d, want 58", usage.CacheWriteTokens)
	}
	if usage.CacheReadTokens != 19179 {
		t.Errorf("CacheReadTokens = %d, want 19179", usage.CacheReadTokens)
	}
	if usage.APIProvider != 66 {
		t.Errorf("APIProvider = %d, want 66", usage.APIProvider)
	}
	if usage.RequestID != "req_5bb00ad48ae048119e3420bddf36257f" {
		t.Errorf("RequestID = %q, want clean request-id", usage.RequestID)
	}
	if usage.ModelUID != "gpt-5-6-luna-low" {
		t.Errorf("ModelUID = %q, want gpt-5-6-luna-low", usage.ModelUID)
	}
	if usage.Headers["openai-processing-ms"] != "419" {
		t.Errorf("header processing-ms = %q, want 419", usage.Headers["openai-processing-ms"])
	}
	if usage.Headers["openai-version"] != "2020-10-01" {
		t.Errorf("header openai-version = %q, want 2020-10-01", usage.Headers["openai-version"])
	}
}

func TestParseDevinUsageField_AnthropicRequestId(t *testing.T) {
	var f7Bytes []byte
	f7Bytes = protowire.AppendTag(f7Bytes, 2, protowire.VarintType)
	f7Bytes = protowire.AppendVarint(f7Bytes, 4)

	f7Bytes = protowire.AppendTag(f7Bytes, 3, protowire.VarintType)
	f7Bytes = protowire.AppendVarint(f7Bytes, 109)

	f7Bytes = protowire.AppendTag(f7Bytes, 5, protowire.VarintType)
	f7Bytes = protowire.AppendVarint(f7Bytes, 577)

	// Anthropic uses capitalized "Request-Id"
	var h []byte
	h = protowire.AppendTag(h, 1, protowire.BytesType)
	h = protowire.AppendString(h, "Request-Id")
	h = protowire.AppendTag(h, 2, protowire.BytesType)
	h = protowire.AppendString(h, "req_011Cf1JivhJrXDq9ycq7cEtH")
	f7Bytes = protowire.AppendTag(f7Bytes, 8, protowire.BytesType)
	f7Bytes = protowire.AppendBytes(f7Bytes, h)

	usage := parseDevinUsageField(f7Bytes)
	if usage == nil {
		t.Fatal("expected non-nil usage")
	}
	if usage.RequestID != "req_011Cf1JivhJrXDq9ycq7cEtH" {
		t.Errorf("RequestID = %q, want req_011Cf1JivhJrXDq9ycq7cEtH", usage.RequestID)
	}
	if usage.InputTokens != 4 {
		t.Errorf("InputTokens = %d, want 4", usage.InputTokens)
	}
	if usage.OutputTokens != 109 {
		t.Errorf("OutputTokens = %d, want 109", usage.OutputTokens)
	}
	if usage.CacheReadTokens != 577 {
		t.Errorf("CacheReadTokens = %d, want 577", usage.CacheReadTokens)
	}
}

func TestParseDevinResponseDimensionGroups(t *testing.T) {
	buildMetric := func(key string, val float32) []byte {
		// Dimension submessage (Tag 4 of Metric)
		var dim []byte
		dim = protowire.AppendTag(dim, 2, protowire.Fixed32Type)
		dim = protowire.AppendFixed32(dim, math.Float32bits(val))

		// Metric submessage (Tag 2 of Group)
		var metric []byte
		metric = protowire.AppendTag(metric, 4, protowire.BytesType)
		metric = protowire.AppendBytes(metric, dim)
		metric = protowire.AppendTag(metric, 5, protowire.BytesType)
		metric = protowire.AppendString(metric, key)
		return metric
	}

	// Build Group (Tag 28)
	var group []byte
	group = protowire.AppendTag(group, 1, protowire.BytesType)
	group = protowire.AppendString(group, "Token Usage")

	group = protowire.AppendTag(group, 2, protowire.BytesType)
	group = protowire.AppendBytes(group, buildMetric("input_tokens", 575.0))

	group = protowire.AppendTag(group, 2, protowire.BytesType)
	group = protowire.AppendBytes(group, buildMetric("output_tokens", 5.0))

	group = protowire.AppendTag(group, 2, protowire.BytesType)
	group = protowire.AppendBytes(group, buildMetric("cached_input_tokens", 128.0))

	// Envelope Tag 28
	var root []byte
	root = protowire.AppendTag(root, 28, protowire.BytesType)
	root = protowire.AppendBytes(root, group)

	promptTokens, completionTokens, cachedTokens, found := ParseDevinResponseDimensionGroups(root)
	if !found {
		t.Fatal("expected found = true")
	}
	if promptTokens != 575 {
		t.Errorf("promptTokens = %d, want 575", promptTokens)
	}
	if completionTokens != 5 {
		t.Errorf("completionTokens = %d, want 5", completionTokens)
	}
	if cachedTokens != 128 {
		t.Errorf("cachedTokens = %d, want 128", cachedTokens)
	}

	// Verify inner group directly (as extracted by ParseDevinFrame case 28)
	p2, c2, ca2, found2 := ParseDevinResponseDimensionGroups(group)
	if !found2 || p2 != 575 || c2 != 5 || ca2 != 128 {
		t.Errorf("inner group ParseDevinResponseDimensionGroups = (%d,%d,%d,%t), want (575,5,128,true)", p2, c2, ca2, found2)
	}

	// Verify multi-group where unrelated group precedes Token Usage
	var latencyGroup []byte
	latencyGroup = protowire.AppendTag(latencyGroup, 1, protowire.BytesType)
	latencyGroup = protowire.AppendString(latencyGroup, "Latency Metrics")

	p3, c3, ca3, found3 := ParseDevinResponseDimensionGroups(latencyGroup, group)
	if !found3 || p3 != 575 || c3 != 5 || ca3 != 128 {
		t.Errorf("multi-group ParseDevinResponseDimensionGroups = (%d,%d,%d,%t), want (575,5,128,true)", p3, c3, ca3, found3)
	}
}

func TestParseDevinResponseDimensionGroups_UnrelatedGroup(t *testing.T) {
	var group []byte
	group = protowire.AppendTag(group, 1, protowire.BytesType)
	group = protowire.AppendString(group, "Latency Metrics")

	var root []byte
	root = protowire.AppendTag(root, 28, protowire.BytesType)
	root = protowire.AppendBytes(root, group)

	promptTokens, completionTokens, cachedTokens, found := ParseDevinResponseDimensionGroups(root)
	if found {
		t.Errorf("expected found = false for unrelated group, got true with prompt=%d, comp=%d, cached=%d", promptTokens, completionTokens, cachedTokens)
	}
}
