package control

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/aaronjauregui/haunt/internal/proto"
)

func useTempRuntimeDir(t *testing.T) {
	t.Helper()
	t.Setenv("HAUNT_RUNTIME_DIR", t.TempDir())
}

func TestSendAndReceive(t *testing.T) {
	useTempRuntimeDir(t)
	got := make(chan proto.Event, 4)
	s, err := Listen("spook", func(e proto.Event) error {
		got <- e
		return nil
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	if err := Send("spook", proto.Event{Type: proto.EventMessage, Text: "hello"}); err != nil {
		t.Fatal(err)
	}
	e := <-got
	if e.Type != proto.EventMessage || e.Text != "hello" {
		t.Errorf("received %+v", e)
	}
}

func TestRejectsDisallowedTypes(t *testing.T) {
	useTempRuntimeDir(t)
	s, err := Listen("spook", func(e proto.Event) error {
		t.Errorf("disallowed event reached publish: %+v", e)
		return nil
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	if err := Send("spook", proto.Event{Type: proto.EventBoot}); err == nil {
		t.Fatal("expected rejection of boot event")
	}
}

func TestSecondInstanceRefused(t *testing.T) {
	useTempRuntimeDir(t)
	s, err := Listen("spook", func(proto.Event) error { return nil }, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	if _, err := Listen("spook", func(proto.Event) error { return nil }, nil); err == nil {
		t.Fatal("expected second Listen to fail while first is live")
	}
}

func TestStaleSocketReplaced(t *testing.T) {
	useTempRuntimeDir(t)
	// Simulate a crashed session: socket file exists, nothing listening.
	path, err := SocketPath("spook")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}

	s, err := Listen("spook", func(proto.Event) error { return nil }, nil)
	if err != nil {
		t.Fatalf("stale socket not replaced: %v", err)
	}
	defer s.Close()
	if err := Send("spook", proto.Event{Type: proto.EventMessage, Text: "x"}); err != nil {
		t.Fatal(err)
	}
}

func TestSendToMissingGhost(t *testing.T) {
	useTempRuntimeDir(t)
	if err := Send("nobody", proto.Event{Type: proto.EventMessage, Text: "x"}); err == nil {
		t.Fatal("expected error sending to missing ghost")
	}
	// SocketPath must not have created the socket itself.
	path, _ := SocketPath("nobody")
	if _, err := os.Stat(filepath.Join(filepath.Dir(path))); err != nil {
		t.Fatal("runtime dir should exist")
	}
}
