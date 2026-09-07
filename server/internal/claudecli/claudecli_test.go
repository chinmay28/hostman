package claudecli

import (
	"encoding/json"
	"strings"
	"testing"
)

// The lines here are what the CLI actually prints, trimmed of the fields
// Decode does not read, so the test is against the protocol and not against
// a story about it.

func one(t *testing.T, line string) Event {
	t.Helper()
	evs, err := Decode([]byte(line))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(evs) != 1 {
		t.Fatalf("got %d events, want 1: %+v", len(evs), evs)
	}
	return evs[0]
}

func TestDecodeInit(t *testing.T) {
	ev := one(t, `{"type":"system","subtype":"init","cwd":"/home/pi/apps","session_id":"11d9","tools":["Bash"],"model":"claude-opus-5[1m]","permissionMode":"default"}`)
	if ev.Kind != KindInit || ev.Model != "claude-opus-5[1m]" || ev.Mode != "default" || ev.Cwd != "/home/pi/apps" {
		t.Fatalf("init = %+v", ev)
	}
}

func TestDecodeAssistantBlocks(t *testing.T) {
	ev := one(t, `{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"I'll check the journal."},{"type":"tool_use","id":"toolu_1","name":"Bash","input":{"command":"journalctl -u homebridge"}}],"usage":{"input_tokens":12,"cache_read_input_tokens":30000,"cache_creation_input_tokens":500,"output_tokens":40}}}`)
	if ev.Kind != KindAssistant || len(ev.Blocks) != 2 {
		t.Fatalf("assistant = %+v", ev)
	}
	if ev.Blocks[0].Type != "text" || ev.Blocks[0].Text != "I'll check the journal." {
		t.Errorf("text block = %+v", ev.Blocks[0])
	}
	if b := ev.Blocks[1]; b.Type != "tool_use" || b.ID != "toolu_1" || b.Name != "Bash" || !strings.Contains(string(b.Input), "journalctl") {
		t.Errorf("tool block = %+v", b)
	}
	if ev.Context != 30512 {
		t.Errorf("context = %d, want the input and both cache figures added up", ev.Context)
	}
}

// The CLI's synthetic "could not reach the API" answer is a plain string body.
func TestDecodeAssistantWithAStringBody(t *testing.T) {
	ev := one(t, `{"type":"assistant","message":{"role":"assistant","content":"Authentication error"}}`)
	if len(ev.Blocks) != 1 || ev.Blocks[0].Text != "Authentication error" {
		t.Fatalf("blocks = %+v", ev.Blocks)
	}
}

