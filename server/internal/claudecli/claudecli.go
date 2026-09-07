// Package claudecli speaks the Claude Code command line's streaming protocol.
//
// Run with `--input-format stream-json --output-format stream-json`, the CLI
// reads one JSON object per line on stdin and writes one per line on stdout:
// the user's messages go in, and what Claude said, what it ran, what it asked
// permission for and what the turn cost come out. Permission prompts are part
// of the same stream — with `--permission-prompt-tool stdio` the CLI writes a
// control request and waits for a control response on stdin — and so are the
// model and the permission mode, which the host changes mid-session by the
// same kind of request in the other direction.
//
// This package is the wire format and nothing else: it builds the command
// line, encodes what goes in, and decodes what comes out into a small set of
// events a screen can draw. The events are deliberately flatter than the CLI's
// own messages, because the phone does not need to know about content blocks
// and message envelopes; it needs to know that Claude said something, ran
// something, or is waiting for an answer. Holding a session open, keeping its
// history, and matching answers to questions is the claude package's job.
package claudecli

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/chinmay28/deployer/server/internal/sshx"
)

// Modes are the permission modes a session can run in, in the words the CLI
// uses. Everything else the CLI accepts is deliberately not offered: "dontAsk"
// and "auto" are for scripts, and a phone is a person.
const (
	ModeDefault     = "default"
	ModeAcceptEdits = "acceptEdits"
	ModePlan        = "plan"
	ModeBypass      = "bypassPermissions"
)

// Modes lists the permission modes, in the order a screen offers them.
var Modes = []string{ModeDefault, ModeAcceptEdits, ModePlan, ModeBypass}

// ValidMode reports whether mode is one HostMan will start a session in.
func ValidMode(mode string) bool {
	for _, m := range Modes {
		if m == mode {
			return true
		}
	}
	return false
}

// Options is how a session is started.
type Options struct {
	// Dir is the working directory, as the user typed it: a `~` prefix is the
	// host's home directory, and empty means home.
	Dir string
	// Model is a CLI alias ("sonnet", "opus") or a full model id. Empty leaves
	// the host's own default alone.
	Model string
	// Mode is the permission mode; see Modes.
	Mode string
	// Name is the session's display name, which the CLI keeps with the
	// conversation so `claude --resume` on the host can find it.
	Name string
	// SessionID is the CLI's session id, a UUID chosen up front so the same
	// conversation can be resumed later. NewSessionID makes one.
	SessionID string
}

// NewSessionID returns a random version-4 UUID, which is the only id the CLI
// accepts for --session-id.
func NewSessionID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	h := hex.EncodeToString(b[:])
	return h[:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:]
}

// launch is the shell in front of the CLI. The native installer puts claude in
// ~/.local/bin, which a non-login shell's PATH does not have, and the working
// directory arrives as the user typed it, tilde included. Both are settled
// here rather than in Go so a host whose home is somewhere unusual still works.
const launch = `PATH="$HOME/.local/bin:$PATH"; export PATH
d=$1; shift
case "$d" in
  "") d=$HOME ;;
  "~") d=$HOME ;;
  "~/"*) d="$HOME/${d#\~/}" ;;
esac
cd -- "$d" || exit 97
exec claude "$@"`

// Command is the shell command that starts a session with the given options.
// Every value the user supplied is a quoted argument the script reads, never
// text the shell parses.
func Command(o Options) (string, error) {
	if o.Mode == "" {
		o.Mode = ModeDefault
	}
	if !ValidMode(o.Mode) {
		return "", fmt.Errorf("%q is not a permission mode", o.Mode)
	}
	if o.SessionID == "" {
		return "", errors.New("a session id is required")
	}
	args := []string{
		"-p",
		"--input-format", "stream-json",
		"--output-format", "stream-json",
		"--verbose",
		"--permission-prompt-tool", "stdio",
		"--permission-mode", o.Mode,
		// Lets a session switch to bypassing permissions later; starting in
		// that mode still needs the flag below.
		"--allow-dangerously-skip-permissions",
		"--session-id", o.SessionID,
	}
	if o.Mode == ModeBypass {
		args = append(args, "--dangerously-skip-permissions")
	}
	if o.Model != "" {
		args = append(args, "--model", o.Model)
	}
	if o.Name != "" {
		args = append(args, "--name", o.Name)
	}
	var b strings.Builder
	b.WriteString("sh -c ")
	b.WriteString(sshx.Quote(launch))
	b.WriteString(" deployer ")
	b.WriteString(sshx.Quote(o.Dir))
	for _, a := range args {
		b.WriteString(" ")
		b.WriteString(sshx.Quote(a))
	}
	return b.String(), nil
}

// --- what goes in ---

