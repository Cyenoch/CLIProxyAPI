package executor

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	devinauth "github.com/router-for-me/CLIProxyAPI/v7/internal/auth/devin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor/helps"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/util"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
	"google.golang.org/protobuf/encoding/protowire"
)

func TestDevinExecutorIdentifierAndFormat(t *testing.T) {
	exec := NewDevinExecutor(&config.Config{})
	if exec.Identifier() != "devin" {
		t.Fatalf("Identifier() = %q, want %q", exec.Identifier(), "devin")
	}

	format := exec.RequestToFormat(cliproxyexecutor.Request{}, cliproxyexecutor.Options{})
	if format != sdktranslator.FormatInteractions {
		t.Fatalf("RequestToFormat() = %q, want %q", format, sdktranslator.FormatInteractions)
	}
}

func TestDevinExecutorPrepareRequest(t *testing.T) {
	exec := NewDevinExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{
		Attributes: map[string]string{
			"api_key": "my-secret-key",
		},
	}
	req, err := http.NewRequest(http.MethodPost, "https://server.codeium.com/test", nil)
	if err != nil {
		t.Fatalf("NewRequest failed: %v", err)
	}

	if err := exec.PrepareRequest(req, auth); err != nil {
		t.Fatalf("PrepareRequest failed: %v", err)
	}

	if authHeader := req.Header.Get("Authorization"); authHeader != "" {
		t.Fatalf("Authorization = %q, want empty", authHeader)
	}
	if req.Header.Get("Content-Type") != "application/connect+proto" {
		t.Fatalf("Content-Type = %q, want application/connect+proto", req.Header.Get("Content-Type"))
	}
	if req.Header.Get("Connect-Protocol-Version") != "1" {
		t.Fatalf("Connect-Protocol-Version = %q, want 1", req.Header.Get("Connect-Protocol-Version"))
	}
	if req.Header.Get("Accept") != "*/*" {
		t.Fatalf("Accept = %q, want */*", req.Header.Get("Accept"))
	}
	sentryTrace := req.Header.Get("Sentry-Trace")
	if sentryTrace == "" {
		t.Fatalf("Sentry-Trace header missing")
	}
	parts := strings.Split(sentryTrace, "-")
	if len(parts) != 3 || len(parts[0]) != 32 || len(parts[1]) != 16 || parts[2] != "1" {
		t.Fatalf("invalid Sentry-Trace format: %q", sentryTrace)
	}

	// Verify User-Agent suppression on the wire
	var receivedUA []string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedUA = r.Header["User-Agent"]
	}))
	defer ts.Close()

	wireReq, err := http.NewRequest(http.MethodPost, ts.URL, nil)
	if err != nil {
		t.Fatalf("NewRequest failed: %v", err)
	}
	if err := exec.PrepareRequest(wireReq, auth); err != nil {
		t.Fatalf("PrepareRequest failed: %v", err)
	}
	resp, err := ts.Client().Do(wireReq)
	if err != nil {
		t.Fatalf("Do request failed: %v", err)
	}
	_ = resp.Body.Close()

	if len(receivedUA) != 0 {
		t.Errorf("expected User-Agent to be completely omitted on wire, got: %v", receivedUA)
	}
}

func TestDevinExecutorFetchesUserJWTBeforeChat(t *testing.T) {
	var server *httptest.Server
	var paths []string
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		switch r.URL.Path {
		case "/exa.auth_pb.AuthService/GetUserJwt":
			var response []byte
			response = protowire.AppendTag(response, 1, protowire.BytesType)
			response = protowire.AppendString(response, "user-jwt")
			response = protowire.AppendTag(response, 2, protowire.BytesType)
			response = protowire.AppendString(response, server.URL)
			_, _ = w.Write(response)
		case helps.DevinChatPath:
			if got := r.Header.Get("Authorization"); got != "" {
				t.Errorf("Authorization = %q, want empty", got)
			}
			body, errRead := io.ReadAll(r.Body)
			if errRead != nil {
				t.Errorf("read chat request: %v", errRead)
				return
			}
			_, payload, errFrame := helps.ReadConnectFrame(bytes.NewReader(body))
			if errFrame != nil {
				t.Errorf("read chat frame: %v", errFrame)
				return
			}
			metadata := protoBytesFieldForTest(t, payload, 1)
			if got := protoStringFieldForTest(t, metadata, 3); got != "devin-session-token$token" {
				t.Errorf("metadata api_key = %q", got)
			}
			if got := protoStringFieldForTest(t, metadata, 21); got != "user-jwt" {
				t.Errorf("metadata user_jwt = %q", got)
			}
			_, _ = w.Write(helps.WrapConnectEnvelopeWithFlag(helps.ConnectFlagEndStream, []byte(`{}`)))
		default:
			t.Errorf("unexpected path: %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	exec := NewDevinExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{"api_key": "token", "base_url": server.URL}}
	_, err := exec.Execute(context.Background(), auth, cliproxyexecutor.Request{Model: "devin/swe-2", Payload: []byte(`{"input":[{"type":"user_input","content":[{"type":"text","text":"hi"}]}]}`)}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatInteractions, ResponseFormat: sdktranslator.FormatInteractions})
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}
	wantPaths := []string{"/exa.auth_pb.AuthService/GetUserJwt", helps.DevinChatPath}
	if fmt.Sprint(paths) != fmt.Sprint(wantPaths) {
		t.Fatalf("paths = %v, want %v", paths, wantPaths)
	}
}

func TestDevinExecutorAssignsRouterModel(t *testing.T) {
	var server *httptest.Server
	var paths []string
	var assignCascadeID, chatCascadeID string
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		switch r.URL.Path {
		case "/exa.auth_pb.AuthService/GetUserJwt":
			var response []byte
			response = protowire.AppendTag(response, 1, protowire.BytesType)
			response = protowire.AppendString(response, "user-jwt")
			_, _ = w.Write(response)
		case helps.DevinAssignModelPath:
			body, errRead := io.ReadAll(r.Body)
			if errRead != nil {
				t.Errorf("read assignment request: %v", errRead)
				return
			}
			if got := protoStringFieldForTest(t, body, 2); got != "adaptive" {
				t.Errorf("model_router_uid = %q, want adaptive", got)
			}
			assignCascadeID = protoStringFieldForTest(t, body, 3)
			routerPrompt := protoBytesFieldForTest(t, body, 5)
			if got := protoStringFieldForTest(t, routerPrompt, 3); got != "hi" {
				t.Errorf("router prompt content = %q, want hi", got)
			}
			var assignment []byte
			assignment = protowire.AppendTag(assignment, 1, protowire.BytesType)
			assignment = protowire.AppendString(assignment, "assignment-jwt")
			assignment = protowire.AppendTag(assignment, 2, protowire.BytesType)
			assignment = protowire.AppendString(assignment, "swe-2-high")
			var response []byte
			response = protowire.AppendTag(response, 1, protowire.BytesType)
			response = protowire.AppendBytes(response, assignment)
			_, _ = w.Write(response)
		case helps.DevinChatPath:
			body, errRead := io.ReadAll(r.Body)
			if errRead != nil {
				t.Errorf("read chat request: %v", errRead)
				return
			}
			_, payload, errFrame := helps.ReadConnectFrame(bytes.NewReader(body))
			if errFrame != nil {
				t.Errorf("read chat frame: %v", errFrame)
				return
			}
			if got := protoStringFieldForTest(t, payload, 21); got != "swe-2-high" {
				t.Errorf("chat_model_uid = %q, want swe-2-high", got)
			}
			if got := protoStringFieldForTest(t, payload, 26); got != "assignment-jwt" {
				t.Errorf("model_assignment_jwt = %q, want assignment-jwt", got)
			}
			chatCascadeID = protoStringFieldForTest(t, payload, 16)
			_, _ = w.Write(helps.WrapConnectEnvelopeWithFlag(helps.ConnectFlagEndStream, []byte(`{}`)))
		default:
			t.Errorf("unexpected path: %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	exec := NewDevinExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{"api_key": "token", "base_url": server.URL}}
	_, err := exec.Execute(context.Background(), auth, cliproxyexecutor.Request{Model: "adaptive", Payload: []byte(`{"input":[{"type":"user_input","content":[{"type":"text","text":"hi"}]}]}`)}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatInteractions, ResponseFormat: sdktranslator.FormatInteractions})
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}
	wantPaths := []string{"/exa.auth_pb.AuthService/GetUserJwt", helps.DevinAssignModelPath, helps.DevinChatPath}
	if fmt.Sprint(paths) != fmt.Sprint(wantPaths) {
		t.Fatalf("paths = %v, want %v", paths, wantPaths)
	}
	if assignCascadeID == "" || assignCascadeID != chatCascadeID {
		t.Fatalf("cascade id mismatch: assign=%q chat=%q", assignCascadeID, chatCascadeID)
	}
}

