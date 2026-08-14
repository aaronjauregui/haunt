package brain

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/aaronjauregui/haunt/internal/proto"
)

// buildWhisper compiles the example brain to WASM once per test run.
var (
	whisperOnce sync.Once
	whisperWasm []byte
	whisperErr  error
)

func whisper(t *testing.T) []byte {
	t.Helper()
	whisperOnce.Do(func() {
		dir, err := os.MkdirTemp("", "haunt-brain-*")
		if err != nil {
			whisperErr = err
			return
		}
		defer os.RemoveAll(dir)
		out := filepath.Join(dir, "brain.wasm")
		cmd := exec.Command("go", "build", "-o", out, "../../examples/whisper")
		cmd.Env = append(os.Environ(), "GOOS=wasip1", "GOARCH=wasm")
		if b, err := cmd.CombinedOutput(); err != nil {
			whisperErr = &buildError{err: err, out: string(b)}
			return
		}
		whisperWasm, whisperErr = os.ReadFile(out)
	})
	if whisperErr != nil {
		t.Fatalf("building whisper brain: %v", whisperErr)
	}
	return whisperWasm
}

type buildError struct {
	err error
	out string
}

func (e *buildError) Error() string { return e.err.Error() + ": " + e.out }

// collector gathers intents with a signal channel for waiting.
type collector struct {
	mu      sync.Mutex
	intents []proto.Intent
	notify  chan struct{}
}

func newCollector() *collector {
	return &collector{notify: make(chan struct{}, 64)}
}

func (c *collector) handle(i proto.Intent) {
	c.mu.Lock()
	c.intents = append(c.intents, i)
	c.mu.Unlock()
	c.notify <- struct{}{}
}

// waitFor blocks until pred is satisfied by the intent list or times out.
func (c *collector) waitFor(t *testing.T, desc string, pred func([]proto.Intent) bool) []proto.Intent {
	t.Helper()
	timeout := time.After(10 * time.Second)
	for {
		c.mu.Lock()
		snapshot := append([]proto.Intent(nil), c.intents...)
		c.mu.Unlock()
		if pred(snapshot) {
			return snapshot
		}
		select {
		case <-c.notify:
		case <-timeout:
			t.Fatalf("timed out waiting for %s; have %+v", desc, snapshot)
		}
	}
}

func TestWhisperBrainLifecycle(t *testing.T) {
	col := newCollector()
	h, err := Start(context.Background(), whisper(t), col.handle, Options{})
	if err != nil {
		t.Fatal(err)
	}

	// Boot with prior state: visit count 4 -> brain should say visit 5.
	mustDeliver(t, h, proto.Event{Type: proto.EventBoot, KV: map[string]string{"visits": "4"}})
	intents := col.waitFor(t, "boot intents", func(in []proto.Intent) bool { return len(in) >= 3 })

	if intents[0].Type != proto.IntentKVSet || intents[0].Name != "visits" || intents[0].Value != "5" {
		t.Errorf("intent 0 = %+v, want kv_set visits=5", intents[0])
	}
	if intents[1].Type != proto.IntentSay || intents[1].Text != "Back again — visit 5." {
		t.Errorf("intent 1 = %+v, want visit-5 greeting", intents[1])
	}
	if intents[2].Type != proto.IntentSetTimer || intents[2].Name != "settle" {
		t.Errorf("intent 2 = %+v, want settle timer", intents[2])
	}

	// Click twice: distinct quips.
	mustDeliver(t, h, proto.Event{Type: proto.EventClick, X: 5, Y: 5})
	mustDeliver(t, h, proto.Event{Type: proto.EventClick, X: 5, Y: 5})
	intents = col.waitFor(t, "click intents", func(in []proto.Intent) bool { return len(in) >= 7 })
	if intents[3].Text != "Boo." || intents[5].Text != "Careful, I'm translucent." {
		t.Errorf("click quips = %q, %q", intents[3].Text, intents[5].Text)
	}

	// Timer fires -> balloon hidden.
	mustDeliver(t, h, proto.Event{Type: proto.EventTimer, Name: "settle"})
	intents = col.waitFor(t, "hide_say", func(in []proto.Intent) bool { return len(in) >= 8 })
	if intents[7].Type != proto.IntentHideSay {
		t.Errorf("intent 7 = %+v, want hide_say", intents[7])
	}

	// Clean shutdown.
	if err := h.Close(); err != nil {
		t.Errorf("brain exited uncleanly: %v", err)
	}
}

func TestFirstBootGreeting(t *testing.T) {
	col := newCollector()
	h, err := Start(context.Background(), whisper(t), col.handle, Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()

	mustDeliver(t, h, proto.Event{Type: proto.EventBoot})
	intents := col.waitFor(t, "first-boot intents", func(in []proto.Intent) bool { return len(in) >= 2 })
	if intents[1].Text != "Oh! A new haunt. Hello." {
		t.Errorf("greeting = %q", intents[1].Text)
	}
}

func mustDeliver(t *testing.T, h *Host, e proto.Event) {
	t.Helper()
	if err := h.Deliver(e); err != nil {
		t.Fatal(err)
	}
}