// UserMessage encodes one line of the user's, ready to write to stdin.
func UserMessage(text string) []byte {
	return line(map[string]any{
		"type":    "user",
		"message": map[string]any{"role": "user", "content": text},
	})
}

// Allow answers a permission request yes. input is the tool input the request
// carried, handed back unchanged, and permissions is what the CLI suggested
// for "always allow this", handed back only when that was the answer.
func Allow(requestID string, input json.RawMessage, permissions json.RawMessage) []byte {
	resp := map[string]any{"behavior": "allow"}
	if len(input) > 0 {
		resp["updatedInput"] = input
	}
	if len(permissions) > 0 {
		resp["updatedPermissions"] = permissions
	}
	return controlResponse(requestID, resp)
}

// Deny answers a permission request no, with a reason Claude will read.
func Deny(requestID, reason string) []byte {
	if reason == "" {
		reason = "The user declined."
	}
	return controlResponse(requestID, map[string]any{"behavior": "deny", "message": reason})
}

// Refuse answers a control request HostMan does not handle, so the CLI does
// not wait on it forever.
func Refuse(requestID, why string) []byte {
	return line(map[string]any{
		"type": "control_response",
		"response": map[string]any{
			"subtype":    "error",
			"request_id": requestID,
			"error":      why,
		},
	})
}

func controlResponse(requestID string, resp map[string]any) []byte {
	return line(map[string]any{
		"type": "control_response",
		"response": map[string]any{
			"subtype":    "success",
			"request_id": requestID,
			"response":   resp,
		},
	})
}

// SetModel asks the CLI to switch models from the next message on.
func SetModel(requestID, model string) []byte {
	return controlRequest(requestID, map[string]any{"subtype": "set_model", "model": model})
}

// SetMode asks the CLI to change the permission mode.
func SetMode(requestID, mode string) []byte {
	return controlRequest(requestID, map[string]any{"subtype": "set_permission_mode", "mode": mode})
}

// Interrupt asks the CLI to stop what it is doing and wait for the next
// message.
func Interrupt(requestID string) []byte {
	return controlRequest(requestID, map[string]any{"subtype": "interrupt"})
}

func controlRequest(requestID string, req map[string]any) []byte {
	return line(map[string]any{
		"type":       "control_request",
		"request_id": requestID,
		"request":    req,
	})
}

func line(v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err) // every value here is built from maps and strings
	}
	return append(b, '\n')
}

// --- what comes out ---

// Kinds of Event. The first group is decoded from the CLI; the second is
// written by the session holding the conversation, so a screen reading the
// log sees one shape for everything.
const (
	// KindInit is the CLI saying hello: the model, the permission mode and the
	// directory the session actually got.
	KindInit = "init"
	// KindAssistant is something Claude said, or a tool it decided to use.
	KindAssistant = "assistant"
	// KindToolResult is what a tool gave back, matched to the use by id.
	KindToolResult = "tool_result"
	// KindPermission is Claude waiting for a yes or a no.
	KindPermission = "permission"
	// KindPermissionCancelled is the CLI withdrawing a question — the turn
	// was interrupted, or the tool was no longer wanted.
	KindPermissionCancelled = "permission_cancelled"
	// KindResult ends a turn: what it cost, how many turns it took, and
	// whether it ended in an error.
	KindResult = "result"
	// KindControlResponse answers a request the host made: a model change, a
	// mode change, an interrupt.
	KindControlResponse = "control_response"
	// KindControlRequest is a request from the CLI that is not a permission
	// prompt. HostMan refuses these; the kind exists so the session can.
	KindControlRequest = "control_request"
	// KindNotice is anything else worth a line on the screen: a retry, a
	// compaction, a warning.
	KindNotice = "notice"

	// KindUser is a message the user sent, written to the log by the session.
	KindUser = "user"
	// KindAnswered records how a permission request was answered, and by
	// which screen if several were watching.
	KindAnswered = "answered"
	// KindModel and KindMode record a change that the CLI accepted.
	KindModel = "model"
	KindMode  = "mode"
	// KindExit is the end of the process, with the reason.
	KindExit = "exit"
)