func TestDevinExecutorRouterAssignmentIncomplete(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/exa.auth_pb.AuthService/GetUserJwt":
			var response []byte
			response = protowire.AppendTag(response, 1, protowire.BytesType)
			response = protowire.AppendString(response, "user-jwt")
			_, _ = w.Write(response)
		case helps.DevinAssignModelPath:
			// Assignment without model_uid must fail the turn.
			var assignment []byte
			assignment = protowire.AppendTag(assignment, 1, protowire.BytesType)
			assignment = protowire.AppendString(assignment, "assignment-jwt")
			var response []byte
			response = protowire.AppendTag(response, 1, protowire.BytesType)
			response = protowire.AppendBytes(response, assignment)
			_, _ = w.Write(response)
		case helps.DevinChatPath:
			t.Errorf("chat must not be sent with a failed router assignment")
			_, _ = w.Write(helps.WrapConnectEnvelopeWithFlag(helps.ConnectFlagEndStream, []byte(`{}`)))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	exec := NewDevinExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{"api_key": "token", "base_url": server.URL}}
	_, err := exec.Execute(context.Background(), auth, cliproxyexecutor.Request{Model: "adaptive", Payload: []byte(`{"input":[{"type":"user_input","content":[{"type":"text","text":"hi"}]}]}`)}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatInteractions, ResponseFormat: sdktranslator.FormatInteractions})
	if err == nil {
		t.Fatal("Execute succeeded, want assignment failure")
	}
}

func TestDevinAuthCredentials(t *testing.T) {
	auth := &cliproxyauth.Auth{
		Attributes: map[string]string{
			"session_token": "token-xyz",
			"base_url":      "https://custom.endpoint.com",
			"device_seed":   "seed-456",
		},
	}
	apiKey, baseURL, seed := devinAuthCredentials(auth)
	if apiKey != "token-xyz" {
		t.Errorf("apiKey = %q, want token-xyz", apiKey)
	}
	if baseURL != "https://custom.endpoint.com" {
		t.Errorf("baseURL = %q, want https://custom.endpoint.com", baseURL)
	}
	if seed != "seed-456" {
		t.Errorf("seed = %q, want seed-456", seed)
	}
}

func TestDevinExecutor_GetSensitiveWords(t *testing.T) {
	eEmpty := &DevinExecutor{}
	if words := eEmpty.getSensitiveWords(); len(words) != 0 {
		t.Errorf("words = %v, want empty", words)
	}

	eWithWords := &DevinExecutor{
		cfg: &config.Config{
			Devin: config.DevinConfig{
				SensitiveWords: []string{"sample-word-1", "sample-word-2"},
			},
		},
	}
	words := eWithWords.getSensitiveWords()
	if len(words) != 2 || words[0] != "sample-word-1" || words[1] != "sample-word-2" {
		t.Errorf("words = %v, want [sample-word-1 sample-word-2]", words)
	}
}

func TestParseInteractionsPayload(t *testing.T) {
	interactionsPayload := []byte(`{
		"system_instruction": "You are a helpful coding assistant.",
		"generation_config": {
			"temperature": 0.8,
			"top_p": 0.7,
			"max_output_tokens": 16000,
			"stop_sequences": ["END"],
			"tool_choice": {"type":"function","name":"read_file"},
			"thinking_level": "high"
		},
		"parallel_tool_calls": false,
		"previous_interaction_id": "session-uuid-1",
		"input": [
			{"type":"user_input","content":[{"type":"text","text":"hello"}]},
			{"type":"thought","content":[{"type":"text","text":"planning..."}],"signature":"c2VhbGVkLnYxLnRlc3Q="},
			{"type":"model_output","content":[{"type":"text","text":"I can help with that."}]},
			{"type":"function_call","name":"read_file","id":"call_1","arguments":{"path":"main.go"}},
			{"type":"function_result","id":"call_1","is_error":true,"result":"package main\n"}
		],
		"tools": [
			{"name":"read_file","description":"Read file content","strict":true,"parameters":{"type":"object"}}
		]
	}`)

	parsed := parseInteractionsPayload(interactionsPayload, nil)
	prompts := parsed.prompts

	if parsed.systemPrompt != "You are a helpful coding assistant." {
		t.Errorf("systemPrompt = %q, want expected", parsed.systemPrompt)
	}
	if parsed.completion.Temperature == nil || *parsed.completion.Temperature != 0.8 {
		t.Errorf("temperature = %v, want 0.8", parsed.completion.Temperature)
	}
	if parsed.completion.MaxTokens != 16000 {
		t.Errorf("maxTokens = %d, want 16000", parsed.completion.MaxTokens)
	}
	if parsed.completion.TopP == nil || *parsed.completion.TopP != 0.7 {
		t.Errorf("topP = %v, want 0.7", parsed.completion.TopP)
	}
	if len(parsed.completion.StopPatterns) != 1 || parsed.completion.StopPatterns[0] != "END" {
		t.Errorf("stopPatterns = %v, want [END]", parsed.completion.StopPatterns)
	}
	if parsed.toolChoice.ToolName != "read_file" {
		t.Errorf("toolChoice = %+v, want read_file", parsed.toolChoice)
	}
	if !parsed.disableParallelToolCalls {
		t.Error("parallel tool calls should be disabled")
	}
	if parsed.thinkingLevel != "high" {
		t.Errorf("thinkingLevel = %q, want high", parsed.thinkingLevel)
	}
	if parsed.sessionID != "session-uuid-1" || parsed.cascadeID != "session-uuid-1" {
		t.Errorf("session/cascade ID = %q / %q, want session-uuid-1", parsed.sessionID, parsed.cascadeID)
	}

	if len(parsed.tools) != 1 || parsed.tools[0].Name != "read_file" || !parsed.tools[0].Strict {
		t.Fatalf("tools count/name/strict mismatch: %+v", parsed.tools)
	}

	if len(prompts) != 3 {
		t.Fatalf("expected 3 prompt items (user, assistant-with-thought-and-call, tool-result), got %d: %+v", len(prompts), prompts)
	}

	// 1. User turn
	if prompts[0].Source != 1 || prompts[0].Content != "hello" {
		t.Errorf("prompt[0] user turn mismatch: %+v", prompts[0])
	}

	// 2. Assistant turn (attached thought + content + function call)
	if prompts[1].Source != 2 {
		t.Errorf("prompt[1] source = %d, want 2", prompts[1].Source)
	}
	if prompts[1].Thinking != "planning..." {
		t.Errorf("prompt[1] thinking = %q, want planning...", prompts[1].Thinking)
	}
	if string(prompts[1].Signature) != "sealed.v1.test" {
		t.Errorf("prompt[1] signature = %q, want sealed.v1.test", string(prompts[1].Signature))
	}
	if len(prompts[1].ToolCalls) != 1 || prompts[1].ToolCalls[0].Name != "read_file" {
		t.Errorf("prompt[1] tool calls mismatch: %+v", prompts[1].ToolCalls)
	}

	// 3. Tool result turn
	if prompts[2].Source != 4 || prompts[2].ToolCallID != "call_1" || !prompts[2].ToolResultErr || prompts[2].Content != "package main\n" {
		t.Errorf("prompt[2] tool result mismatch: %+v", prompts[2])
	}
}

