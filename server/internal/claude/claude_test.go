package claude

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/chinmay28/deployer/server/internal/claudecli"
)

// What is worth proving here is the bookkeeping a real CLI would not exercise:
// that a screen which was away gets exactly the events it missed, that a
// permission question is answered once and to the right request, that a model
// change waits for the CLI's word, and that a session nobody is watching is
// eventually let go — unless Claude is still working in it.

func testManager(t *testing.T) (*Manager, *time.Time) {
	t.Helper()
	clock := time.Date(2026, 9, 5, 12, 0, 0, 0, time.UTC)
	m := NewManager(nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	m.now = func() time.Time { return clock }
	return m, &clock
}

func adopt(t *testing.T, m *Manager, opts Options) (*Session, *Loopback) {
	t.Helper()
	s, p := m.AdoptLoopback(1, "pi", opts)
	t.Cleanup(func() { p.Close() })
	return s, p
}

func read(t *testing.T, s *Session, from int64) Chunk {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	c, err := s.Read(ctx, from)
	if err != nil {
		t.Fatalf("read from %d: %v", from, err)
	}
	return c
}

func next(t *testing.T, p *Loopback) map[string]any {
	t.Helper()
	line := p.Next(2 * time.Second)
	if line == "" {
		t.Fatal("nothing was sent to the CLI")
	}
	var v map[string]any
	if err := json.Unmarshal([]byte(line), &v); err != nil {
		t.Fatalf("the CLI got %q: %v", line, err)
	}
	return v
}

const initLine = `{"type":"system","subtype":"init","cwd":"/home/pi/apps","model":"claude-sonnet-5","permissionMode":"default"}`

func TestSendLogsTheMessageAndNamesTheSession(t *testing.T) {
	m, _ := testManager(t)
	s, p := adopt(t, m, Options{})
	p.Emit(initLine)
	read(t, s, 0)

	if err := s.Send("why is homebridge restarting?\nplease look"); err != nil {
		t.Fatal(err)
	}
	sent := next(t, p)
	if sent["type"] != "user" || sent["message"].(map[string]any)["content"] != "why is homebridge restarting?\nplease look" {
		t.Fatalf("the CLI got %v", sent)
	}

	c := read(t, s, 0)
	kinds := []string{}
	for _, e := range c.Entries {
		kinds = append(kinds, e.Kind)
	}
	if strings.Join(kinds, ",") != "init,user" {
		t.Fatalf("history = %v", kinds)
	}
	info := s.Info()
	if !info.Busy || info.Name != "why is homebridge restarting?" || info.Model != "claude-sonnet-5" || info.Dir != "/home/pi/apps" || info.Offset != 2 {
		t.Fatalf("info = %+v", info)
	}
	if err := s.Send("   "); err == nil {
		t.Error("an empty message was accepted")
	}
}

func TestReadResumesWhereAScreenLeftOff(t *testing.T) {
	m, _ := testManager(t)
	s, p := adopt(t, m, Options{})
	p.Emit(initLine)
	first := read(t, s, 0)
	if first.Next != 1 {
		t.Fatalf("next = %d after one event", first.Next)
	}

	p.Emit(`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"Looking."}]}}`)
	p.Emit(`{"type":"result","subtype":"success","num_turns":1,"total_cost_usd":0.02,"result":"Looking.","modelUsage":{"m":{"contextWindow":200000}}}`)

	c := read(t, s, first.Next)
	if c.From != 1 || len(c.Entries) != 2 || c.Entries[0].Kind != "assistant" || c.Entries[1].Kind != "result" || c.Entries[1].Seq != 2 {
		t.Fatalf("chunk = %+v", c)
	}
	info := s.Info()
	if info.Busy || info.Cost != 0.02 || info.Turns != 1 || info.ContextWindow != 200000 {
		t.Fatalf("info after result = %+v", info)
	}

	// A reader waiting for more wakes when it arrives, without polling.
	got := make(chan Chunk, 1)
	go func() { got <- read(t, s, c.Next) }()
	time.Sleep(20 * time.Millisecond)
	p.Emit(`{"type":"system","subtype":"api_retry","attempt":1,"max_retries":3}`)
	select {
	case c := <-got:
		if len(c.Entries) != 1 || c.Entries[0].Kind != "notice" {
			t.Fatalf("woke with %+v", c)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the reader did not wake")
	}
}

func TestPermissionIsAnsweredOnceAndToTheRightRequest(t *testing.T) {
	m, _ := testManager(t)
	s, p := adopt(t, m, Options{})
	p.Emit(initLine)
	p.Emit(`{"type":"control_request","request_id":"req_7","request":{"subtype":"can_use_tool","tool_name":"Bash","input":{"command":"sudo kill 7712"},"permission_suggestions":[{"type":"addRules"}],"tool_use_id":"toolu_9"}}`)
	c := read(t, s, 1)
	if len(c.Entries) != 1 || c.Entries[0].Kind != "permission" || c.Entries[0].RequestID != "req_7" {
		t.Fatalf("history = %+v", c.Entries)
	}
	if s.Info().Pending != 1 {
		t.Fatal("the request is not counted as pending")
	}

	if err := s.Answer("req_9", true, false, ""); !errors.Is(err, ErrNoRequest) {
		t.Fatalf("answering an unknown request: %v", err)
	}
	if err := s.Answer("req_7", true, true, ""); err != nil {
		t.Fatal(err)
	}
	sent := next(t, p)
	resp := sent["response"].(map[string]any)
	if sent["type"] != "control_response" || resp["request_id"] != "req_7" {
		t.Fatalf("the CLI got %v", sent)
	}
	body := resp["response"].(map[string]any)
	if body["behavior"] != "allow" || body["updatedInput"].(map[string]any)["command"] != "sudo kill 7712" || body["updatedPermissions"] == nil {
		t.Fatalf("answer body = %v", body)
	}

	// The second phone, a moment later, is told it was settled.
	if err := s.Answer("req_7", false, false, "no"); !errors.Is(err, ErrNoRequest) {
		t.Fatalf("answering twice: %v", err)
	}
	c = read(t, s, 2)
	if len(c.Entries) != 1 || c.Entries[0].Kind != "answered" || c.Entries[0].Behavior != "allow" || c.Entries[0].ToolUseID != "toolu_9" {
		t.Fatalf("answered entry = %+v", c.Entries)
	}
	if s.Info().Pending != 0 {
		t.Fatal("the request is still pending after being answered")
	}
}

func TestDenyCarriesTheReasonAndACancelledRequestGoesQuiet(t *testing.T) {
	m, _ := testManager(t)
	s, p := adopt(t, m, Options{})
	p.Emit(`{"type":"control_request","request_id":"a","request":{"subtype":"can_use_tool","tool_name":"Bash","input":{}}}`)
	p.Emit(`{"type":"control_request","request_id":"b","request":{"subtype":"can_use_tool","tool_name":"Edit","input":{}}}`)
	read(t, s, 0)
	for s.Info().Pending < 2 {
		time.Sleep(5 * time.Millisecond)
	}
	if err := s.Answer("a", false, false, "not that file"); err != nil {
		t.Fatal(err)
	}
	body := next(t, p)["response"].(map[string]any)["response"].(map[string]any)
	if body["behavior"] != "deny" || body["message"] != "not that file" {
		t.Fatalf("deny body = %v", body)
	}

	p.Emit(`{"type":"control_cancel_request","request_id":"b"}`)
	for s.Info().Pending != 0 {
		time.Sleep(5 * time.Millisecond)
	}
	if err := s.Answer("b", true, false, ""); !errors.Is(err, ErrNoRequest) {
		t.Fatalf("answering a withdrawn request: %v", err)
	}
}

func TestOtherControlRequestsAreRefusedSoTheCLIDoesNotHang(t *testing.T) {
	m, _ := testManager(t)
	_, p := adopt(t, m, Options{})
	p.Emit(`{"type":"control_request","request_id":"h1","request":{"subtype":"hook_callback"}}`)
	sent := next(t, p)
	resp := sent["response"].(map[string]any)
	if resp["subtype"] != "error" || resp["request_id"] != "h1" {
		t.Fatalf("the CLI got %v", sent)
	}
}

func TestSetModelWaitsForTheCLI(t *testing.T) {
	m, _ := testManager(t)
	s, p := adopt(t, m, Options{Model: "sonnet"})

	done := make(chan error, 1)
	go func() { done <- s.SetModel(context.Background(), "opus") }()
	sent := next(t, p)
	req := sent["request"].(map[string]any)
	if sent["type"] != "control_request" || req["subtype"] != "set_model" || req["model"] != "opus" {
		t.Fatalf("the CLI got %v", sent)
	}
	id := sent["request_id"].(string)
	p.Emit(`{"type":"control_response","response":{"subtype":"success","request_id":"` + id + `","response":{}}}`)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if s.Info().Model != "opus" {
		t.Fatal("the model did not change")
	}
	c := read(t, s, 0)
	if last := c.Entries[len(c.Entries)-1]; last.Kind != "model" || last.Model != "opus" {
		t.Fatalf("last entry = %+v", last)
	}

	// A refusal is an error here, with the CLI's own words.
	go func() { done <- s.SetMode(context.Background(), claudecli.ModePlan) }()
	sent = next(t, p)
	id = sent["request_id"].(string)
	p.Emit(`{"type":"control_response","response":{"subtype":"error","request_id":"` + id + `","error":"nope"}}`)
	if err := <-done; err == nil || !strings.Contains(err.Error(), "nope") {
		t.Fatalf("refused change: %v", err)
	}
	if s.Info().Mode != "default" {
		t.Fatal("a refused mode change took effect")
	}
	if err := s.SetMode(context.Background(), "auto"); err == nil {
		t.Fatal("a mode HostMan does not offer was accepted")
	}
}

func TestInterruptAsksTheCLI(t *testing.T) {
	m, _ := testManager(t)
	s, p := adopt(t, m, Options{})
	go func() {
		sent := next(t, p)
		p.Emit(`{"type":"control_response","response":{"subtype":"success","request_id":"` + sent["request_id"].(string) + `"}}`)
	}()
	if err := s.Interrupt(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestExitEndsTheHistoryWithAReason(t *testing.T) {
	m, _ := testManager(t)
	s, p := adopt(t, m, Options{})
	p.Emit(initLine)
	read(t, s, 0)
	p.Exit(1, "Error: Not logged in\nRun claude auth login")

	c := read(t, s, 1)
	if !c.Done || len(c.Entries) != 1 || c.Entries[0].Kind != "exit" {
		t.Fatalf("chunk = %+v", c)
	}
	if !strings.Contains(c.Exit, "status 1") || !strings.Contains(c.Exit, "Run claude auth login") {
		t.Fatalf("exit = %q", c.Exit)
	}
	if err := s.Send("hello?"); !errors.Is(err, ErrClosed) {
		t.Fatalf("sending after exit: %v", err)
	}
	if err := s.SetModel(context.Background(), "opus"); !errors.Is(err, ErrClosed) {
		t.Fatalf("changing the model after exit: %v", err)
	}
	if got := exitReason(97, ""); !strings.Contains(got, "directory") {
		t.Errorf("exit 97 = %q", got)
	}
	if got := exitReason(127, ""); !strings.Contains(got, "not installed") {
		t.Errorf("exit 127 = %q", got)
	}
}

func TestClosingFromHostManRecordsWhyNotHow(t *testing.T) {
	m, _ := testManager(t)
	s, _ := adopt(t, m, Options{})
	if err := m.Close(s.ID()); err != nil {
		t.Fatal(err)
	}
	c := read(t, s, 0)
	if !c.Done || c.Exit != "ended from HostMan" {
		t.Fatalf("chunk = %+v", c)
	}
	if _, err := m.Get(s.ID()); !errors.Is(err, ErrNotFound) {
		t.Fatal("a closed session is still listed")
	}
}

func TestReapingLeavesWorkingAndWatchedSessionsAlone(t *testing.T) {
	m, clock := testManager(t)
	idle, _ := adopt(t, m, Options{})
	watched, _ := adopt(t, m, Options{})
	working, wp := adopt(t, m, Options{})
	detach := watched.Attach()
	defer detach()
	if err := working.Send("do the thing"); err != nil {
		t.Fatal(err)
	}
	wp.Next(time.Second)

	*clock = clock.Add(Idle + time.Minute)
	m.reap()

	if _, err := m.Get(idle.ID()); !errors.Is(err, ErrNotFound) {
		t.Error("the idle session was kept")
	}
	if _, err := m.Get(watched.ID()); err != nil {
		t.Error("the watched session was reaped")
	}
	if _, err := m.Get(working.ID()); err != nil {
		t.Error("the working session was reaped")
	}

	// Once the turn ends, the working one is idle like any other.
	wp.Emit(`{"type":"result","subtype":"success","num_turns":1}`)
	for working.Info().Busy {
		time.Sleep(5 * time.Millisecond)
	}
	*clock = clock.Add(Idle + time.Minute)
	m.reap()
	if _, err := m.Get(working.ID()); !errors.Is(err, ErrNotFound) {
		t.Error("the finished session was kept")
	}
}

func TestPerHostLimitCountsRunningSessionsOnly(t *testing.T) {
	m, _ := testManager(t)
	var procs []*Loopback
	for i := 0; i < PerHost; i++ {
		_, p := adopt(t, m, Options{})
		procs = append(procs, p)
	}
	open := 0
	for _, s := range m.ForHost(1) {
		if s.Running() {
			open++
		}
	}
	if open != PerHost {
		t.Fatalf("open = %d", open)
	}
	procs[0].Exit(0, "")
	for m.ForHost(1)[0].Running() {
		time.Sleep(5 * time.Millisecond)
	}
	if len(m.ForHost(1)) != PerHost {
		t.Fatal("an ended session vanished from the list before being reaped")
	}
}

func TestHistoryIsBoundedAndSaysWhatWasLost(t *testing.T) {
	m, _ := testManager(t)
	s, p := adopt(t, m, Options{})
	big := strings.Repeat("x", 100<<10)
	for i := 0; i < 30; i++ {
		p.Emit(`{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"t","content":"` + big + `"}]}}`)
	}
	for s.Info().Offset < 30 {
		time.Sleep(5 * time.Millisecond)
	}
	c := read(t, s, 0)
	if c.From == 0 {
		t.Fatal("nothing was dropped from a 3 MB history")
	}
	if c.Next != 30 || c.From+int64(len(c.Entries)) != 30 {
		t.Fatalf("chunk from %d with %d entries, next %d", c.From, len(c.Entries), c.Next)
	}
	s.mu.Lock()
	size := s.size
	s.mu.Unlock()
	if size > MaxLog {
		t.Fatalf("size = %d, over the limit", size)
	}
}