// Event is one thing that happened in a session, flattened for a screen.
// Which fields are set depends on Kind.
type Event struct {
	Kind string `json:"kind"`

	// Init, and the model and mode changes.
	Model string `json:"model,omitempty"`
	Mode  string `json:"mode,omitempty"`
	Cwd   string `json:"cwd,omitempty"`

	// Assistant: what was said and what was used, in order.
	Blocks []Block `json:"blocks,omitempty"`
	// Context is how full the conversation was when this was said, in tokens:
	// everything the model read to produce it. Zero when unknown.
	Context int `json:"context,omitempty"`

	// Tool results and permission requests name the use they belong to.
	ToolUseID string `json:"toolUseId,omitempty"`
	// Content is a tool result's output, as text.
	Content string `json:"content,omitempty"`
	IsError bool   `json:"isError,omitempty"`

	// Permission requests, and the answers to them.
	RequestID   string          `json:"requestId,omitempty"`
	ToolName    string          `json:"toolName,omitempty"`
	Input       json.RawMessage `json:"input,omitempty"`
	Description string          `json:"description,omitempty"`
	// Suggestions is what the CLI proposes to remember for "always". It is
	// handed back to the CLI, never shown, so it stays opaque.
	Suggestions json.RawMessage `json:"-"`
	// Behavior is "allow" or "deny" on an answer.
	Behavior string `json:"behavior,omitempty"`

	// Results.
	Cost       float64 `json:"cost,omitempty"`
	Turns      int     `json:"turns,omitempty"`
	DurationMs int64   `json:"durationMs,omitempty"`
	// ContextWindow is the model's limit, so Context can be read as a share.
	ContextWindow int `json:"contextWindow,omitempty"`

	// Text is a result's final answer, a notice's words, a user message, or
	// the reason a session ended.
	Text string `json:"text,omitempty"`
	// Subtype is the CLI's own word for a notice or a control request.
	Subtype string `json:"subtype,omitempty"`

	// Control responses.
	OK    bool   `json:"ok,omitempty"`
	Error string `json:"error,omitempty"`
}

// Block is one piece of an assistant message.
type Block struct {
	// Type is "text", "thinking" or "tool_use".
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
	// A tool use has an id the result will name, the tool, and its input.
	ID    string          `json:"id,omitempty"`
	Name  string          `json:"name,omitempty"`
	Input json.RawMessage `json:"input,omitempty"`
}

// message is the CLI's envelope, with only the parts Decode reads.
type message struct {
	Type      string `json:"type"`
	Subtype   string `json:"subtype"`
	SessionID string `json:"session_id"`
	// system/init
	Model          string `json:"model"`
	PermissionMode string `json:"permissionMode"`
	Cwd            string `json:"cwd"`
	// system notices
	Content string `json:"content"`
	Attempt int    `json:"attempt"`
	Retries int    `json:"max_retries"`
	// assistant and user
	Message *struct {
		Role    string          `json:"role"`
		Content json.RawMessage `json:"content"`
		Usage   *struct {
			Input         int `json:"input_tokens"`
			CacheRead     int `json:"cache_read_input_tokens"`
			CacheCreation int `json:"cache_creation_input_tokens"`
		} `json:"usage"`
	} `json:"message"`
	// control requests, both ways
	RequestID string `json:"request_id"`
	Request   *struct {
		Subtype     string          `json:"subtype"`
		ToolName    string          `json:"tool_name"`
		DisplayName string          `json:"display_name"`
		Input       json.RawMessage `json:"input"`
		Description string          `json:"description"`
		ToolUseID   string          `json:"tool_use_id"`
		Suggestions json.RawMessage `json:"permission_suggestions"`
	} `json:"request"`
	Response *struct {
		Subtype   string `json:"subtype"`
		RequestID string `json:"request_id"`
		Error     string `json:"error"`
	} `json:"response"`
	// result
	IsError    bool                       `json:"is_error"`
	Result     string                     `json:"result"`
	Cost       float64                    `json:"total_cost_usd"`
	Turns      int                        `json:"num_turns"`
	DurationMs int64                      `json:"duration_ms"`
	ModelUsage map[string]json.RawMessage `json:"modelUsage"`
	Errors     []string                   `json:"errors"`
}

type contentBlock struct {
	Type      string          `json:"type"`
	Text      string          `json:"text"`
	Thinking  string          `json:"thinking"`
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Input     json.RawMessage `json:"input"`
	ToolUseID string          `json:"tool_use_id"`
	Content   json.RawMessage `json:"content"`
	IsError   bool            `json:"is_error"`
}

// Decode turns one line from the CLI into the events it means. Most lines are
// one event, a user message carrying several tool results is several, and the
// lines that are only of interest to a terminal — token deltas, rate-limit
// bookkeeping — are none. A line that is not JSON is an error; the CLI does
// not print prose on stdout.
func Decode(data []byte) ([]Event, error) {
	var m message
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("not a protocol line: %w", err)
	}
	switch m.Type {
	case "system":
		return decodeSystem(&m), nil
	case "assistant":
		return decodeAssistant(&m), nil
	case "user":
		return decodeUser(&m), nil
	case "control_request":
		return decodeControlRequest(&m), nil
	case "control_cancel_request":
		return []Event{{Kind: KindPermissionCancelled, RequestID: m.RequestID}}, nil
	case "control_response":
		if m.Response == nil {
			return nil, nil
		}
		return []Event{{
			Kind:      KindControlResponse,
			RequestID: m.Response.RequestID,
			OK:        m.Response.Subtype == "success",
			Error:     m.Response.Error,
		}}, nil
	case "result":
		return []Event{decodeResult(&m)}, nil
	}
	return nil, nil
}