func TestParseInteractionsPayload_MultipleThoughtsAndZeroTemperature(t *testing.T) {
	interactionsPayload := []byte(`{
		"generation_config": {
			"temperature": 0.0
		},
		"input": [
			{"type": "user_input", "content": [{"type": "text", "text": "hello"}]},
			{"type": "thought", "text": "Thought part 1"},
			{"type": "thought", "text": "Thought part 2"},
			{"type": "model_output", "text": "Hello there!"}
		]
	}`)

	parsed := parseInteractionsPayload(interactionsPayload, nil)
	prompts := parsed.prompts

	if parsed.completion.Temperature == nil || *parsed.completion.Temperature != 0.0 {
		t.Fatalf("temperature = %v, want 0.0", parsed.completion.Temperature)
	}

	if len(prompts) != 2 {
		t.Fatalf("prompts len = %d, want 2", len(prompts))
	}

	asst := prompts[1]
	if asst.Source != 2 {
		t.Fatalf("assistant source = %d, want 2", asst.Source)
	}
	wantThinking := "Thought part 1\n\nThought part 2"
	if asst.Thinking != wantThinking {
		t.Fatalf("assistant thinking = %q, want %q", asst.Thinking, wantThinking)
	}
	if asst.Content != "Hello there!" {
		t.Fatalf("assistant content = %q, want Hello there!", asst.Content)
	}
}

func TestSupplementSignaturesFromOriginal(t *testing.T) {
	// Devin-native sealed signatures are supplemented; foreign-provider
	// signatures (anthropic/openai/gemini) are invalid upstream and dropped.
	originalRequest := []byte(`{
		"messages": [
			{"role":"user","content":"hello"},
			{"role":"assistant","content":[
				{"type":"thinking","thinking":"let me think","signature":"c2VhbGVkLnYxLnRlc3Q="},
				{"type":"text","text":"here is the answer"}
			]},
			{"role":"user","content":"again"},
			{"role":"assistant","content":[
				{"type":"thinking","thinking":"foreign","signature":"Q0FRU3Rlc3Q="},
				{"type":"text","text":"other answer"}
			]}
		]
	}`)

	prompts := []helps.DevinPrompt{
		{Source: 1, Content: "hello"},
		{Source: 2, Content: "here is the answer"}, // signature missing in interactions
		{Source: 1, Content: "again"},
		{Source: 2, Content: "other answer"},
	}

	supplementSignaturesFromOriginal(originalRequest, prompts)

	if string(prompts[1].Signature) != "sealed.v1.test" {
		t.Errorf("signature = %q, want sealed.v1.test", string(prompts[1].Signature))
	}
	if prompts[1].SignatureType != "sealed" {
		t.Errorf("signatureType = %q, want sealed", prompts[1].SignatureType)
	}
	if len(prompts[3].Signature) != 0 || prompts[3].SignatureType != "" {
		t.Errorf("foreign signature must not be forwarded, got %q (%q)", prompts[3].Signature, prompts[3].SignatureType)
	}
}

func TestDetectSignatureType_GlobalDetectorIntegration(t *testing.T) {
	tests := []struct {
		name     string
		sig      string
		wantType string
	}{
		{
			name:     "Devin native sealed signature",
			sig:      "sealed.v1.abcde12345",
			wantType: "sealed",
		},
		{
			name:     "Anthropic CAQS signature",
			sig:      "CAQStest12345",
			wantType: "anthropic",
		},
		{
			name:     "Anthropic with claude# prefix",
			sig:      "claude#CAQStest12345",
			wantType: "anthropic",
		},
		{
			name:     "OpenAI gAAAA Fernet signature",
			sig:      "gAAAAABk1234567890",
			wantType: "openai",
		},
		{
			name:     "Gemini AY signature",
			sig:      "AY12345",
			wantType: "gemini",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := detectSignatureType(tt.sig)
			if got != tt.wantType {
				t.Errorf("detectSignatureType(%q) = %q, want %q", tt.sig, got, tt.wantType)
			}
			_, pType := parseSignatureBytes(tt.sig)
			if pType != tt.wantType {
				t.Errorf("parseSignatureBytes(%q) type = %q, want %q", tt.sig, pType, tt.wantType)
			}
		})
	}
}

func TestDevinStatusError_RetryAfter(t *testing.T) {
	// 1. HTTP 429 with integer Retry-After
	hdr429 := http.Header{}
	hdr429.Set("Retry-After", "30")
	err1 := newDevinStatusError(http.StatusTooManyRequests, hdr429, []byte("rate limited"))
	if err1.code != 429 {
		t.Fatalf("expected code 429, got %d", err1.code)
	}
	if err1.retryAfter == nil || *err1.retryAfter != 30*time.Second {
		t.Fatalf("expected retryAfter 30s, got %v", err1.retryAfter)
	}

	// 2. HTTP 429 with HTTP Date
	hdrDate := http.Header{}
	futureTime := time.Now().Add(60 * time.Second).UTC().Format(http.TimeFormat)
	hdrDate.Set("Retry-After", futureTime)
	err2 := newDevinStatusError(http.StatusTooManyRequests, hdrDate, []byte("rate limited"))
	if err2.retryAfter == nil || *err2.retryAfter <= 0 || *err2.retryAfter > 65*time.Second {
		t.Fatalf("expected retryAfter ~60s, got %v", err2.retryAfter)
	}

	// 3. HTTP 500 with Retry-After (should not set retryAfter)
	err3 := newDevinStatusError(http.StatusInternalServerError, hdr429, []byte("server error"))
	if err3.retryAfter != nil {
		t.Fatalf("expected nil retryAfter for 500, got %v", err3.retryAfter)
	}
}

