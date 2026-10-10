package control

import (
	"context"
	"strings"
	"testing"

	"github.com/aaronjauregui/haunt/internal/proto"
)

// A request a ghost installed a handler for is answered with the handler's
// line; without one, the same line is read as an event and refused.
func TestMemoryReindexRequest(t *testing.T) {
	useTempRuntimeDir(t)
	s, err := Listen("spook", func(proto.Event) error { return nil }, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	reply, err := MemoryReindex("spook")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(reply, "not accepted") {
		t.Errorf("with no handler, reply = %q, want the event refusal", reply)
	}

	s.Handle(ReqMemoryReindex, func(context.Context) string { return "ok 2 topics, 5 lines" })
	if reply, err = MemoryReindex("spook"); err != nil || reply != "ok 2 topics, 5 lines" {
		t.Errorf("reply = %q, %v", reply, err)
	}
	// Events still arrive on the same socket.
	if err := Send("spook", proto.Event{Type: proto.EventMessage, Text: "hello"}); err != nil {
		t.Errorf("an event beside the handler: %v", err)
	}
	s.Handle(ReqMemoryReindex, nil)
	if reply, _ = MemoryReindex("spook"); !strings.Contains(reply, "not accepted") {
		t.Errorf("after removing the handler, reply = %q", reply)
	}
}

func TestNotRunning(t *testing.T) {
	useTempRuntimeDir(t)
	_, err := MemoryReindex("nobody")
	if !IsNotRunning(err) || !strings.Contains(err.Error(), `ghost "nobody" does not appear to be running`) {
		t.Errorf("MemoryReindex: %v", err)
	}
	if err := Send("nobody", proto.Event{Type: proto.EventMessage}); !IsNotRunning(err) {
		t.Errorf("Send: %v", err)
	}
}
