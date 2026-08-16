package brain

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sync"
	"time"

	"github.com/aaronjauregui/haunt/internal/proto"
)

// BuiltinHost runs a brain that ships with the driver, in this process. It
// exists so a ghost can be art plus a persona — no compiler in the
// authoring path — without the driver growing a privileged back door into
// its own runtime.
//
// The transport is a pair of pipes rather than a WASI module's stdio, and
// that is the whole difference: events are marshalled, intents are parsed
// and validated by the same readIntents as every other brain, and the brain
// function holds nothing but an io.Reader and an io.Writer. A built-in
// brain therefore has exactly the powers a downloaded one has, which is
// what keeps the sandbox meaningful for everyone else.
type BuiltinHost struct {
	stdinW io.WriteCloser
	encMu  sync.Mutex
	enc    *json.Encoder
	done   chan struct{}
}

var _ Runner = (*BuiltinHost)(nil)

// StartBuiltin launches run on its own goroutine with a pipe for events and
// a pipe for intents. run is expected to read event JSON lines until EOF and
// write intent JSON lines — brainkit.RunIO, in practice.
func StartBuiltin(run func(events io.Reader, intents io.Writer), onIntent IntentHandler, opts Options) (*BuiltinHost, error) {
	if opts.Stderr == nil {
		opts.Stderr = os.Stderr
	}
	stdinR, stdinW := io.Pipe()
	stdoutR, stdoutW := io.Pipe()

	h := &BuiltinHost{
		stdinW: stdinW,
		enc:    json.NewEncoder(stdinW),
		done:   make(chan struct{}),
	}
	inner := &Host{
		onIntent: onIntent,
		InvalidIntent: func(line string, err error) {
			fmt.Fprintf(opts.Stderr, "haunt: dropping invalid intent %q: %v\n", line, err)
		},
	}
	intentsDone := make(chan struct{})
	go func() {
		defer close(intentsDone)
		inner.readIntents(stdoutR)
	}()
	go func() {
		defer stdoutW.Close()
		// Closing the read end makes later deliveries fail rather than
		// block: a brain that has stopped reading — because it returned, or
		// because it panicked — must not wedge the driver's delivery
		// goroutine on a pipe nobody is draining.
		defer stdinR.Close()
		defer func() {
			// A panicking built-in brain must not take the driver's whole
			// session with it: the ghost falls silent, which is what a
			// crashed WASM brain does too.
			if r := recover(); r != nil {
				fmt.Fprintf(opts.Stderr, "haunt: built-in brain panicked: %v\n", r)
			}
		}()
		run(stdinR, stdoutW)
	}()
	// Closed once the brain has returned *and* its last intents have been
	// executed, so shutting down does not drop the goodbye.
	go func() {
		defer close(h.done)
		<-intentsDone
	}()
	return h, nil
}

// Deliver sends one event to the brain.
func (h *BuiltinHost) Deliver(e proto.Event) error {
	h.encMu.Lock()
	defer h.encMu.Unlock()
	if err := h.enc.Encode(e); err != nil {
		return fmt.Errorf("delivering event to brain: %w", err)
	}
	return nil
}

// Close closes the event pipe, which ends the brain's read loop.
func (h *BuiltinHost) Close() error {
	h.stdinW.Close()
	select {
	case <-h.done:
		return nil
	case <-time.After(3 * time.Second):
		// Nothing to kill — it is a goroutine in this process — so the
		// honest thing is to say so and carry on shutting down.
		return fmt.Errorf("built-in brain did not exit")
	}
}
