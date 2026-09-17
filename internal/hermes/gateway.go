package hermes

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"syscall"
	"time"
)

// Gateway is one ghost's `hermes gateway run`, supervised.
//
// It runs for as long as the ghost does and is restarted if it dies, with
// the same shape of backoff the troupe gives a ghost (internal/troupe/
// restart.go): doubling waits, and giving up after a run of failures that
// never reached a steady life. A ghost whose brain is down still pokes,
// still reminds and still shows its menu; its turns fail with a line in the
// balloon until the gateway is back.
type Gateway struct {
	Config  Config
	Profile Profile
	// Log receives the gateway's own output. Nil discards it; hermes writes
	// its real logs under the profile's logs/ anyway.
	Log io.Writer
	// Logf reports supervision events. Nil is silent.
	Logf func(format string, a ...any)

	cancel context.CancelFunc
	done   chan struct{}
	mu     sync.Mutex
	cmd    *exec.Cmd
}

// Supervision defaults.
const (
	gatewayStartWait = 90 * time.Second
	gatewayDelay     = 2 * time.Second
	gatewayMaxDelay  = 60 * time.Second
	gatewayTries     = 6
	gatewaySteady    = 2 * time.Minute
)

// Start launches the gateway and waits until its API server answers, or
// fails. After that it keeps it running until Stop.
func (g *Gateway) Start(ctx context.Context) (*Client, error) {
	client := &Client{BaseURL: g.Profile.URL(), Key: g.Profile.Key, Ghost: g.Profile.Ghost}

	// A gateway already answering on the profile's port is one this ghost
	// left behind, or one the person started by hand; either way it is
	// serving this profile, and `--replace` below would only fight it.
	hctx, hcancel := context.WithTimeout(ctx, 2*time.Second)
	up := client.Health(hctx) == nil
	hcancel()
	if up {
		g.logf("a hermes gateway is already answering for %s; using it", g.Profile.Ghost)
		g.done = make(chan struct{})
		close(g.done)
		g.cancel = func() {}
		return client, nil
	}

	runCtx, cancel := context.WithCancel(context.Background())
	g.cancel = cancel
	g.done = make(chan struct{})
	exited, err := g.spawn(runCtx)
	if err != nil {
		cancel()
		close(g.done)
		return nil, err
	}
	if err := g.waitHealthy(ctx, client, exited); err != nil {
		cancel()
		<-exited
		close(g.done)
		return nil, err
	}
	go g.supervise(runCtx, client, exited)
	return client, nil
}

func (g *Gateway) logf(format string, a ...any) {
	if g.Logf != nil {
		g.Logf(format, a...)
	}
}

// spawn starts one gateway process. The returned channel closes when it
// exits.
func (g *Gateway) spawn(ctx context.Context) (<-chan struct{}, error) {
	argv := append(append([]string(nil), g.Config.command()...),
		"gateway", "run", "--external-supervisor", "--replace")
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Dir = g.Profile.Dir
	cmd.Env = append(os.Environ(),
		"HERMES_HOME="+g.Profile.Dir,
	)
	if g.Log != nil {
		cmd.Stdout, cmd.Stderr = g.Log, g.Log
	}
	// Its own process group, so stopping it stops whatever it spawned — the
	// MCP server among them.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true, Pdeathsig: syscall.SIGTERM}
	if err := cmd.Start(); err != nil {
		if _, lookErr := exec.LookPath(argv[0]); lookErr != nil {
			return nil, fmt.Errorf("hermes is not installed (%s not found); see docs/hermes-brain.md", argv[0])
		}
		return nil, fmt.Errorf("starting the hermes gateway: %w", err)
	}
	g.mu.Lock()
	g.cmd = cmd
	g.mu.Unlock()
	exited := make(chan struct{})
	go func() {
		cmd.Wait()
		close(exited)
	}()
	go func() {
		select {
		case <-ctx.Done():
			terminate(cmd, exited)
		case <-exited:
		}
	}()
	return exited, nil
}

// terminate asks the process group to stop, and insists after a while.
func terminate(cmd *exec.Cmd, exited <-chan struct{}) {
	pgid := -cmd.Process.Pid
	syscall.Kill(pgid, syscall.SIGTERM)
	select {
	case <-exited:
	case <-time.After(10 * time.Second):
		syscall.Kill(pgid, syscall.SIGKILL)
		<-exited
	}
}

func (g *Gateway) waitHealthy(ctx context.Context, client *Client, exited <-chan struct{}) error {
	deadline := time.NewTimer(gatewayStartWait)
	defer deadline.Stop()
	tick := time.NewTicker(250 * time.Millisecond)
	defer tick.Stop()
	for {
		hctx, cancel := context.WithTimeout(ctx, time.Second)
		err := client.Health(hctx)
		cancel()
		if err == nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-exited:
			return fmt.Errorf("the hermes gateway exited while starting; see %s",
				filepath.Join(g.Profile.Dir, "logs"))
		case <-deadline.C:
			return fmt.Errorf("the hermes gateway did not answer within %s: %v", gatewayStartWait, err)
		case <-tick.C:
		}
	}
}

func (g *Gateway) supervise(ctx context.Context, client *Client, exited <-chan struct{}) {
	defer close(g.done)
	failures := 0
	started := time.Now()
	for {
		select {
		case <-ctx.Done():
			<-exited
			return
		case <-exited:
		}
		if time.Since(started) >= gatewaySteady {
			failures = 0
		}
		failures++
		if failures > gatewayTries {
			g.logf("the hermes gateway for %s keeps dying; leaving it stopped", g.Profile.Ghost)
			return
		}
		wait := gatewayDelay << (failures - 1)
		if wait > gatewayMaxDelay {
			wait = gatewayMaxDelay
		}
		g.logf("the hermes gateway for %s stopped; restarting in %s", g.Profile.Ghost, wait)
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
		var err error
		started = time.Now()
		if exited, err = g.spawn(ctx); err != nil {
			g.logf("restarting the hermes gateway: %v", err)
			closed := make(chan struct{})
			close(closed)
			exited = closed
			continue
		}
		if err := g.waitHealthy(ctx, client, exited); err != nil {
			g.logf("%v", err)
		}
	}
}

// Stop ends the gateway and waits for it.
func (g *Gateway) Stop() error {
	if g.cancel == nil {
		return nil
	}
	g.cancel()
	select {
	case <-g.done:
		return nil
	case <-time.After(15 * time.Second):
		return fmt.Errorf("the hermes gateway did not stop")
	}
}