func TestResolveDevinSessionAndCascadeIDs(t *testing.T) {
	// 1. Direct UUID preservation
	rawUUID := "8176cf8a-feff-44c1-8e3e-b10f6d737ae1"
	sid, cid := resolveDevinSessionAndCascadeIDs(context.Background(), rawUUID, rawUUID, cliproxyexecutor.Options{})
	if sid != rawUUID || cid != rawUUID {
		t.Fatalf("sid/cid = %q/%q, want %q", sid, cid, rawUUID)
	}

	// 2. Non-UUID mapping to deterministic UUID
	sid1, cid1 := resolveDevinSessionAndCascadeIDs(context.Background(), "lcp:12345678", "", cliproxyexecutor.Options{})
	sid2, cid2 := resolveDevinSessionAndCascadeIDs(context.Background(), "lcp:12345678", "", cliproxyexecutor.Options{})
	if sid1 != sid2 || cid1 != cid2 {
		t.Fatalf("deterministic mapping failed: %q != %q", sid1, sid2)
	}
	if _, err := uuid.Parse(sid1); err != nil {
		t.Fatalf("mapped sid is not a valid UUID: %q", sid1)
	}

	// 3. Fallback to ctx session
	ctx := util.WithSessionID(context.Background(), "ctx-session-abc")
	sidCtx, cidCtx := resolveDevinSessionAndCascadeIDs(ctx, "", "", cliproxyexecutor.Options{})
	if _, err := uuid.Parse(sidCtx); err != nil {
		t.Fatalf("sidCtx is not a valid UUID: %q", sidCtx)
	}
	if sidCtx != cidCtx {
		t.Fatalf("sidCtx %q != cidCtx %q", sidCtx, cidCtx)
	}

	// 4. Fallback to fresh UUID when nothing supplied
	sidEmpty, cidEmpty := resolveDevinSessionAndCascadeIDs(context.Background(), "", "", cliproxyexecutor.Options{})
	if _, err := uuid.Parse(sidEmpty); err != nil {
		t.Fatalf("sidEmpty is not a valid UUID: %q", sidEmpty)
	}
	if sidEmpty != cidEmpty {
		t.Fatalf("sidEmpty %q != cidEmpty %q", sidEmpty, cidEmpty)
	}
}

func TestConsumeDevinFramesToInteractions(t *testing.T) {
	// Synthesize a Connect stream with 2 data frames and 1 EOS trailer
	var streamBuf bytes.Buffer

	// Frame 1: thinking + content
	var f1 []byte
	f1 = appendDevinFieldBytes(f1, 1, []byte("bot-uuid-1"))
	f1 = appendDevinFieldBytes(f1, 9, []byte("reasoning step"))
	f1 = appendDevinFieldBytes(f1, 3, []byte("hello response"))
	f1 = appendDevinFieldBytes(f1, 10, []byte("sealed.v1.sig"))
	streamBuf.Write(helps.WrapConnectEnvelope(f1))

	// Frame 2: tool call + usage
	var f2 []byte
	var tcBytes []byte
	tcBytes = appendDevinFieldBytes(tcBytes, 1, []byte("toolu_1"))
	tcBytes = appendDevinFieldBytes(tcBytes, 2, []byte("bash"))
	tcBytes = appendDevinFieldBytes(tcBytes, 3, []byte(`{"command":"ls"}`))
	f2 = appendDevinFieldBytes(f2, 6, tcBytes)

	var usageBytes []byte
	usageBytes = appendVarintField(usageBytes, 2, 100) // prompt
	usageBytes = appendVarintField(usageBytes, 3, 50)  // completion
	usageBytes = appendVarintField(usageBytes, 5, 20)  // cached
	f2 = appendDevinFieldBytes(f2, 7, usageBytes)
	streamBuf.Write(helps.WrapConnectEnvelope(f2))

	// Frame 3: EOS Trailer flag 0x02
	trailerJSON := []byte(`{}`)
	streamBuf.Write(helps.WrapConnectEnvelopeWithFlag(helps.ConnectFlagEndStream, trailerJSON))

	interactionsJSON, respLog, err := consumeDevinFramesToInteractions(&streamBuf, "swe-2", "swe-2-high")
	if err != nil {
		t.Fatalf("consumeDevinFramesToInteractions failed: %v", err)
	}
	if respLog == nil {
		t.Fatal("expected non-nil respLog")
	}
	if respLog.FramesCount != 3 {
		t.Errorf("FramesCount = %d, want 3", respLog.FramesCount)
	}

	root := gjson.ParseBytes(interactionsJSON)
	if root.Get("status").String() != "completed" {
		t.Errorf("status = %q, want completed", root.Get("status").String())
	}
	if root.Get("usage.total_input_tokens").Int() != 120 {
		t.Errorf("input tokens = %d, want 120", root.Get("usage.total_input_tokens").Int())
	}
	if root.Get("usage.total_output_tokens").Int() != 50 {
		t.Errorf("output tokens = %d, want 50", root.Get("usage.total_output_tokens").Int())
	}
	if root.Get("usage.total_cached_tokens").Int() != 20 {
		t.Errorf("cached tokens = %d, want 20", root.Get("usage.total_cached_tokens").Int())
	}
	if root.Get("usage.total_tokens").Int() != 170 {
		t.Errorf("total tokens = %d, want 170", root.Get("usage.total_tokens").Int())
	}

	steps := root.Get("steps").Array()
	if len(steps) != 3 {
		t.Fatalf("steps count = %d, want 3 (thought, model_output, function_call). Payload: %s", len(steps), string(interactionsJSON))
	}

	// Thought step has signature
	if steps[0].Get("type").String() != "thought" {
		t.Errorf("step[0] type = %q, want thought", steps[0].Get("type").String())
	}
	expectedSig := "sealed.v1.sig"
	if steps[0].Get("signature").String() != expectedSig {
		t.Errorf("step[0] signature = %q, want %q", steps[0].Get("signature").String(), expectedSig)
	}

	// Model output step
	if steps[1].Get("type").String() != "model_output" {
		t.Errorf("step[1] type = %q, want model_output", steps[1].Get("type").String())
	}
	if steps[1].Get("content.0.text").String() != "hello response" {
		t.Errorf("step[1] text = %q, want 'hello response'", steps[1].Get("content.0.text").String())
	}

	// Function call step
	if steps[2].Get("type").String() != "function_call" {
		t.Errorf("step[2] type = %q, want function_call", steps[2].Get("type").String())
	}
	if steps[2].Get("name").String() != "bash" {
		t.Errorf("step[2] tool name = %q, want bash", steps[2].Get("name").String())
	}
}

func protoBytesFieldForTest(t *testing.T, data []byte, field protowire.Number) []byte {
	t.Helper()
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
			return value
		}
		m := protowire.ConsumeFieldValue(num, typ, data)
		if m < 0 {
			t.Fatalf("skip field %d: %v", num, protowire.ParseError(m))
		}
		data = data[m:]
	}
	t.Fatalf("field %d not found", field)
	return nil
}

func protoStringFieldForTest(t *testing.T, data []byte, field protowire.Number) string {
	t.Helper()
	return string(protoBytesFieldForTest(t, data, field))
}

func appendDevinFieldBytes(dst []byte, fieldNum int, val []byte) []byte {
	tag := uint64(fieldNum<<3 | 2)
	dst = appendVarintRaw(dst, tag)
	dst = appendVarintRaw(dst, uint64(len(val)))
	dst = append(dst, val...)
	return dst
}

func appendVarintField(dst []byte, fieldNum int, v uint64) []byte {
	tag := uint64(fieldNum<<3 | 0)
	dst = appendVarintRaw(dst, tag)
	dst = appendVarintRaw(dst, v)
	return dst
}

func appendVarintRaw(dst []byte, v uint64) []byte {
	for v >= 0x80 {
		dst = append(dst, byte(v)|0x80)
		v >>= 7
	}
	dst = append(dst, byte(v))
	return dst
}

