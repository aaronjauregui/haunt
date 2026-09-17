package control

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/aaronjauregui/haunt/internal/proto"
)

// fakeTools echoes its arguments, and for "block" waits for ctx and
// reports that it saw the cancellation.
type fakeTools struct {
	started   chan struct{}
	cancelled chan struct{}
}

func newFakeTools() *fakeTools {
	return &fakeTools{started: make(chan struct{}, 1), cancelled: make(chan struct{}, 1)}
}

func (f *fakeTools) List() []ToolSpec {
	return []ToolSpec{
		{Name: "echo", Description: "says it back", Schema: json.RawMessage(`{"type":"object"}`)},
		{Name: "block", Description: "waits", Schema: json.RawMessage(`{"type":"object"}`)},
	}
}

func (f *fakeTools) Call(ctx context.Context, name string, args json.RawMessage) ToolResult {
	switch name {
	case "echo":
		return ToolResult{Text: string(args)}
	case "block":
		f.started <- struct{}{}
		<-ctx.Done()
		f.cancelled <- struct{}{}
		return ToolResult{Text: "cancelled", IsError: true}
	}
	return ToolResult{Text: "no such tool: " + name, IsError: true}
}

func listenTools(t *testing.T, tools Tools) *Server {
	t.Helper()
	useTempRuntimeDir(t)
	s, err := Listen("spook", func(proto.Event) error { return nil }, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	if tools != nil {
		s.SetTools(tools)
	}
	return s
}

func TestToolListAndCall(t *testing.T) {
	listenTools(t, newFakeTools())

	list, err := ListTools("spook")
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 || list[0].Name != "echo" || string(list[0].Schema) != `{"type":"object"}` {
		t.Errorf("list = %+v", list)
	}

	res, err := CallTool(context.Background(), "spook", "echo", json.RawMessage(`{"a":1}`))
	if err != nil {
		t.Fatal(err)
	}
	if res.Text != `{"a":1}` || res.IsError {
		t.Errorf("call = %+v", res)
	}

	// No arguments arrive as an empty object.
	res, err = CallTool(context.Background(), "spook", "echo", nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.Text != `{}` {
		t.Errorf("argument-less call got %q", res.Text)
	}

	res, err = CallTool(context.Background(), "spook", "nope", nil)
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsError {
		t.Errorf("tool failure not marked: %+v", res)
	}
}

func TestToolWireFormat(t *testing.T) {
	listenTools(t, newFakeTools())
	path, _ := SocketPath("spook")

	ask := func(line string) string {
		conn, err := net.Dial("unix", path)
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close()
		conn.Write([]byte(line + "\n"))
		r := bufio.NewReader(conn)
		reply, err := r.ReadString('\n')
		if err != nil {
			t.Fatal(err)
		}
		// Single-request connection: the server hangs up after answering.
		conn.SetReadDeadline(time.Now().Add(2 * time.Second))
		if _, err := r.ReadByte(); err == nil {
			t.Errorf("connection still open after answering %s", line)
		}
		return strings.TrimSpace(reply)
	}

	got := ask(`{"type":"tool_call","id":"8","name":"echo","args":{"x":true}}`)
	if want := `{"type":"tool_result","id":"8","text":"{\"x\":true}","is_error":false}`; got != want {
		t.Errorf("tool_call reply\n got %s\nwant %s", got, want)
	}
	got = ask(`{"type":"tool_list","id":"7"}`)
	if !strings.HasPrefix(got, `{"type":"tool_list_result","id":"7","tools":[{"name":"echo"`) ||
		!strings.Contains(got, `"input_schema":{"type":"object"}`) {
		t.Errorf("tool_list reply %s", got)
	}
	got = ask(`{"type":"tool_call","id":9,"name":"echo"}`)
	if !strings.HasPrefix(got, `{"type":"error","id":"","error":"bad request json`) {
		t.Errorf("bad request reply %s", got)
	}
}

func TestToolsNotSet(t *testing.T) {
	s := listenTools(t, nil)
	if _, err := ListTools("spook"); err == nil || !strings.Contains(err.Error(), "no tools") {
		t.Errorf("ListTools without tools: %v", err)
	}
	if _, err := CallTool(context.Background(), "spook", "echo", nil); err == nil {
		t.Error("CallTool without tools succeeded")
	}
	// Installed later, while serving, it is picked up.
	s.SetTools(newFakeTools())
	if _, err := ListTools("spook"); err != nil {
		t.Errorf("after SetTools: %v", err)
	}
	s.SetTools(nil)
	if _, err := ListTools("spook"); err == nil {
		t.Error("tools still offered after SetTools(nil)")
	}
}

func TestToolCallClientCancel(t *testing.T) {
	f := newFakeTools()
	listenTools(t, f)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := CallTool(ctx, "spook", "block", nil)
		done <- err
	}()
	select {
	case <-f.started:
	case <-time.After(5 * time.Second):
		t.Fatal("call never started")
	}
	// Longer than nothing, to show the waiting call is not dropped by a
	// deadline; the idle timeout itself is 30s and not worth waiting out.
	time.Sleep(100 * time.Millisecond)
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("client err = %v, want context.Canceled", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("client did not return on cancel")
	}
	select {
	case <-f.cancelled:
	case <-time.After(5 * time.Second):
		t.Fatal("server-side ctx not cancelled when the client hung up")
	}
}

func TestToolCallServerClose(t *testing.T) {
	f := newFakeTools()
	s := listenTools(t, f)

	done := make(chan error, 1)
	go func() {
		_, err := CallTool(context.Background(), "spook", "block", nil)
		done <- err
	}()
	<-f.started
	s.Close()
	select {
	case <-f.cancelled:
	case <-time.After(5 * time.Second):
		t.Fatal("server-side ctx not cancelled on Close")
	}
	select {
	case err := <-done:
		if err == nil {
			t.Error("client got a result from a call cut short")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("client left waiting after the ghost closed")
	}
}

func TestEventsAlongsideTools(t *testing.T) {
	useTempRuntimeDir(t)
	got := make(chan proto.Event, 1)
	s, err := Listen("spook", func(e proto.Event) error {
		got <- e
		return nil
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	s.SetTools(newFakeTools())

	if err := Send("spook", proto.Event{Type: proto.EventMessage, Text: "hello"}); err != nil {
		t.Fatal(err)
	}
	if e := <-got; e.Text != "hello" {
		t.Errorf("received %+v", e)
	}
	if err := Send("spook", proto.Event{Type: proto.EventBoot}); err == nil {
		t.Error("boot event accepted")
	}

	// A connection carrying several events still gets one "ok" per line.
	path, _ := SocketPath("spook")
	conn, err := net.Dial("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	conn.Write([]byte(`{"type":"message","text":"a"}` + "\n" + `not json` + "\n"))
	r := bufio.NewReader(conn)
	if l, _ := r.ReadString('\n'); l != "ok\n" {
		t.Errorf("first reply %q", l)
	}
	if l, _ := r.ReadString('\n'); !strings.HasPrefix(l, "error: bad event json") {
		t.Errorf("second reply %q", l)
	}
}

func TestNotRunning(t *testing.T) {
	useTempRuntimeDir(t)
	_, err := ListTools("nobody")
	if !IsNotRunning(err) || !strings.Contains(err.Error(), `ghost "nobody" does not appear to be running`) {
		t.Errorf("ListTools: %v", err)
	}
	_, err = CallTool(context.Background(), "nobody", "echo", nil)
	if !IsNotRunning(err) {
		t.Errorf("CallTool: %v", err)
	}
	if err := Send("nobody", proto.Event{Type: proto.EventMessage}); !IsNotRunning(err) {
		t.Errorf("Send: %v", err)
	}
}
