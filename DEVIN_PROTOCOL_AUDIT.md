# Devin Protocol Audit Against oh-my-pi

## Scope

This audit compares CLIProxyAPI commit `7fa443dc8bf8ca2f1ffd81c2472deb31b097b697` with oh-my-pi commit [`becbf82cb2c0b598e27cf0bf10453065645d915e`](https://github.com/can1357/oh-my-pi/tree/becbf82cb2c0b598e27cf0bf10453065645d915e), fetched on 2026-09-14.

The strongest evidence is the protobuf schema checked into oh-my-pi. oh-my-pi is still a reverse-engineered client rather than an official protocol specification, so implementation-only differences are separated from schema-confirmed defects.

## Executive summary

CLIProxyAPI has four high-confidence response-conversion defects:

1. It decodes a nonexistent tool-call index from field 4.
2. It concatenates cumulative `arguments_json` snapshots as if they were deltas.
3. It emits duplicate tool starts when repeated frames carry the same ID/name.
4. It parses but ignores `stop_reason`, producing incorrect downstream finish reasons.

It also has substantial request-fidelity and current-client compatibility gaps: no `GetUserJwt` chat bootstrap, no model-router assignment, dropped tool-choice/stop/top-p/strict/error semantics, unstable history message IDs, signature provenance loss, and lossy usage decoding.

## Confirmed defects

### 1. `ChatToolCall.field 4` is not an index

**Severity: Critical for parallel tool calls**

CLIProxyAPI treats a varint field 4 as `DevinToolCallDelta.Index`:

- [`internal/runtime/executor/helps/devin_wire.go`](internal/runtime/executor/helps/devin_wire.go), `parseDevinToolCallDelta`, lines 649-691.
- [`internal/runtime/executor/devin_executor_test.go`](internal/runtime/executor/devin_executor_test.go), lines 771-839, constructs field 4 as a varint index.

The current schema defines:

```proto
message ChatToolCall {
  string id = 1;
  string name = 2;
  string arguments_json = 3;
  string invalid_json_str = 4;
  string invalid_json_err = 5;
  bool is_custom_tool_call = 6;
}
```

Source: [oh-my-pi `codeium_common.proto`, lines 3501-3508](https://github.com/can1357/oh-my-pi/blob/becbf82cb2c0b598e27cf0bf10453065645d915e/packages/ai/src/providers/devin/proto/exa/codeium_common_pb/codeium_common.proto#L3501-L3508).

There is no wire index. Normal responses therefore leave every local `Index` at zero. Multiple tool calls collapse into one builder/step, and the current test passes only because it emits a field shape that the schema cannot produce.

**Required direction:** key tool-call state by `id`, with an active-ID fallback for continuation frames. Do not infer an index from field 4.

### 2. Cumulative tool arguments are corrupted

**Severity: Critical for streamed tool calls**

CLIProxyAPI always appends every `arguments_json` value:

- Streaming forwards the whole value as an `arguments_delta`: [`internal/runtime/executor/devin_executor.go`](internal/runtime/executor/devin_executor.go), lines 745-750.
- Non-streaming uses `strings.Builder.WriteString`: same file, lines 1006-1023.

Current Devin frames can carry cumulative snapshots. oh-my-pi handles both cumulative and incremental forms by checking whether the new value starts with the previous buffer and emitting only the suffix: [provider lines 371-409](https://github.com/can1357/oh-my-pi/blob/becbf82cb2c0b598e27cf0bf10453065645d915e/packages/ai/src/providers/devin.ts#L371-L409). Its regression test sends cumulative snapshots: [test lines 46-85](https://github.com/can1357/oh-my-pi/blob/becbf82cb2c0b598e27cf0bf10453065645d915e/packages/ai/test/devin-streaming-args.test.ts#L46-L85).

For snapshots `{"x":1` followed by `{"x":12}`, CLIProxyAPI produces `{"x":1{"x":12}` instead of `{"x":12}`.

### 3. Repeated ID/name frames produce duplicate tool starts

**Severity: High**

For an existing tool-call step, CLIProxyAPI emits another `step.start` whenever a later frame repeats the ID or name: [`internal/runtime/executor/devin_executor.go`](internal/runtime/executor/devin_executor.go), lines 725-743.

This is not a harmless metadata update:

- The OpenAI Chat translator emits another tool-call start chunk.
- The Claude translator closes the active block and opens a new `tool_use` block on every `step.start`: [`internal/translator/interactions/claude/interactions_claude_response.go`](internal/translator/interactions/claude/interactions_claude_response.go), lines 140-159 and 225-237.

The upstream cumulative-argument test repeats the same ID/name in each frame, so this path can split one Devin tool call into multiple Claude tool calls.

**Required direction:** emit exactly one start per tool-call ID; update internal metadata without emitting another start.

### 4. `stop_reason` is parsed and then ignored

**Severity: High**

The parser stores response field 5, but neither streaming nor non-streaming consumption uses it. The local comment is also wrong: it says `2/4=stop`, while 4 is `MIN_LOG_PROB`.

- Local parse: [`internal/runtime/executor/helps/devin_wire.go`](internal/runtime/executor/helps/devin_wire.go), lines 106-121 and 536-543.
- Current enum: [oh-my-pi proto lines 766-781](https://github.com/can1357/oh-my-pi/blob/becbf82cb2c0b598e27cf0bf10453065645d915e/packages/ai/src/providers/devin/proto/exa/codeium_common_pb/codeium_common.proto#L766-L781).

CLIProxyAPI always emits `interaction.completed` with `status: completed`; downstream translators then choose only `tool_calls`/`tool_use` or ordinary stop. `MAX_TOKENS` (3) is therefore reported as `stop`/`end_turn` instead of `length`/`max_tokens`. Content-filter and error stop reasons are also silently converted into successful completion.

### 5. Response IDs are decoded under the wrong meaning and discarded

**Severity: High for native-history continuity**

Current response fields are `message_id = 1`, `output_id = 15`, and `request_id = 17`: [oh-my-pi proto lines 569-597](https://github.com/can1357/oh-my-pi/blob/becbf82cb2c0b598e27cf0bf10453065645d915e/packages/ai/src/providers/devin/proto/exa/api_server_pb/api_server.proto#L569-L597).

CLIProxyAPI labels field 1 `OutputID`, field 17 `MessageID`, ignores field 15, and does not expose any of them in the generated interaction. It mints a random interaction ID instead. See [`internal/runtime/executor/helps/devin_wire.go`](internal/runtime/executor/helps/devin_wire.go), lines 106-121 and 569-590, and [`internal/runtime/executor/devin_executor.go`](internal/runtime/executor/devin_executor.go), lines 454-455 and 855-856.

This also prevents subsequent requests from reusing Devin's native assistant message ID. oh-my-pi preserves a native response ID and uses deterministic IDs for reconstructed user/tool history: [provider lines 654-726](https://github.com/can1357/oh-my-pi/blob/becbf82cb2c0b598e27cf0bf10453065645d915e/packages/ai/src/providers/devin.ts#L654-L726).

### 6. Usage fields 4 and 6 are misidentified

**Severity: Medium**

Current `ModelUsageStats` defines field 4 as `cache_write_tokens` and field 6 as `api_provider`: [oh-my-pi proto lines 3646-3658](https://github.com/can1357/oh-my-pi/blob/becbf82cb2c0b598e27cf0bf10453065645d915e/packages/ai/src/providers/devin/proto/exa/codeium_common_pb/codeium_common.proto#L3646-L3658).

CLIProxyAPI treats field 4 as extra prompt tokens and field 6 as a status code: [`internal/runtime/executor/helps/devin_wire.go`](internal/runtime/executor/helps/devin_wire.go), lines 755-827. The overall token sum may coincidentally remain correct because cache-write tokens are folded into prompt tokens, but cache-write accounting is lost and the decoded field meanings are wrong.

## Request and compatibility gaps

### 7. Current chat authentication bootstrap is missing

**Severity: High compatibility risk**

The current flow calls `AuthService/GetUserJwt`, follows `custom_api_server_url` when returned, and sends the JWT in `Metadata.user_jwt` (field 21):

- Auth schema: [oh-my-pi `auth.proto`, lines 8-18](https://github.com/can1357/oh-my-pi/blob/becbf82cb2c0b598e27cf0bf10453065645d915e/packages/ai/src/providers/devin/proto/exa/auth_pb/auth.proto#L8-L18).
- Client flow: [chat setup, lines 164-208](https://github.com/can1357/oh-my-pi/blob/becbf82cb2c0b598e27cf0bf10453065645d915e/packages/ai/src/providers/devin.ts#L164-L208) and [JWT/custom-host bootstrap, lines 486-519](https://github.com/can1357/oh-my-pi/blob/becbf82cb2c0b598e27cf0bf10453065645d915e/packages/ai/src/providers/devin.ts#L486-L519).

CLIProxyAPI puts only the session token into metadata field 3, adds a legacy `Authorization: Basic token-token` header, and never obtains or sends `user_jwt`: [`internal/runtime/executor/devin_executor.go`](internal/runtime/executor/devin_executor.go), lines 95-122 and 370-439.

This may continue working against a backward-compatible public edge, but it cannot honor server-selected custom API hosts and is not equivalent to the current released-client flow.

### 8. Generation controls are dropped or hard-coded

**Severity: High request-fidelity defect**

CLIProxyAPI's completion configuration uses:

- default temperature `1.0`
- `max_newlines = 400`
- `top_k = 40`
- `top_p = 0.95`
- no `first_temperature`
- no stop patterns

See [`internal/runtime/executor/helps/devin_wire.go`](internal/runtime/executor/helps/devin_wire.go), lines 416-441.

The current oh-my-pi client uses temperature `0.4`, max newlines `200`, top-k `50`, top-p `1`, first temperature, stop patterns, and FIM threshold: [provider lines 597-624](https://github.com/can1357/oh-my-pi/blob/becbf82cb2c0b598e27cf0bf10453065645d915e/packages/ai/src/providers/devin.ts#L597-L624).

More importantly, CLIProxyAPI ignores caller `top_p` and stop sequences entirely, so protocol conversion does not preserve valid OpenAI/Claude request semantics.

### 9. Tool-choice, parallelism, strictness, and tool-error semantics are lost

**Severity: High for constrained tool use**

The current request schema has `disable_parallel_tool_calls = 11`, `tool_choice = 12`, and tool definition `strict = 12`; tool-result prompts have `tool_result_is_error = 9`:

- [GetChatMessageRequest fields](https://github.com/can1357/oh-my-pi/blob/becbf82cb2c0b598e27cf0bf10453065645d915e/packages/ai/src/providers/devin/proto/exa/api_server_pb/api_server.proto#L538-L567)
- [Chat prompt/tool fields](https://github.com/can1357/oh-my-pi/blob/becbf82cb2c0b598e27cf0bf10453065645d915e/packages/ai/src/providers/devin/proto/exa/chat_pb/chat.proto#L340-L392)

CLIProxyAPI serializes none of these. Consequently:

- `tool_choice: none` can still call tools.
- required/named tool choice is not enforced.
- `parallel_tool_calls: false` is not honored.
- strict tool schemas become non-strict.
- failed tool results are sent as successful results.

The Claude-to-interactions translator already drops `tool_result.is_error`, so preserving this requires a change before or alongside the Devin executor: [`internal/translator/interactions/claude/interactions_claude_request.go`](internal/translator/interactions/claude/interactions_claude_request.go), lines 272-298.

### 10. History IDs and signature provenance are unstable

**Severity: High for long-running mixed-provider conversations**

CLIProxyAPI generates a fresh UUID for every history item each request and forwards detected Claude/OpenAI/Gemini signatures into Devin prompts without proving that the assistant message originated from the same Devin model/provider. See [`internal/runtime/executor/devin_executor.go`](internal/runtime/executor/devin_executor.go), lines 1189-1266 and 1557-1698.

oh-my-pi only reuses a signature/response ID for a native Devin assistant message; foreign reasoning is demoted to ordinary prompt text and foreign signatures are removed: [provider lines 667-703](https://github.com/can1357/oh-my-pi/blob/becbf82cb2c0b598e27cf0bf10453065645d915e/packages/ai/src/providers/devin.ts#L667-L703), with a regression test at [lines 64-109](https://github.com/can1357/oh-my-pi/blob/becbf82cb2c0b598e27cf0bf10453065645d915e/packages/ai/test/devin-history.test.ts#L64-L109).

Forwarding a foreign or transformed signature can cause signature validation failures; randomizing IDs also defeats stable history identity and may reduce cache continuity.

### 11. Current request/response metadata is omitted

**Severity: Medium**

CLIProxyAPI does not send current optional-but-useful request fields such as:

- `system_prompt_cache_options` (ephemeral)
- `execution_id`
- tool `strict`

It also ignores response fields including:

- `actual_model_uid`
- `credit_cost`
- `committed_credit_cost`
- `committed_acu_cost`
- `phase`

This loses actual routing and billing information even when generation succeeds. See current request/response schema at [api_server.proto lines 538-597](https://github.com/can1357/oh-my-pi/blob/becbf82cb2c0b598e27cf0bf10453065645d915e/packages/ai/src/providers/devin/proto/exa/api_server_pb/api_server.proto#L538-L597).

### 12. Model-router assignment is unsupported

**Severity: Medium; becomes High if `adaptive` is exposed**

For router models, the current client must call `AssignModel`, use the returned concrete model UID, and forward `model_assignment_jwt`; sending the router UID directly is invalid. See [provider lines 175-183 and 521-568](https://github.com/can1357/oh-my-pi/blob/becbf82cb2c0b598e27cf0bf10453065645d915e/packages/ai/src/providers/devin.ts#L175-L183) and [router tests](https://github.com/can1357/oh-my-pi/blob/becbf82cb2c0b598e27cf0bf10453065645d915e/packages/ai/test/devin-router.test.ts#L130-L212).

CLIProxyAPI has no `AssignModel` flow and does not parse the router marker from model metadata. Its currently published catalog does not expose `adaptive`, limiting immediate impact, but the protocol implementation is incomplete.

## Important differences that are not proven defects

1. **Field 15 is not “thread session metadata.”** It is `CortexTrajectoryReference`. However, CLIProxyAPI's nested fields map coherently to `trajectory_id`, `step_index`, `trajectory_type = CASCADE`, and sometimes `step_type = USER_INPUT`, so removing it solely because oh-my-pi omits it would be unjustified. The local names/comments should be corrected.
2. **Uncompressed request frames are valid.** oh-my-pi uses gzip; CLIProxyAPI's uncompressed Connect frame is not inherently wrong.
3. **Strict EOS enforcement is reasonable.** CLIProxyAPI rejects a body that closes before a Connect end-stream trailer; this is stricter than oh-my-pi and should not be treated as an error.
4. **Metadata version and field 31 need live verification.** CLIProxyAPI uses version `3000.10.21` and a 732-character value in metadata field 31 (`f`), while oh-my-pi pins `3000.6.2` and omits `f`. The schema alone does not establish what opaque field `f` means or which released identity the backend currently gates on.
5. **System-prompt sanitization and synthetic image labels are policy choices.** They reduce wire fidelity compared with oh-my-pi but may be intentional product behavior.

## Repair status

All confirmed defects and the high-severity compatibility gaps have been fixed:

| Finding | Status |
| --- | --- |
| 1. `ChatToolCall.field 4` treated as index | **Fixed.** `DevinToolCallDelta.Index` removed; tool-call state is keyed by call ID with an active-ID fallback for continuation frames (`devinToolCallAccumulator` in `devin_executor.go`). |
| 2. Cumulative `arguments_json` corrupted | **Fixed.** `mergeDevinToolArguments` emits only the suffix when a frame carries a cumulative snapshot, and still accepts true deltas. |
| 3. Duplicate tool starts | **Fixed.** Exactly one `step.start` per tool-call ID; repeated ID/name frames only update state. |
| 4. `stop_reason` ignored | **Fixed.** `devinTerminalState` maps stop reasons to interactions status/stop_reason: `MAX_TOKENS` → `incomplete`/`max_tokens`, `FUNCTION_CALL` → `tool_calls`, `CONTENT_FILTER` → `incomplete`/`content_filter`, `ERROR`/non-finite → `failed`/`error`. OpenAI chat (`length`/`tool_calls`/`content_filter`), OpenAI responses (`incomplete`/`failed` + `incomplete_details.reason`), and Claude (`max_tokens`/`tool_use`/`refusal`) translators consume it for stream and non-stream. |
| 5. Response IDs misnamed/discarded | **Fixed.** `message_id` (1), `output_id` (15), `request_id` (17), and `actual_model_uid` (23) are decoded distinctly; the native message ID becomes the interaction ID, and `actual_model_uid` surfaces as `upstream_model`. |
| 6. Usage fields misidentified | **Fixed.** `ModelUsageStats` fields are decoded per schema: `input_tokens` (2), `output_tokens` (3), `cache_write_tokens` (4), `cache_read_tokens` (5), `api_provider` (6, no longer a status code), plus `message_id`/`response_header`/`model_uid`/`billing_model_uid`/`requested_model_uid`. |
| 7. `GetUserJwt` bootstrap missing | **Fixed.** Every chat turn calls `AuthService/GetUserJwt` first, follows `custom_api_server_url`, and sends the JWT in metadata field 21; the legacy `Authorization: Basic` header is gone from chat. |
| 8. Generation controls dropped | **Fixed.** Caller `temperature`, `top_p`, `max_output_tokens` (clamped to catalog `max_completion_tokens`), and stop sequences are encoded; first-temperature mirrors temperature like the native client. |
| 9. Tool semantics lost | **Fixed.** `tool_choice` (auto/none/required/named via the `ChatToolChoice` oneof), `disable_parallel_tool_calls` (field 11), tool `strict` (field 12), and `tool_result_is_error` (field 9) are all preserved through the interactions layer. Parallel calls are also disabled for catalog models that do not declare `supports_parallel_tool_calls`, matching the native client. |
| 10. History IDs/signatures | **Fixed.** History message IDs are deterministic UUIDv5 over `cascadeID/index/source/toolCallID` (matching oh-my-pi's seed scheme); native response IDs are reused when present; signatures typed `anthropic`/`openai`/`gemini` are filtered out by `devinSignatureBytes`. |
| 11. Response metadata omitted | **Partially fixed.** `actual_model_uid` and the full usage identity set are preserved. `credit_cost`/`committed_acu_cost`/`phase` remain informational only in the upstream response log. |
| 12. Router assignment unsupported | **Fixed.** `IsDevinModelRouter` detects catalog entries with `is_model_router`; the executor calls `AssignModel` (sharing the cascade ID, last user prompt with empty message ID), uses the returned `model_uid` as `chat_model_uid`, forwards `model_assignment_jwt` (field 26), and fails the turn on incomplete assignments. `fetch_devin_models` now parses `model_info` for `is_model_router`/`display_option`, `supports_parallel_tool_calls`, and `max_output_tokens`; `adaptive` was added to the embedded catalog. |

## Remaining gaps

- `Metadata` still uses the locally observed client version/field-31 fingerprint (`3000.10.21` + `f`) rather than oh-my-pi's `3000.6.2`; this requires live verification before changing.
- `credit_cost`, `committed_credit_cost`, `committed_acu_cost`, and `phase` are logged but not surfaced in interactions usage.
- `response_dimension_groups` remain a usage fallback only; per-dimension detail is not exposed.

## Verification

- `gofmt -w .` and `git diff --check`: clean.
- `go test ./internal/runtime/executor ./internal/runtime/executor/helps -run Devin`: passes with schema-valid fixtures (tool calls encoded with fields 1-3 only).
- `go test ./internal/translator/openai/interactions/chat-completions ./internal/translator/openai/interactions/responses ./internal/translator/interactions/claude`: all pass.
- `go test ./...`: fails only in the unrelated pre-existing `TestOpenAICompatExecutorToolResultContentByInputModalities`, which fails identically on the base commit.
- `go build -o cli-proxy-api ./cmd/server` and `go build -o test-output ./cmd/server`: pass.

## Recommended repair order

1. Replace hand-maintained response parsing with generated protobuf types, or at minimum align it exactly with the checked-in schema.
2. Rebuild tool-call accumulation around call IDs and cumulative-snapshot handling; replace the current synthetic-index tests with schema-valid fixtures.
3. Propagate stop reasons into the interactions status/finish-reason model.
4. Preserve native response IDs and enforce signature provenance.
5. Add `GetUserJwt`/custom-host bootstrap and then router assignment.
6. Preserve generation controls and tool semantics (`tool_choice`, parallel disable, strict, tool-result errors).
7. Split usage into input/output/cache-read/cache-write and expose routing/billing metadata.

## Minimum regression tests

- Two tool calls encoded with schema-valid fields 1-3 only; both survive stream and non-stream conversion.
- Cumulative argument snapshots emit only suffix deltas and finalize to valid JSON.
- Repeated ID/name frames emit one downstream tool start.
- stop reasons 3, 10, 11, and 13 map to length, tool use, content filter, and error.
- usage `(input=11, output=7, cacheWrite=13, cacheRead=100)` retains all four values and totals 131.
- native message IDs survive a second turn; foreign signatures are not forwarded.
- `tool_choice=none`, named choice, `parallel_tool_calls=false`, strict tools, and failed tool results are preserved.
- `GetUserJwt` custom-host and `AssignModel` flows are sequenced and validated.

## Code review follow-up (2026-09-14)

The nine code-quality and behavior findings from the follow-up review have been addressed:

| Area | Final behavior |
| --- | --- |
| Canonical thinking | Devin uses `ApplyThinkingWithModelInfo` and a provider applier. Numeric suffixes, source budgets, and explicit `none` use the shared validation/conversion rules. The executor uses one destination model definition for thinking, token limits, and parallel-tool support; native effort UIDs remain intact. |
| Catalog ownership | `models/devin_models.json` is the sole embedded Devin catalog. The secondary `models.json` section, hardcoded list, unused raw/revision APIs, and alternate JSON envelopes were removed. The fetch tool and updater use the same `{"devin": [...]}` format. |
| Catalog lifecycle | Both catalogs reuse the same refresh-loop implementation but run independently, retaining Home-mode Devin refresh. Requests remain under caller cancellation until their bodies are read and closed. Changed Devin catalogs notify the existing model refresh callback; identical catalogs do not. |
| Network timeouts | Quota refresh and model-catalog fetches no longer impose whole-request timeouts. Credential acquisition retains its timeout. |
| Shared identity | `internal/auth/devin/metadata.go` owns fingerprint generation and common metadata encoding. The existing chat/status application identities and client version are preserved. |
| Generation failure | Stop reasons `7` and `13` produce HTTP 502 for non-streaming requests and an error chunk for streams. They are not emitted as successful completion; failure accounting is recorded. |
| System instructions | Hardcoded line deletion was removed. Only CRLF normalization and explicitly configured sensitive-word obfuscation remain; obfuscation does not delete instructions. |
| Tool definitions | Descriptions and schemas are forwarded without the `task_id` to `taskId` rewrite. |
| Tool results | Claude and Interactions tool-result images reach protobuf `images` field 10. Ordinary JSON arrays, objects, numbers, booleans, and null remain intact instead of being mistaken for multimodal content blocks. |

Regression tests exercise the executor against a mock upstream, checking actual protobuf requests and client-visible failure behavior. Additional tests cover caller instruction preservation, catalog cancellation/notifications, and independent catalog refresh lifecycles without wall-clock sleeps.

Validation after the follow-up:

- Devin executor, wire, authentication, registry, server catalog-plan, translator, thinking, and SDK checks passed.
- Race-enabled Devin/catalog/fingerprint tests passed in registry, authentication, wire helpers, and executor packages.
- `gofmt -w .`, `git diff --check`, and `go build -o test-output ./cmd/server` passed.
- `go test ./...` still fails in `TestOpenAICompatExecutorToolResultContentByInputModalities`; the same failure was reproduced on the pre-Devin baseline `d23ba5ee` during review.
- The repaired binary was tested using a temporary copy of the existing EasyCLIProxyAPI Devin OAuth record: `devin/swe-2` returned HTTP 200, `OK`, and `finish_reason: stop`.
- The repaired binary also completed a named streaming tool call: one `record_value` call with `{"value":7}`, HTTP 200, and `finish_reason: tool_calls`.
- The initial `adaptive` request returned the account's daily-quota-exhausted response. The successful follow-up below resolves that verification gap. No new login or purchase was performed.

## Adaptive live retest (2026-09-15)

The current working-tree binary successfully completed a `devin/adaptive` request using a temporary copy of the EasyCLIProxyAPI OAuth record. The source record was disabled; only the isolated copy was enabled for this user-authorized test.

- Endpoint: OpenAI-compatible `/v1/chat/completions`, non-streaming.
- Result: HTTP 200 in approximately 2.6 seconds, response text `OK.`, `finish_reason: stop`.
- Usage: 353 input tokens, 15 output tokens, 368 total tokens.
- This verifies the current JWT bootstrap, model assignment, and chat generation path. It supersedes the earlier quota-limited verification result.
