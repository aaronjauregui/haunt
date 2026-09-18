// Package testenv provides a headless Wayland compositor (sway with the
// wlroots headless backend) for integration tests, so nothing is ever drawn
// on the developer's real session.
package testenv

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"testing"
	"time"
)

var displayRe = regexp.MustCompile(`Running compositor on wayland display '([^']+)'`)

// The harness screen. Exported through ScreenSize so tests can assert
// against it — a surface taller than the display is a bug with a number.
const (
	screenW = 1280
	screenH = 720
)

// ScreenSize is the headless output's size in pixels.
func ScreenSize() (w, h int) { return screenW, screenH }

// AddOutput plugs in another headless screen, so multi-monitor behaviour
// can be tested rather than assumed. Returns its connector name.
func (c *Compositor) AddOutput() (string, error) {
	before, err := c.outputNames()
	if err != nil {
		return "", err
	}
	if _, err := c.Swaymsg("create_output"); err != nil {
		return "", fmt.Errorf("create_output: %w", err)
	}
	for i := 0; i < 50; i++ {
		now, err := c.outputNames()
		if err != nil {
			return "", err
		}
		for n := range now {
			if !before[n] {
				// Give it a predictable size, like the first one.
				if _, err := c.Swaymsg("output", n, "resolution",
					fmt.Sprintf("%dx%d", screenW, screenH)); err != nil {
					return "", err
				}
				return n, nil
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	return "", fmt.Errorf("the new output never appeared")
}

// outputNames is the set of connectors sway currently has.
func (c *Compositor) outputNames() (map[string]bool, error) {
	out, err := c.Swaymsg("-t", "get_outputs")
	if err != nil {
		return nil, err
	}
	var outputs []struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(out, &outputs); err != nil {
		return nil, err
	}
	set := map[string]bool{}
	for _, o := range outputs {
		set[o.Name] = true
	}
	return set, nil
}

// Compositor is a running headless sway instance.
type Compositor struct {
	// Display is the WAYLAND_DISPLAY value clients should use.
	Display string
	// Sock is the sway IPC socket path (SWAYSOCK), for swaymsg.
	Sock string

	cmd     *exec.Cmd
	dir     string
	logFile string
}

// Start launches sway with the headless backend on a fresh Wayland display.
// It never touches the outer session: the headless backend creates a virtual
// output only, and all sockets live under the default XDG_RUNTIME_DIR with
// compositor-chosen fresh names.
func Start() (*Compositor, error) {
	if _, err := exec.LookPath("sway"); err != nil {
		return nil, fmt.Errorf("sway not found: %w", err)
	}

	dir, err := os.MkdirTemp("", "haunt-testenv-*")
	if err != nil {
		return nil, err
	}

	cfg := filepath.Join(dir, "sway.cfg")
	sock := filepath.Join(dir, "sway.sock")
	// Minimal config: no bars, no autostart, a predictable output size.
	conf := fmt.Sprintf("output HEADLESS-1 resolution %dx%d\n", screenW, screenH) +
		"seat seat0 xcursor_theme default 24\n"
	if err := os.WriteFile(cfg, []byte(conf), 0o644); err != nil {
		os.RemoveAll(dir)
		return nil, err
	}

	cmd := exec.Command("sway", "-V", "-c", cfg)
	cmd.Env = append(sanitizedEnv(),
		"WLR_BACKENDS=headless",
		"WLR_RENDERER=pixman",
		"WLR_LIBINPUT_NO_DEVICES=1",
		"SWAYSOCK="+sock,
	)
	// Own process group so Stop can kill sway and any children it spawned.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	logPath := filepath.Join(dir, "sway.log")
	logf, err := os.Create(logPath)
	if err != nil {
		os.RemoveAll(dir)
		return nil, err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		logf.Close()
		os.RemoveAll(dir)
		return nil, err
	}
	cmd.Stdout = logf

	if err := cmd.Start(); err != nil {
		logf.Close()
		os.RemoveAll(dir)
		return nil, fmt.Errorf("starting sway: %w", err)
	}

	c := &Compositor{Sock: sock, cmd: cmd, dir: dir, logFile: logPath}

	// Scan sway's stderr for the display name, teeing it to the log file.
	displayCh := make(chan string, 1)
	go func() {
		sc := bufio.NewScanner(stderr)
		for sc.Scan() {
			line := sc.Text()
			fmt.Fprintln(logf, line)
			if m := displayRe.FindStringSubmatch(line); m != nil {
				select {
				case displayCh <- m[1]:
				default:
				}
			}
		}
		logf.Close()
	}()

	select {
	case d := <-displayCh:
		c.Display = d
	case <-time.After(10 * time.Second):
		c.Stop()
		return nil, fmt.Errorf("sway did not report a wayland display within 10s; log: %s", logPath)
	}

	// Wait for the IPC socket so callers can use swaymsg immediately.
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat(sock); err == nil {
			break
		}
		if time.Now().After(deadline) {
			c.Stop()
			return nil, fmt.Errorf("sway IPC socket never appeared; log: %s", logPath)
		}
		time.Sleep(50 * time.Millisecond)
	}

	return c, nil
}

// sanitizedEnv strips variables that could leak the outer session into the
// nested compositor or its clients.
func sanitizedEnv() []string {
	var env []string
	for _, kv := range os.Environ() {
		k, _, _ := strings.Cut(kv, "=")
		switch k {
		case "WAYLAND_DISPLAY", "DISPLAY", "SWAYSOCK", "WAYLAND_SOCKET":
			continue
		}
		env = append(env, kv)
	}
	return env
}

// Env returns the environment a client process should run with to connect to
// the headless compositor.
func (c *Compositor) Env() []string {
	return append(sanitizedEnv(),
		"WAYLAND_DISPLAY="+c.Display,
		"SWAYSOCK="+c.Sock,
	)
}

// Screenshot captures the headless output to a PNG file using grim.
func (c *Compositor) Screenshot(path string) error {
	cmd := exec.Command("grim", path)
	cmd.Env = c.Env()
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("grim: %w: %s", err, out)
	}
	return nil
}

// Swaymsg runs a swaymsg command against the nested compositor.
func (c *Compositor) Swaymsg(args ...string) ([]byte, error) {
	cmd := exec.Command("swaymsg", append([]string{"-s", c.Sock}, args...)...)
	cmd.Env = c.Env()
	return cmd.Output()
}

// Stop terminates the compositor and removes its temp directory.
func (c *Compositor) Stop() {
	if c.cmd != nil && c.cmd.Process != nil {
		syscall.Kill(-c.cmd.Process.Pid, syscall.SIGTERM)
		done := make(chan struct{})
		go func() { c.cmd.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			syscall.Kill(-c.cmd.Process.Pid, syscall.SIGKILL)
			<-done
		}
	}
	os.RemoveAll(c.dir)
}

// StartT starts a compositor for a test, skipping if sway is unavailable and
// registering cleanup.
func StartT(t *testing.T) *Compositor {
	t.Helper()
	if _, err := exec.LookPath("sway"); err != nil {
		t.Skip("sway not installed; skipping compositor integration test")
	}
	c, err := Start()
	if err != nil {
		t.Fatalf("starting headless compositor: %v", err)
	}
	t.Cleanup(c.Stop)
	return c
}