func TestParseInteractionsPayload_WithImages(t *testing.T) {
	interactionsPayload := []byte(`{
		"input": [
			{
				"type": "user_input",
				"content": [
					{"type": "text", "text": "transcribe this"},
					{"type": "image", "mime_type": "image/png", "data": "iVBORw0KGgoAAAANSUhEUgAA"}
				]
			}
		]
	}`)

	prompts := parseInteractionsPayload(interactionsPayload, nil).prompts

	if len(prompts) != 1 {
		t.Fatalf("expected 1 prompt, got %d", len(prompts))
	}
	p := prompts[0]
	if len(p.Images) != 1 {
		t.Fatalf("expected 1 image in prompt, got %d", len(p.Images))
	}
	if p.Images[0].Base64Data != "iVBORw0KGgoAAAANSUhEUgAA" {
		t.Errorf("image base64 = %q", p.Images[0].Base64Data)
	}
	if p.Images[0].MimeType != "image/png" {
		t.Errorf("image mime = %q, want image/png", p.Images[0].MimeType)
	}
	if !strings.HasPrefix(p.Content, "[Image 1: pasted_image_1.png]\n\ntranscribe this") {
		t.Errorf("prompt content = %q, want expected prefix", p.Content)
	}
}

func TestSupplementImagesFromOriginal(t *testing.T) {
	origRequest := []byte(`{
		"messages": [
			{
				"role": "user",
				"content": [
					{"type": "text", "text": "look at this"},
					{"type": "image_url", "image_url": {"url": "data:image/jpeg;base64,/9j/4AAQSkZJRgABAQEASABIAAD"}}
				]
			}
		]
	}`)

	prompts := []helps.DevinPrompt{
		{
			Source:  1,
			Content: "look at this",
		},
	}

	supplementImagesFromOriginal(origRequest, prompts)

	if len(prompts[0].Images) != 1 {
		t.Fatalf("expected 1 image supplemented, got %d", len(prompts[0].Images))
	}
	if prompts[0].Images[0].MimeType != "image/jpeg" {
		t.Errorf("mime_type = %q, want image/jpeg", prompts[0].Images[0].MimeType)
	}
	if prompts[0].Images[0].Base64Data != "/9j/4AAQSkZJRgABAQEASABIAAD" {
		t.Errorf("base64 = %q", prompts[0].Images[0].Base64Data)
	}
	if !strings.Contains(prompts[0].Content, "[Image 1: pasted_image_1.jpg]") {
		t.Errorf("content missing image header: %q", prompts[0].Content)
	}
}

func TestDevinExecutor_Refresh(t *testing.T) {
	// Build mock protobuf response
	var planInfo []byte
	planInfo = protowire.AppendTag(planInfo, 2, protowire.BytesType)
	planInfo = protowire.AppendString(planInfo, "Pro")

	var orgInfo []byte
	orgInfo = protowire.AppendTag(orgInfo, 4, protowire.BytesType)
	orgInfo = protowire.AppendString(orgInfo, "org-test-devin")
	orgInfo = protowire.AppendTag(orgInfo, 8, protowire.BytesType)
	orgInfo = protowire.AppendString(orgInfo, "XCodeCLI")
	planInfo = protowire.AppendTag(planInfo, 33, protowire.BytesType)
	planInfo = protowire.AppendBytes(planInfo, orgInfo)

	var planStatus []byte
	planStatus = protowire.AppendTag(planStatus, 1, protowire.BytesType)
	planStatus = protowire.AppendBytes(planStatus, planInfo)
	planStatus = protowire.AppendTag(planStatus, 14, protowire.VarintType)
	planStatus = protowire.AppendVarint(planStatus, 95)
	planStatus = protowire.AppendTag(planStatus, 15, protowire.VarintType)
	planStatus = protowire.AppendVarint(planStatus, 45)
	planStatus = protowire.AppendTag(planStatus, 17, protowire.VarintType)
	planStatus = protowire.AppendVarint(planStatus, 1789200000)
	planStatus = protowire.AppendTag(planStatus, 18, protowire.VarintType)
	planStatus = protowire.AppendVarint(planStatus, 1789286400)

	var userStatus []byte
	userStatus = protowire.AppendTag(userStatus, 3, protowire.BytesType)
	userStatus = protowire.AppendString(userStatus, "refreshuser")
	userStatus = protowire.AppendTag(userStatus, 5, protowire.BytesType)
	userStatus = protowire.AppendString(userStatus, "team-xyz")
	userStatus = protowire.AppendTag(userStatus, 7, protowire.BytesType)
	userStatus = protowire.AppendString(userStatus, "refreshuser@example.com")
	userStatus = protowire.AppendTag(userStatus, 13, protowire.BytesType)
	userStatus = protowire.AppendBytes(userStatus, planStatus)
	userStatus = protowire.AppendTag(userStatus, 36, protowire.BytesType)
	userStatus = protowire.AppendString(userStatus, "user-id-999")

	var mockResp []byte
	mockResp = protowire.AppendTag(mockResp, 1, protowire.BytesType)
	mockResp = protowire.AppendBytes(mockResp, userStatus)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != devinauth.DevinGetUserStatusPath {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/proto")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(mockResp)
	}))
	defer server.Close()

	cfg := &config.Config{}
	exec := NewDevinExecutor(cfg)

	auth := &cliproxyauth.Auth{
		ID:       "devin-refresh.json",
		Provider: "devin",
		Attributes: map[string]string{
			"api_key":  "devin-session-token$test",
			"base_url": server.URL,
		},
		Metadata: map[string]any{
			"api_key":  "devin-session-token$test",
			"base_url": server.URL,
		},
	}

	updated, err := exec.Refresh(context.Background(), auth)
	if err != nil {
		t.Fatalf("exec.Refresh failed: %v", err)
	}

	if updated.Metadata["plan"] != "Pro" {
		t.Errorf("expected plan Pro, got %v", updated.Metadata["plan"])
	}
	if updated.Metadata["email"] != "refreshuser@example.com" {
		t.Errorf("expected email refreshuser@example.com, got %v", updated.Metadata["email"])
	}
	if updated.Metadata["user_name"] != "refreshuser" {
		t.Errorf("expected user_name refreshuser, got %v", updated.Metadata["user_name"])
	}
	if updated.Metadata["daily_quota_remaining_percent"] != nil {
		t.Errorf("expected daily quota to not be in metadata, got %v", updated.Metadata["daily_quota_remaining_percent"])
	}
	if updated.Metadata["weekly_quota_remaining_percent"] != nil {
		t.Errorf("expected weekly quota to not be in metadata, got %v", updated.Metadata["weekly_quota_remaining_percent"])
	}
	if updated.Quota.Signals["daily_quota_remaining_percent"] != "95%" {
		t.Errorf("expected quota signal 95%%, got %q", updated.Quota.Signals["daily_quota_remaining_percent"])
	}
	if updated.Quota.Signals["weekly_quota_remaining_percent"] != "45%" {
		t.Errorf("expected quota signal 45%%, got %q", updated.Quota.Signals["weekly_quota_remaining_percent"])
	}
	if updated.Quota.ObservedAt.IsZero() {
		t.Error("expected non-zero Quota.ObservedAt")
	}
}

