// Package brain hosts ghost logic. A brain is a process-shaped program that
// reads Event JSON lines on stdin and writes Intent JSON lines on stdout —
// the same contract whether it runs as a sandboxed WASM (WASI) module inside
// the driver or, later, as an external process on a socket.
package brain

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sync"

	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/imports/wasi_snapshot_preview1"
	"github.com/tetratelabs/wazero/sys"

	"github.com/aaronjauregui/haunt/internal/proto"
)

// IntentHandler receives each validated intent a brain emits.
type IntentHandler func(proto.Intent)

// Host runs one WASM brain with WASI stdio as its event/intent transport.
type Host struct {
	runtime wazero.Runtime
	cancel  context.CancelFunc

	stdinW   io.WriteCloser
	encMu    sync.Mutex
	enc      *json.Encoder
	done     chan error
	onIntent IntentHandler

	// InvalidIntent, if set, is called with the raw line and error for
	// intents that fail validation or parsing. Defaults to stderr logging.
	InvalidIntent func(line string, err error)
}

// Options configures a brain instance.
type Options struct {
	// Stderr receives the brain's stderr (debug output). Defaults to the
	// driver's stderr.
	Stderr io.Writer
}

// Start compiles and launches the WASM module in wasmBytes. Events are sent
// with Deliver; each intent line the brain writes is validated and passed to
// onIntent (from a dedicated goroutine, one at a time).
func Start(ctx context.Context, wasmBytes []byte, onIntent IntentHandler, opts Options) (*Host, error) {
	if opts.Stderr == nil {
		opts.Stderr = os.Stderr
	}
	ctx, cancel := context.WithCancel(ctx)

	h := &Host{
		cancel:   cancel,
		done:     make(chan error, 1),
		onIntent: onIntent,
		InvalidIntent: func(line string, err error) {
			fmt.Fprintf(os.Stderr, "haunt: dropping invalid intent %q: %v\n", line, err)
		},
	}

	h.runtime = wazero.NewRuntime(ctx)
	wasi_snapshot_preview1.MustInstantiate(ctx, h.runtime)

	compiled, err := h.runtime.CompileModule(ctx, wasmBytes)
	if err != nil {
		cancel()
		h.runtime.Close(context.Background())
		return nil, fmt.Errorf("compiling brain module: %w", err)
	}

	stdinR, stdinW := io.Pipe()
	stdoutR, stdoutW := io.Pipe()
	h.stdinW = stdinW
	h.enc = json.NewEncoder(stdinW)

	cfg := wazero.NewModuleConfig().
		WithName("brain").
		WithStdin(stdinR).
		WithStdout(stdoutW).
		WithStderr(opts.Stderr).
		// No preopened dirs, no environment, no args: the sandbox is the
		// point. Clock access is allowed for time.Now.
		WithSysWalltime().
		WithSysNanotime()

	go h.readIntents(stdoutR)

	go func() {
		_, err := h.runtime.InstantiateModule(ctx, compiled, cfg)
		// A clean brain exit (driver closed stdin, main returned) surfaces
		// as ExitError(0); treat it as success.
		if exitErr, ok := err.(*sys.ExitError); ok && exitErr.ExitCode() == 0 {
			err = nil
		}
		stdoutW.Close()
		h.done <- err
	}()

	return h, nil
}

// Deliver sends one event to the brain. Safe for concurrent use.
func (h *Host) Deliver(e proto.Event) error {
	h.encMu.Lock()
	defer h.encMu.Unlock()
	if err := h.enc.Encode(e); err != nil {
		return fmt.Errorf("delivering event to brain: %w", err)
	}
	return nil
}

func (h *Host) readIntents(r io.Reader) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for sc.Scan() {
		line := sc.Text()
		if line == "" {
			continue
		}
		var in proto.Intent
		if err := json.Unmarshal([]byte(line), &in); err != nil {
			h.InvalidIntent(line, err)
			continue
		}
		if err := in.Validate(); err != nil {
			h.InvalidIntent(line, err)
			continue
		}
		h.onIntent(in)
	}
}

// Close shuts the brain down: stdin is closed so a well-behaved brain exits
// its read loop; the module is torn down either way. Returns the brain's
// exit error, if any.
func (h *Host) Close() error {
	h.stdinW.Close()
	err := <-h.done
	h.cancel()
	h.runtime.Close(context.Background())
	return err
}

// Wait blocks until the brain exits on its own.
func (h *Host) Wait() error {
	err := <-h.done
	h.done <- err
	return err
}