func TestDecodeToolResults(t *testing.T) {
	evs, err := Decode([]byte(`{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_1","content":"line one\nline two"},{"type":"tool_result","tool_use_id":"toolu_2","content":[{"type":"text","text":"a"},{"type":"text","text":"b"}],"is_error":true}]}}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(evs) != 2 {
		t.Fatalf("got %d events, want one per result", len(evs))
	}
	if evs[0].Kind != KindToolResult || evs[0].ToolUseID != "toolu_1" || evs[0].Content != "line one\nline two" || evs[0].IsError {
		t.Errorf("first = %+v", evs[0])
	}
	if evs[1].ToolUseID != "toolu_2" || evs[1].Content != "a\nb" || !evs[1].IsError {
		t.Errorf("second = %+v", evs[1])
	}
}

// A user line that is only an echo of what was typed carries no tool result
// and so is nothing to show: the session already logged what the user said.
func TestDecodeIgnoresEchoedUserText(t *testing.T) {
	evs, err := Decode([]byte(`{"type":"user","message":{"role":"user","content":"hello"}}`))
	if err != nil || len(evs) != 0 {
		t.Fatalf("evs = %+v, err = %v", evs, err)
	}
}

func TestDecodePermissionRequest(t *testing.T) {
	ev := one(t, `{"type":"control_request","request_id":"req_7","request":{"subtype":"can_use_tool","tool_name":"Bash","display_name":"Bash","input":{"command":"sudo kill 7712"},"description":"Ends one process","permission_suggestions":[{"type":"addRules","rules":[{"toolName":"Bash","ruleContent":"kill:*"}],"behavior":"allow","destination":"session"}],"tool_use_id":"toolu_9"}}`)
	if ev.Kind != KindPermission || ev.RequestID != "req_7" || ev.ToolName != "Bash" || ev.ToolUseID != "toolu_9" {
		t.Fatalf("permission = %+v", ev)
	}
	if !strings.Contains(string(ev.Input), "sudo kill") || ev.Description != "Ends one process" {
		t.Errorf("input/description = %s / %q", ev.Input, ev.Description)
	}
	if !strings.Contains(string(ev.Suggestions), "addRules") {
		t.Errorf("suggestions were not kept: %s", ev.Suggestions)
	}
	// They are for handing back, not for showing.
	if out, _ := json.Marshal(ev); strings.Contains(string(out), "addRules") {
		t.Errorf("suggestions leaked into the JSON a screen gets: %s", out)
	}
}

func TestDecodeOtherControlRequestsAreNamedSoTheyCanBeRefused(t *testing.T) {
	ev := one(t, `{"type":"control_request","request_id":"req_8","request":{"subtype":"hook_callback","callback_id":"x"}}`)
	if ev.Kind != KindControlRequest || ev.RequestID != "req_8" || ev.Subtype != "hook_callback" {
		t.Fatalf("event = %+v", ev)
	}
}

func TestDecodeControlResponseAndCancel(t *testing.T) {
	ok := one(t, `{"type":"control_response","response":{"subtype":"success","request_id":"m1","response":{}}}`)
	if ok.Kind != KindControlResponse || !ok.OK || ok.RequestID != "m1" {
		t.Fatalf("success = %+v", ok)
	}
	bad := one(t, `{"type":"control_response","response":{"subtype":"error","request_id":"m2","error":"no such model"}}`)
	if bad.OK || bad.Error != "no such model" {
		t.Fatalf("error = %+v", bad)
	}
	cancel := one(t, `{"type":"control_cancel_request","request_id":"req_7"}`)
	if cancel.Kind != KindPermissionCancelled || cancel.RequestID != "req_7" {
		t.Fatalf("cancel = %+v", cancel)
	}
}

func TestDecodeResult(t *testing.T) {
	ev := one(t, `{"type":"result","subtype":"success","is_error":false,"duration_ms":4120,"num_turns":3,"result":"done","total_cost_usd":0.1412,"session_id":"s","modelUsage":{"claude-sonnet-5":{"inputTokens":10,"contextWindow":200000}}}`)
	if ev.Kind != KindResult || ev.Cost != 0.1412 || ev.Turns != 3 || ev.DurationMs != 4120 || ev.Text != "done" || ev.ContextWindow != 200000 {
		t.Fatalf("result = %+v", ev)
	}
	errResult := one(t, `{"type":"result","subtype":"error_during_execution","is_error":true,"errors":["It went wrong"],"num_turns":1}`)
	if !errResult.IsError || errResult.Text != "It went wrong" {
		t.Fatalf("error result = %+v", errResult)
	}
}

func TestDecodeNotices(t *testing.T) {
	retry := one(t, `{"type":"system","subtype":"api_retry","attempt":2,"max_retries":3,"error_status":429}`)
	if retry.Kind != KindNotice || !strings.Contains(retry.Text, "2 of 3") {
		t.Fatalf("retry = %+v", retry)
	}
	compact := one(t, `{"type":"system","subtype":"compact_boundary","compact_metadata":{"trigger":"auto"}}`)
	if compact.Kind != KindNotice || compact.Subtype != "compact_boundary" {
		t.Fatalf("compact = %+v", compact)
	}
}

// Token deltas and the CLI's own bookkeeping are for a terminal, not a chat.
func TestDecodeSkipsWhatAScreenDoesNotDraw(t *testing.T) {
	for _, line := range []string{
		`{"type":"stream_event","event":{"type":"content_block_delta","delta":{"type":"text_delta","text":"tok"}}}`,
		`{"type":"active_goal","value":null}`,
		`{"type":"autocompact_state","value":{"enabled":true}}`,
		`{"type":"rate_limit_event","rate_limit_info":{}}`,
	} {
		evs, err := Decode([]byte(line))
		if err != nil || len(evs) != 0 {
			t.Errorf("%s -> %+v, %v", line, evs, err)
		}
	}
	if _, err := Decode([]byte("not json at all")); err == nil {
		t.Error("prose on stdout should be an error, not silence")
	}
}

func TestEncodings(t *testing.T) {
	var v map[string]any
	must := func(b []byte) map[string]any {
		t.Helper()
		if !strings.HasSuffix(string(b), "\n") || strings.Count(string(b), "\n") != 1 {
			t.Fatalf("not one line: %q", b)
		}
		v = nil
		if err := json.Unmarshal(b, &v); err != nil {
			t.Fatal(err)
		}
		return v
	}
	u := must(UserMessage("hi\nthere"))
	if u["type"] != "user" || u["message"].(map[string]any)["content"] != "hi\nthere" {
		t.Errorf("user = %v", u)
	}

	a := must(Allow("req_7", json.RawMessage(`{"command":"ls"}`), json.RawMessage(`[{"type":"addRules"}]`)))
	resp := a["response"].(map[string]any)
	if a["type"] != "control_response" || resp["subtype"] != "success" || resp["request_id"] != "req_7" {
		t.Errorf("allow envelope = %v", a)
	}
	inner := resp["response"].(map[string]any)
	if inner["behavior"] != "allow" || inner["updatedInput"].(map[string]any)["command"] != "ls" || len(inner["updatedPermissions"].([]any)) != 1 {
		t.Errorf("allow body = %v", inner)
	}

	d := must(Deny("req_7", ""))
	inner = d["response"].(map[string]any)["response"].(map[string]any)
	if inner["behavior"] != "deny" || inner["message"] == "" {
		t.Errorf("deny body = %v", inner)
	}

	r := must(Refuse("req_8", "not supported"))
	if r["response"].(map[string]any)["subtype"] != "error" {
		t.Errorf("refuse = %v", r)
	}

	m := must(SetModel("m1", "sonnet"))
	req := m["request"].(map[string]any)
	if m["type"] != "control_request" || m["request_id"] != "m1" || req["subtype"] != "set_model" || req["model"] != "sonnet" {
		t.Errorf("set model = %v", m)
	}
	if req = must(SetMode("m2", ModeAcceptEdits))["request"].(map[string]any); req["subtype"] != "set_permission_mode" || req["mode"] != "acceptEdits" {
		t.Errorf("set mode = %v", req)
	}
	if req = must(Interrupt("m3"))["request"].(map[string]any); req["subtype"] != "interrupt" {
		t.Errorf("interrupt = %v", req)
	}
}

func TestCommand(t *testing.T) {
	cmd, err := Command(Options{Dir: "~/apps", Model: "sonnet", Mode: ModeBypass, Name: "Fix it", SessionID: "0000-1"})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"'~/apps'", "'--model' 'sonnet'", "'--permission-mode' 'bypassPermissions'",
		"'--dangerously-skip-permissions'", "'--permission-prompt-tool' 'stdio'",
		"'--input-format' 'stream-json'", "'--output-format' 'stream-json'",
		"'--session-id' '0000-1'", "'--name' 'Fix it'", ".local/bin",
	} {
		if !strings.Contains(cmd, want) {
			t.Errorf("command lacks %s:\n%s", want, cmd)
		}
	}
	plain, _ := Command(Options{Mode: ModeDefault, SessionID: "x"})
	if strings.Contains(plain, "dangerously-skip") && !strings.Contains(plain, "allow-dangerously") {
		t.Error("a default session must not skip permissions")
	}
	if strings.Count(plain, "'--dangerously-skip-permissions'") != 0 {
		t.Error("a default session passed the skip flag")
	}
	if _, err := Command(Options{Mode: "auto", SessionID: "x"}); err == nil {
		t.Error("a mode HostMan does not offer was accepted")
	}
	if _, err := Command(Options{Mode: ModeDefault}); err == nil {
		t.Error("a session without an id was accepted")
	}
}

func TestNewSessionIDIsAVersion4UUID(t *testing.T) {
	id := NewSessionID()
	if len(id) != 36 || id[14] != '4' || !strings.ContainsAny(string(id[19]), "89ab") {
		t.Fatalf("id = %s", id)
	}
	if NewSessionID() == id {
		t.Fatal("two ids came out the same")
	}
}