func TestDevinExecutor_MaxCompletionTokensClamping(t *testing.T) {
	reg := registry.GetGlobalRegistry()
	clientID := "test-devin-clamp-client"
	modelID := "devin/swe-2-clamp-test"
	reg.RegisterClient(clientID, "devin", []*registry.ModelInfo{
		{
			ID:                  modelID,
			MaxCompletionTokens: 64000,
			ContextLength:       262000,
		},
	})
	defer reg.UnregisterClient(clientID)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/exa.auth_pb.AuthService/GetUserJwt" {
			var response []byte
			response = protowire.AppendTag(response, 1, protowire.BytesType)
			response = protowire.AppendString(response, "user-jwt")
			_, _ = w.Write(response)
			return
		}
		http.NotFound(w, r)
	}))
	defer server.Close()

	cfg := &config.Config{}
	exec := NewDevinExecutor(cfg)
	auth := &cliproxyauth.Auth{
		Attributes: map[string]string{
			"api_key":  "test-key",
			"base_url": server.URL,
		},
	}

	// 1. When requested max_output_tokens exceeds MaxCompletionTokens (e.g. 100000 > 64000)
	payloadOversized := []byte(`{
		"generation_config": {
			"max_output_tokens": 100000
		},
		"input": [{"type":"user_input","content":[{"type":"text","text":"hello"}]}]
	}`)
	reqOversized := cliproxyexecutor.Request{
		Model:   modelID,
		Payload: payloadOversized,
	}
	opts := cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FormatInteractions,
	}

	httpReq, _, _, err := exec.prepareDevinHTTPRequest(context.Background(), auth, reqOversized, opts)
	if err != nil {
		t.Fatalf("prepareDevinHTTPRequest failed: %v", err)
	}

	// Read body, unwrap 5-byte Connect envelope, and inspect Field 8 Subfield 2 (maxTokens)
	bodyBytes, err := io.ReadAll(httpReq.Body)
	if err != nil {
		t.Fatalf("read body failed: %v", err)
	}
	flag, payloadBytes, err := helps.ReadConnectFrame(bytes.NewReader(bodyBytes))
	if err != nil || flag != 0 {
		t.Fatalf("unwrap failed: %v", err)
	}

	maxTokensFound := 0
	b := payloadBytes
	for len(b) > 0 {
		num, typ, n := protowire.ConsumeTag(b)
		if n < 0 {
			break
		}
		b = b[n:]
		if num == 8 && typ == protowire.BytesType {
			subBytes, m := protowire.ConsumeBytes(b)
			if m >= 0 {
				sb := subBytes
				for len(sb) > 0 {
					snum, styp, sn := protowire.ConsumeTag(sb)
					if sn < 0 {
						break
					}
					sb = sb[sn:]
					if snum == 2 && styp == protowire.VarintType {
						val, vn := protowire.ConsumeVarint(sb)
						if vn >= 0 {
							maxTokensFound = int(val)
							break
						}
					}
					skip := protowire.ConsumeFieldValue(snum, styp, sb)
					if skip < 0 {
						break
					}
					sb = sb[skip:]
				}
			}
			break
		}
		skip := protowire.ConsumeFieldValue(num, typ, b)
		if skip < 0 {
			break
		}
		b = b[skip:]
	}

	if maxTokensFound != 64000 {
		t.Errorf("maxTokensFound = %d, want clamped 64000", maxTokensFound)
	}
}

func TestConsumeDevinFramesToInteractions_MultiToolCallsNoPanic(t *testing.T) {
	// Build a schema-valid stream with multiple tool calls and cumulative arguments.
	var buf bytes.Buffer
	// Frame 1: tool call 0 start + partial args
	var tc0 []byte
	tc0 = protowire.AppendTag(tc0, 1, protowire.BytesType)
	tc0 = protowire.AppendString(tc0, "call_0")
	tc0 = protowire.AppendTag(tc0, 2, protowire.BytesType)
	tc0 = protowire.AppendString(tc0, "tool_0")
	tc0 = protowire.AppendTag(tc0, 3, protowire.BytesType)
	tc0 = protowire.AppendString(tc0, `{"a":`)

	var f1 []byte
	f1 = protowire.AppendTag(f1, 6, protowire.BytesType)
	f1 = protowire.AppendBytes(f1, tc0)
	buf.Write(helps.WrapConnectEnvelope(f1))

	// Frame 2: tool call 1 complete args
	var tc1 []byte
	tc1 = protowire.AppendTag(tc1, 1, protowire.BytesType)
	tc1 = protowire.AppendString(tc1, "call_1")
	tc1 = protowire.AppendTag(tc1, 2, protowire.BytesType)
	tc1 = protowire.AppendString(tc1, "tool_1")
	tc1 = protowire.AppendTag(tc1, 3, protowire.BytesType)
	tc1 = protowire.AppendString(tc1, `{"b":2}`)

	var f2 []byte
	f2 = protowire.AppendTag(f2, 6, protowire.BytesType)
	f2 = protowire.AppendBytes(f2, tc1)
	buf.Write(helps.WrapConnectEnvelope(f2))

	// Frame 3: tool call 0 cumulative arguments
	var tc0Cont []byte
	tc0Cont = protowire.AppendTag(tc0Cont, 1, protowire.BytesType)
	tc0Cont = protowire.AppendString(tc0Cont, "call_0")
	tc0Cont = protowire.AppendTag(tc0Cont, 2, protowire.BytesType)
	tc0Cont = protowire.AppendString(tc0Cont, "tool_0")
	tc0Cont = protowire.AppendTag(tc0Cont, 3, protowire.BytesType)
	tc0Cont = protowire.AppendString(tc0Cont, `{"a":1}`)

	var f3 []byte
	f3 = protowire.AppendTag(f3, 6, protowire.BytesType)
	f3 = protowire.AppendBytes(f3, tc0Cont)
	buf.Write(helps.WrapConnectEnvelope(f3))

	// EOS frame
	buf.Write(helps.WrapConnectEnvelopeWithFlag(helps.ConnectFlagEndStream, []byte(`{}`)))

	interactionsJSON, respLog, err := consumeDevinFramesToInteractions(&buf, "devin/swe-2", "swe-2-high")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if respLog == nil {
		t.Fatal("expected non-nil respLog")
	}

	root := gjson.ParseBytes(interactionsJSON)
	steps := root.Get("steps").Array()
	if len(steps) != 2 {
		t.Fatalf("expected 2 function_call steps, got %d", len(steps))
	}
	if steps[0].Get("name").String() != "tool_0" || steps[0].Get("arguments").Raw != `{"a":1}` {
		t.Errorf("step 0 arguments = %q, want {\"a\":1}", steps[0].Get("arguments").Raw)
	}
	if steps[1].Get("name").String() != "tool_1" || steps[1].Get("arguments").Raw != `{"b":2}` {
		t.Errorf("step 1 arguments = %q, want {\"b\":2}", steps[1].Get("arguments").Raw)
	}
}

