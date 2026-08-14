package brain

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"
	"time"

	"github.com/aaronjauregui/haunt/internal/proto"
)

// Runner is the common interface over WASM and process brains: deliver
// events in, intents come out through the handler given at start.
type Runner interface {
	Deliver(e proto.Event) error
	Close() error
}

// ProcessHost runs a brain as a native subprocess speaking the same stdio
// JSONL protocol as WASM brains. No sandbox: gated by the native-brain
// capability at the manifest/consent layer.
type ProcessHost struct {
	cmd   *exec.Cmd
	stdin io.WriteCloser
	encMu sync.Mutex
	enc   *json.Encoder
	done  chan error
}

// Both hosts satisfy Runner.
var (
	_ Runner = (*Host)(nil)
	_ Runner = (*ProcessHost)(nil)
)

// StartProcess launches argv (argv[0] resolved relative to dir) with the
// ghost directory as working directory.
func StartProcess(dir string, argv []string, onIntent IntentHandler, opts Options) (*ProcessHost, error) {
	if len(argv) == 0 {
		return nil, fmt.Errorf("empty brain command")
	}
	if opts.Stderr == nil {
		opts.Stderr = os.Stderr
	}

	// Deliberately not CommandContext: shutdown must be orderly (close
	// stdin, let the brain exit) rather than a context-triggered SIGKILL.
	cmd := exec.Command("./"+argv[0], argv[1:]...)
	cmd.Dir = dir
	cmd.Stderr = opts.Stderr

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("starting brain process: %w", err)
	}

	h := &ProcessHost{
		cmd:   cmd,
		stdin: stdin,
		enc:   json.NewEncoder(stdin),
		done:  make(chan error, 1),
	}
	inner := &Host{
		onIntent: onIntent,
		InvalidIntent: func(line string, err error) {
			fmt.Fprintf(os.Stderr, "haunt: dropping invalid intent %q: %v\n", line, err)
		},
	}
	go func() {
		inner.readIntents(stdout)
		h.done <- cmd.Wait()
	}()
	return h, nil
}

// Deliver sends one event to the brain process.
func (h *ProcessHost) Deliver(e proto.Event) error {
	h.encMu.Lock()
	defer h.encMu.Unlock()
	if err := h.enc.Encode(e); err != nil {
		return fmt.Errorf("delivering event to brain: %w", err)
	}
	return nil
}

// Close closes stdin (a well-behaved brain exits) and waits, escalating to
// SIGKILL if the process lingers.
func (h *ProcessHost) Close() error {
	h.stdin.Close()
	select {
	case err := <-h.done:
		return err
	case <-time.After(3 * time.Second):
		h.cmd.Process.Kill()
		<-h.done
		return fmt.Errorf("brain process did not exit; killed")
	}
}