func decodeSystem(m *message) []Event {
	switch m.Subtype {
	case "init":
		return []Event{{Kind: KindInit, Model: m.Model, Mode: m.PermissionMode, Cwd: m.Cwd}}
	case "api_retry":
		text := "Retrying the API"
		if m.Retries > 0 {
			text = fmt.Sprintf("Retrying the API, attempt %d of %d", m.Attempt, m.Retries)
		}
		return []Event{{Kind: KindNotice, Subtype: m.Subtype, Text: text}}
	case "compact_boundary":
		return []Event{{Kind: KindNotice, Subtype: m.Subtype, Text: "The conversation was compacted to make room"}}
	}
	if m.Content != "" {
		return []Event{{Kind: KindNotice, Subtype: m.Subtype, Text: m.Content}}
	}
	return nil
}

func decodeAssistant(m *message) []Event {
	if m.Message == nil {
		return nil
	}
	var blocks []contentBlock
	if err := json.Unmarshal(m.Message.Content, &blocks); err != nil {
		// A string body is a plain answer.
		var text string
		if json.Unmarshal(m.Message.Content, &text) == nil && text != "" {
			blocks = []contentBlock{{Type: "text", Text: text}}
		}
	}
	ev := Event{Kind: KindAssistant}
	for _, b := range blocks {
		switch b.Type {
		case "text":
			if b.Text != "" {
				ev.Blocks = append(ev.Blocks, Block{Type: "text", Text: b.Text})
			}
		case "thinking":
			if b.Thinking != "" {
				ev.Blocks = append(ev.Blocks, Block{Type: "thinking", Text: b.Thinking})
			}
		case "tool_use":
			ev.Blocks = append(ev.Blocks, Block{Type: "tool_use", ID: b.ID, Name: b.Name, Input: b.Input})
		}
	}
	if len(ev.Blocks) == 0 {
		return nil
	}
	if u := m.Message.Usage; u != nil {
		ev.Context = u.Input + u.CacheRead + u.CacheCreation
	}
	return []Event{ev}
}

func decodeUser(m *message) []Event {
	if m.Message == nil {
		return nil
	}
	var blocks []contentBlock
	if err := json.Unmarshal(m.Message.Content, &blocks); err != nil {
		return nil
	}
	var out []Event
	for _, b := range blocks {
		if b.Type != "tool_result" {
			continue
		}
		out = append(out, Event{
			Kind:      KindToolResult,
			ToolUseID: b.ToolUseID,
			Content:   resultText(b.Content),
			IsError:   b.IsError,
		})
	}
	return out
}

// resultText flattens a tool result's content, which is a string or a list of
// text blocks, into the text a screen shows.
func resultText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var parts []contentBlock
	if json.Unmarshal(raw, &parts) != nil {
		return string(raw)
	}
	var b strings.Builder
	for _, p := range parts {
		if p.Type == "text" {
			if b.Len() > 0 {
				b.WriteString("\n")
			}
			b.WriteString(p.Text)
		}
	}
	return b.String()
}

func decodeControlRequest(m *message) []Event {
	if m.Request == nil {
		return nil
	}
	if m.Request.Subtype != "can_use_tool" {
		return []Event{{Kind: KindControlRequest, RequestID: m.RequestID, Subtype: m.Request.Subtype}}
	}
	name := m.Request.DisplayName
	if name == "" {
		name = m.Request.ToolName
	}
	return []Event{{
		Kind:        KindPermission,
		RequestID:   m.RequestID,
		ToolName:    name,
		Input:       m.Request.Input,
		Description: m.Request.Description,
		ToolUseID:   m.Request.ToolUseID,
		Suggestions: m.Request.Suggestions,
	}}
}

func decodeResult(m *message) Event {
	ev := Event{
		Kind:       KindResult,
		Cost:       m.Cost,
		Turns:      m.Turns,
		DurationMs: m.DurationMs,
		IsError:    m.IsError,
		Text:       m.Result,
	}
	if ev.IsError && ev.Text == "" && len(m.Errors) > 0 {
		ev.Text = m.Errors[0]
	}
	for _, raw := range m.ModelUsage {
		var u struct {
			ContextWindow int `json:"contextWindow"`
		}
		if json.Unmarshal(raw, &u) == nil && u.ContextWindow > ev.ContextWindow {
			ev.ContextWindow = u.ContextWindow
		}
	}
	return ev
}