func TestStreamDevinFrames_CumulativeToolArgumentsStartOnce(t *testing.T) {
	var buf bytes.Buffer
	for _, args := range []string{`{"path":`, `{"path":"main.go"}`} {
		var toolCall []byte
		toolCall = protowire.AppendTag(toolCall, 1, protowire.BytesType)
		toolCall = protowire.AppendString(toolCall, "call_1")
		toolCall = protowire.AppendTag(toolCall, 2, protowire.BytesType)
		toolCall = protowire.AppendString(toolCall, "read_file")
		toolCall = protowire.AppendTag(toolCall, 3, protowire.BytesType)
		toolCall = protowire.AppendString(toolCall, args)
		var frame []byte
		frame = protowire.AppendTag(frame, 6, protowire.BytesType)
		frame = protowire.AppendBytes(frame, toolCall)
		buf.Write(helps.WrapConnectEnvelope(frame))
	}
	buf.Write(helps.WrapConnectEnvelopeWithFlag(helps.ConnectFlagEndStream, []byte(`{}`)))

	e := &DevinExecutor{}
	out := make(chan cliproxyexecutor.StreamChunk, 20)
	go func() {
		defer close(out)
		e.streamDevinFrames(
			context.Background(),
			&buf,
			cliproxyexecutor.Request{Model: "devin/swe-2"},
			cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatInteractions},
			"swe-2-high",
			sdktranslator.FormatInteractions,
			nil,
			out,
		)
	}()

	starts := 0
	var arguments strings.Builder
	for chunk := range out {
		if chunk.Err != nil {
			t.Fatalf("unexpected stream error: %v", chunk.Err)
		}
		raw := strings.TrimSpace(strings.TrimPrefix(string(chunk.Payload), "data: "))
		if raw == "" || raw == "[DONE]" {
			continue
		}
		event := gjson.Parse(raw)
		if event.Get("event_type").String() == "step.start" && event.Get("step.type").String() == "function_call" {
			starts++
		}
		if event.Get("event_type").String() == "step.delta" && event.Get("delta.type").String() == "arguments_delta" {
			arguments.WriteString(event.Get("delta.arguments").String())
		}
	}

	if starts != 1 {
		t.Fatalf("function_call starts = %d, want 1", starts)
	}
	if arguments.String() != `{"path":"main.go"}` {
		t.Fatalf("arguments = %q, want cumulative JSON", arguments.String())
	}
}

func TestConsumeDevinFramesToInteractions_PreservesMessageIDAndStopReason(t *testing.T) {
	var frame []byte
	frame = protowire.AppendTag(frame, 1, protowire.BytesType)
	frame = protowire.AppendString(frame, "message-1")
	frame = protowire.AppendTag(frame, 3, protowire.BytesType)
	frame = protowire.AppendString(frame, "partial")
	frame = protowire.AppendTag(frame, 5, protowire.VarintType)
	frame = protowire.AppendVarint(frame, 3)

	var buf bytes.Buffer
	buf.Write(helps.WrapConnectEnvelope(frame))
	buf.Write(helps.WrapConnectEnvelopeWithFlag(helps.ConnectFlagEndStream, []byte(`{}`)))

	payload, _, err := consumeDevinFramesToInteractions(&buf, "devin/swe-2", "swe-2-high")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	root := gjson.ParseBytes(payload)
	if got := root.Get("id").String(); got != "message-1" {
		t.Fatalf("id = %q, want message-1", got)
	}
	if got := root.Get("status").String(); got != "incomplete" {
		t.Fatalf("status = %q, want incomplete", got)
	}
	if got := root.Get("stop_reason").String(); got != "max_tokens" {
		t.Fatalf("stop_reason = %q, want max_tokens", got)
	}
}

func TestStreamDevinFrames_InterleavedThinkingAndContent(t *testing.T) {
	// Frame 1: thinking part 1
	var f1 []byte
	f1 = protowire.AppendTag(f1, 9, protowire.BytesType)
	f1 = protowire.AppendString(f1, "thought 1")

	// Frame 2: content text
	var f2 []byte
	f2 = protowire.AppendTag(f2, 3, protowire.BytesType)
	f2 = protowire.AppendString(f2, "content 1")

	// Frame 3: thinking part 2 (interleaved after content)
	var f3 []byte
	f3 = protowire.AppendTag(f3, 9, protowire.BytesType)
	f3 = protowire.AppendString(f3, "thought 2")

	// Frame 4: content text 2
	var f4 []byte
	f4 = protowire.AppendTag(f4, 3, protowire.BytesType)
	f4 = protowire.AppendString(f4, "content 2")

	var buf bytes.Buffer
	buf.Write(helps.WrapConnectEnvelope(f1))
	buf.Write(helps.WrapConnectEnvelope(f2))
	buf.Write(helps.WrapConnectEnvelope(f3))
	buf.Write(helps.WrapConnectEnvelope(f4))
	buf.Write(helps.WrapConnectEnvelopeWithFlag(helps.ConnectFlagEndStream, []byte(`{}`)))

	e := &DevinExecutor{}
	out := make(chan cliproxyexecutor.StreamChunk, 50)
	opts := cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FormatInteractions,
	}

	go func() {
		defer close(out)
		e.streamDevinFrames(
			context.Background(),
			&buf,
			cliproxyexecutor.Request{Model: "devin/swe-2"},
			opts,
			"swe-2-high",
			sdktranslator.FormatInteractions,
			nil,
			out,
		)
	}()

	var events []gjson.Result
	for chunk := range out {
		if chunk.Err != nil {
			t.Fatalf("unexpected chunk error: %v", chunk.Err)
		}
		raw := string(chunk.Payload)
		if strings.HasPrefix(raw, "data: ") && !strings.Contains(raw, "[DONE]") {
			data := strings.TrimPrefix(raw, "data: ")
			data = strings.TrimSpace(data)
			events = append(events, gjson.Parse(data))
		}
	}

	// Verify step sequence:
	// 1. step.start (0, thought)
	// 2. step.stop (0)
	// 3. step.start (1, model_output)
	// 4. step.stop (1)
	// 5. step.start (2, thought)
	// 6. step.stop (2)
	// 7. step.start (3, model_output)
	// 8. step.stop (3)
	var stepEvents []string
	for _, ev := range events {
		eventType := ev.Get("event_type").String()
		if eventType == "step.start" {
			stepEvents = append(stepEvents, fmt.Sprintf("start(%d,%s)", ev.Get("index").Int(), ev.Get("step.type").String()))
		} else if eventType == "step.stop" {
			stepEvents = append(stepEvents, fmt.Sprintf("stop(%d)", ev.Get("index").Int()))
		}
	}

	expectedEvents := []string{
		"start(0,thought)",
		"stop(0)",
		"start(1,model_output)",
		"stop(1)",
		"start(2,thought)",
		"stop(2)",
		"start(3,model_output)",
		"stop(3)",
	}

	if len(stepEvents) != len(expectedEvents) {
		t.Fatalf("got step events %v, want %v", stepEvents, expectedEvents)
	}
	for i := range expectedEvents {
		if stepEvents[i] != expectedEvents[i] {
			t.Errorf("step event %d = %s, want %s", i, stepEvents[i], expectedEvents[i])
		}
	}
}

