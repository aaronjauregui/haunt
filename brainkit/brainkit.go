// Package brainkit is the SDK for writing Haunt brains in Go. A brain built
// on brainkit compiles to a WASI module (GOOS=wasip1 GOARCH=wasm) for
// sandboxed in-driver execution, and to a native binary for socket brains —
// the transport is stdin/stdout JSON lines either way.
package brainkit

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"sync"

	"github.com/aaronjauregui/haunt/internal/proto"
)

// Re-exported schema types so brains import only brainkit.
type (
	Event  = proto.Event
	Intent = proto.Intent
)

// Brain handles events. Implementations receive each event in stdin order.
type Brain interface {
	OnEvent(e Event, emit *Emitter)
}

// Func adapts a function to the Brain interface.
type Func func(e Event, emit *Emitter)

func (f Func) OnEvent(e Event, emit *Emitter) { f(e, emit) }

// Emitter sends intents back to the driver.
type Emitter struct {
	mu  sync.Mutex
	enc *json.Encoder
}

func (em *Emitter) emit(i Intent) {
	em.mu.Lock()
	defer em.mu.Unlock()
	if err := em.enc.Encode(i); err != nil {
		fmt.Fprintf(os.Stderr, "brainkit: emit failed: %v\n", err)
	}
}

// Say shows text in the ghost's balloon.
func (em *Emitter) Say(text string) { em.emit(Intent{Type: proto.IntentSay, Text: text}) }

// HideSay dismisses the balloon.
func (em *Emitter) HideSay() { em.emit(Intent{Type: proto.IntentHideSay}) }

// Move repositions the sprite to absolute output coordinates.
func (em *Emitter) Move(x, y int) { em.emit(Intent{Type: proto.IntentMove, X: x, Y: y}) }

// SetTimer schedules a named EventTimer delayMS milliseconds from now.
func (em *Emitter) SetTimer(name string, delayMS int) {
	em.emit(Intent{Type: proto.IntentSetTimer, Name: name, DelayMS: delayMS})
}

// CancelTimer cancels a pending named timer.
func (em *Emitter) CancelTimer(name string) {
	em.emit(Intent{Type: proto.IntentCancelTimer, Name: name})
}

// Remember persists key=value in the ghost's store; it comes back in the
// boot event's KV map next session.
func (em *Emitter) Remember(key, value string) {
	em.emit(Intent{Type: proto.IntentKVSet, Name: key, Value: value})
}

// Log writes a debug message via the driver.
func (em *Emitter) Log(text string) { em.emit(Intent{Type: proto.IntentLog, Text: text}) }

// Tell sends text to another running ghost (requires the tell capability).
// The receiving ghost sees a message event with From set to this ghost.
func (em *Emitter) Tell(ghostName, text string) {
	em.emit(Intent{Type: proto.IntentTell, Name: ghostName, Text: text})
}

// Run reads events from stdin until EOF, dispatching each to b. Call it from
// main; it returns when the driver closes the event stream.
func Run(b Brain) {
	em := &Emitter{enc: json.NewEncoder(os.Stdout)}
	sc := bufio.NewScanner(os.Stdin)
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for sc.Scan() {
		if len(sc.Bytes()) == 0 {
			continue
		}
		var e Event
		if err := json.Unmarshal(sc.Bytes(), &e); err != nil {
			fmt.Fprintf(os.Stderr, "brainkit: bad event: %v\n", err)
			continue
		}
		b.OnEvent(e, em)
	}
}
