// Package desktop answers questions about the session the ghost lives in.
// Today that is one question: is something fullscreen right now?
//
// It matters twice over. A companion that chatters over a film is a
// nuisance, and one that starts a diffusion run mid-firefight is worse than
// a nuisance — on a single-GPU desktop that is a visible stutter in
// somebody's game.
//
// There is no portable Wayland protocol for this that we can use: a client
// cannot see other clients' surfaces, wlr-foreign-toplevel-management would
// answer it but is not in the vendored tree, and the desktop portals do not
// expose it either. So this asks the compositor over its own IPC — sway and
// Hyprland both have one, both are trivial to speak, and both are detected
// from the environment. Anywhere else the answer is "not fullscreen", which
// is the right way to be wrong: a ghost that stays quiet on an unrecognised
// compositor would be a ghost that never speaks.
package desktop

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// pollInterval is how long an answer is trusted. The question is asked
// before the ghost does something, not on a loop, so this only bounds how
// stale an answer can be — long enough to cost nothing, short enough that
// alt-tabbing out of a game is noticed promptly.
const pollInterval = 2 * time.Second

// Watcher answers whether the focused window is fullscreen.
type Watcher struct {
	ask func() (bool, error)
	// what names the mechanism, for logs.
	what string
	log  func(string)

	mu       sync.Mutex
	last     bool
	lastAt   time.Time
	failures int
}

// New picks a mechanism from the environment. log may be nil.
func New(log func(string)) *Watcher {
	if log == nil {
		log = func(string) {}
	}
	w := &Watcher{log: log}
	switch {
	case os.Getenv("SWAYSOCK") != "":
		w.ask, w.what = swayFullscreen, "sway"
	case os.Getenv("HYPRLAND_INSTANCE_SIGNATURE") != "":
		w.ask, w.what = hyprFullscreen, "hyprland"
	default:
		w.what = "none"
		log("haunt: no compositor IPC found; fullscreen awareness is off")
	}
	return w
}

// Mechanism names how the question is being answered ("sway", "hyprland",
// "none").
func (w *Watcher) Mechanism() string { return w.what }

// Fullscreen reports whether the focused window is fullscreen, cached for a
// couple of seconds. Errors answer false: the failure mode of this check is
// a ghost that speaks when it should not, which is better than one that
// falls silent because a socket moved.
func (w *Watcher) Fullscreen() bool {
	if w.ask == nil {
		return false
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if time.Since(w.lastAt) < pollInterval {
		return w.last
	}
	full, err := w.ask()
	w.lastAt = time.Now()
	if err != nil {
		// Say so once, then stop: a compositor that has gone away should
		// not fill the journal.
		w.failures++
		if w.failures == 1 {
			w.log(fmt.Sprintf("haunt: cannot ask %s about fullscreen windows: %v", w.what, err))
		}
		w.last = false
		return false
	}
	w.failures = 0
	w.last = full
	return full
}

// --- sway ---

// swayFullscreen asks sway's IPC for the tree and looks for a focused node
// that is fullscreen. The protocol is a magic string, a little-endian
// length and type, then JSON.
func swayFullscreen() (bool, error) {
	sock := os.Getenv("SWAYSOCK")
	if sock == "" {
		return false, fmt.Errorf("SWAYSOCK is unset")
	}
	conn, err := net.DialTimeout("unix", sock, 2*time.Second)
	if err != nil {
		return false, err
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(2 * time.Second))

	const getTree = 4
	var req []byte
	req = append(req, []byte("i3-ipc")...)
	req = binary.LittleEndian.AppendUint32(req, 0)
	req = binary.LittleEndian.AppendUint32(req, getTree)
	if _, err := conn.Write(req); err != nil {
		return false, err
	}

	head := make([]byte, 14)
	if _, err := readFull(conn, head); err != nil {
		return false, err
	}
	if string(head[:6]) != "i3-ipc" {
		return false, fmt.Errorf("unexpected reply from sway")
	}
	n := binary.LittleEndian.Uint32(head[6:10])
	body := make([]byte, n)
	if _, err := readFull(conn, body); err != nil {
		return false, err
	}

	var tree swayNode
	if err := json.Unmarshal(body, &tree); err != nil {
		return false, err
	}
	return tree.fullscreenFocused(), nil
}

// swayNode is the part of sway's tree this cares about.
type swayNode struct {
	Type           string     `json:"type"`
	Focused        bool       `json:"focused"`
	FullscreenMode int        `json:"fullscreen_mode"`
	Nodes          []swayNode `json:"nodes"`
	Floating       []swayNode `json:"floating_nodes"`
}

// fullscreenFocused reports whether the focused *window* is fullscreen.
//
// The node type matters: sway reports fullscreen_mode 1 on every workspace,
// the scratchpad included, since a workspace does fill its output. Only
// containers hold windows, so only containers answer this question — an
// earlier version asked the whole tree and concluded that an empty desktop
// was permanently fullscreen.
//
// Fullscreen but unfocused does not count either: a game left running on
// the other monitor is not what the ghost would be interrupting.
func (n swayNode) fullscreenFocused() bool {
	if n.Focused && n.FullscreenMode > 0 && (n.Type == "con" || n.Type == "floating_con") {
		return true
	}
	for _, c := range n.Nodes {
		if c.fullscreenFocused() {
			return true
		}
	}
	for _, c := range n.Floating {
		if c.fullscreenFocused() {
			return true
		}
	}
	return false
}

// --- hyprland ---

// hyprFullscreen asks hyprctl's socket about the active window.
func hyprFullscreen() (bool, error) {
	sig := os.Getenv("HYPRLAND_INSTANCE_SIGNATURE")
	if sig == "" {
		return false, fmt.Errorf("HYPRLAND_INSTANCE_SIGNATURE is unset")
	}
	runtime := os.Getenv("XDG_RUNTIME_DIR")
	if runtime == "" {
		return false, fmt.Errorf("XDG_RUNTIME_DIR is unset")
	}
	// Hyprland moved the socket in 0.40; try both.
	paths := []string{
		filepath.Join(runtime, "hypr", sig, ".socket.sock"),
		filepath.Join("/tmp/hypr", sig, ".socket.sock"),
	}
	var conn net.Conn
	var err error
	for _, p := range paths {
		if conn, err = net.DialTimeout("unix", p, 2*time.Second); err == nil {
			break
		}
	}
	if conn == nil {
		return false, err
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(2 * time.Second))

	if _, err := conn.Write([]byte("j/activewindow")); err != nil {
		return false, err
	}
	body, err := readAll(conn)
	if err != nil {
		return false, err
	}
	var win struct {
		// Hyprland reports an int mode in recent versions and a bool in
		// older ones, so accept whatever arrives.
		Fullscreen json.RawMessage `json:"fullscreen"`
	}
	if err := json.Unmarshal(body, &win); err != nil {
		return false, err
	}
	switch string(win.Fullscreen) {
	case "", "0", "false", "null":
		return false, nil
	}
	return true, nil
}

func readFull(c net.Conn, buf []byte) (int, error) {
	got := 0
	for got < len(buf) {
		n, err := c.Read(buf[got:])
		if err != nil {
			return got, err
		}
		got += n
	}
	return got, nil
}

func readAll(c net.Conn) ([]byte, error) {
	var out []byte
	buf := make([]byte, 4096)
	for {
		n, err := c.Read(buf)
		out = append(out, buf[:n]...)
		if err != nil || n < len(buf) {
			return out, nil
		}
	}
}