func TestDevinExecutorRejectsGenerationErrors(t *testing.T) {
	for _, reason := range []uint64{7, 13} {
		t.Run(fmt.Sprint(reason), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/exa.auth_pb.AuthService/GetUserJwt" {
					response := protowire.AppendTag(nil, 1, protowire.BytesType)
					_, _ = w.Write(protowire.AppendString(response, "test-jwt"))
					return
				}
				frame := protowire.AppendTag(nil, 5, protowire.VarintType)
				frame = protowire.AppendVarint(frame, reason)
				_, _ = w.Write(helps.WrapConnectEnvelope(frame))
				_, _ = w.Write(helps.WrapConnectEnvelopeWithFlag(helps.ConnectFlagEndStream, []byte(`{}`)))
			}))
			defer server.Close()
			exec := NewDevinExecutor(&config.Config{})
			auth := &cliproxyauth.Auth{Attributes: map[string]string{"api_key": "test-key", "base_url": server.URL}}
			req := cliproxyexecutor.Request{Model: "devin/swe-2", Payload: []byte(`{"input":"hello"}`)}
			opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatInteractions}
			if _, err := exec.Execute(context.Background(), auth, req, opts); err == nil {
				t.Error("generation error accepted as successful response")
			} else if status, ok := err.(interface{ StatusCode() int }); !ok || status.StatusCode() != http.StatusBadGateway {
				t.Errorf("error = %v, want HTTP 502", err)
			}
			stream, err := exec.ExecuteStream(context.Background(), auth, req, opts)
			if err != nil {
				t.Fatal(err)
			}
			failed := false
			for chunk := range stream.Chunks {
				if chunk.Err != nil {
					failed = true
				}
				if bytes.Contains(chunk.Payload, []byte("interaction.completed")) || bytes.Contains(chunk.Payload, []byte("[DONE]")) {
					t.Errorf("generation failure emitted success: %s", chunk.Payload)
				}
			}
			if !failed {
				t.Error("generation error accepted as successful stream")
			}
		})
	}
}

func TestDevinExecutorCanonicalThinking(t *testing.T) {
	for _, tc := range []struct {
		name, model, body, want string
		format                  sdktranslator.Format
	}{
		{"alias capabilities", "devin/gemini-3-flash", `{"generation_config":{"thinking_level":"minimal"}}`, "gemini-3-8-flash-low", sdktranslator.FormatInteractions},
		{"native UID", "devin/gpt-6-astra-xhigh", `{"generation_config":{"thinking_level":"low"}}`, "gpt-6-astra-xhigh", sdktranslator.FormatInteractions},
		{"numeric suffix", "devin/gpt-6-astra(32768)", `{}`, "gpt-6-astra-xhigh", sdktranslator.FormatInteractions},
		{"suffix overrides body", "devin/gpt-6-astra(32768)", `{"generation_config":{"thinking_level":"low"}}`, "gpt-6-astra-xhigh", sdktranslator.FormatInteractions},
		{"budget", "devin/gpt-6-astra", `{"generation_config":{"thinking_config":{"thinking_budget":4096}}}`, "gpt-6-astra-medium", sdktranslator.FormatInteractions},
		{"Claude budget", "devin/gpt-6-astra", `{"messages":[{"role":"user","content":"hello"}],"thinking":{"type":"enabled","budget_tokens":4096}}`, "gpt-6-astra-medium", sdktranslator.FormatClaude},
		{"disabled", "devin/glm-5-2(0)", `{}`, "glm-5-2-none", sdktranslator.FormatInteractions},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == helps.DevinAuthPath {
					_, _ = w.Write(appendDevinFieldBytes(nil, 1, []byte("test-jwt")))
					return
				}
				_, body, err := helps.ReadConnectFrame(r.Body)
				if err != nil {
					t.Error(err)
					return
				}
				if got := protoStringFieldForTest(t, body, 21); got != tc.want {
					t.Errorf("chat_model_uid = %q, want %q", got, tc.want)
				}
				_, _ = w.Write(helps.WrapConnectEnvelopeWithFlag(helps.ConnectFlagEndStream, []byte(`{}`)))
			}))
			defer server.Close()
			exec := NewDevinExecutor(&config.Config{})
			auth := &cliproxyauth.Auth{Attributes: map[string]string{"api_key": "test-key", "base_url": server.URL}}
			req := cliproxyexecutor.Request{Model: tc.model, Payload: []byte(tc.body)}
			_, err := exec.Execute(context.Background(), auth, req, cliproxyexecutor.Options{SourceFormat: tc.format, OriginalRequest: req.Payload})
			if err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestDevinExecutorPreservesToolResultImages(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		format     sdktranslator.Format
	}{
		{"Claude", `{"messages":[{"role":"user","content":[{"type":"tool_result","tool_use_id":"screenshot-1","content":[{"type":"text","text":"screen captured"},{"type":"image","source":{"type":"base64","media_type":"image/png","data":"aW1hZ2U="}}]}]}]}`, sdktranslator.FormatClaude},
		{"Interactions", `{"input":[{"type":"function_result","call_id":"screenshot-1","result":[{"type":"text","text":"screen captured"},{"type":"image","mime_type":"image/png","data":"aW1hZ2U="}]}]}`, sdktranslator.FormatInteractions},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == helps.DevinAuthPath {
					_, _ = w.Write(appendDevinFieldBytes(nil, 1, []byte("test-jwt")))
					return
				}
				_, wire, err := helps.ReadConnectFrame(r.Body)
				if err != nil {
					t.Error(err)
					return
				}
				prompt := protoBytesFieldForTest(t, wire, 3)
				if got := protoStringFieldForTest(t, prompt, 7); got != "screenshot-1" {
					t.Errorf("tool call id=%q", got)
				}
				if got := protoStringFieldForTest(t, prompt, 3); !strings.Contains(got, "screen captured") {
					t.Errorf("tool text=%q", got)
				}
				image := protoBytesFieldForTest(t, prompt, 10)
				if got := protoStringFieldForTest(t, image, 1); got != "aW1hZ2U=" {
					t.Errorf("image data=%q", got)
				}
				if got := protoStringFieldForTest(t, image, 2); got != "image/png" {
					t.Errorf("image MIME=%q", got)
				}
				_, _ = w.Write(helps.WrapConnectEnvelopeWithFlag(helps.ConnectFlagEndStream, []byte(`{}`)))
			}))
			defer server.Close()
			exec := NewDevinExecutor(&config.Config{})
			auth := &cliproxyauth.Auth{Attributes: map[string]string{"api_key": "test-key", "base_url": server.URL}}
			req := cliproxyexecutor.Request{Model: "devin/swe-2", Payload: []byte(tc.body)}
			_, err := exec.Execute(context.Background(), auth, req, cliproxyexecutor.Options{SourceFormat: tc.format, OriginalRequest: req.Payload})
			if err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestDevinExecutorPreservesStructuredToolResults(t *testing.T) {
	for _, result := range []string{`[1,2]`, `[{"name":"x"}]`, `42`, `false`, `null`, `[]`, `{"ok":true}`} {
		t.Run(result, func(t *testing.T) {
			prompts := make(chan string, 1)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == helps.DevinAuthPath {
					_, _ = w.Write(appendDevinFieldBytes(nil, 1, []byte("test-jwt")))
					return
				}
				_, wire, err := helps.ReadConnectFrame(r.Body)
				if err != nil {
					t.Error(err)
					return
				}
				prompts <- protoStringFieldForTest(t, protoBytesFieldForTest(t, wire, 3), 3)
				_, _ = w.Write(helps.WrapConnectEnvelopeWithFlag(helps.ConnectFlagEndStream, []byte(`{}`)))
			}))
			defer server.Close()
			req := cliproxyexecutor.Request{Model: "devin/swe-2", Payload: []byte(`{"input":[{"type":"function_result","call_id":"call-1","result":` + result + `}]}`)}
			auth := &cliproxyauth.Auth{Attributes: map[string]string{"api_key": "test-key", "base_url": server.URL}}
			_, err := NewDevinExecutor(&config.Config{}).Execute(context.Background(), auth, req, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatInteractions})
			if err != nil {
				t.Fatal(err)
			}
			if got := <-prompts; got != result {
				t.Errorf("tool result=%q, want %q", got, result)
			}
		})
	}
}
