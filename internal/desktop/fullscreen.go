// Package desktop answers questions about the session the ghost lives in:
// is something fullscreen, and what is the person actually looking at?
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

// Window is the focused window, as the compositor describes it.
type Window struct {
	// AppID is the application ("firefox", "foot"), from app_id on Wayland
	// or the X11 class through XWayland.
	AppID string
	// Title is the window title, which carries more than it looks like it
	// does: file names, branch names, error text, what is playing.
	Title string
	// X, Y, W, H is where it is, for capturing just that window.
	X, Y, W, H int
}

// Watcher answers whether the focused window is fullscreen.
type Watcher struct {
	ask func() (bool, error)
	// askWindow describes the focused window.
	askWindow func() (Window, error)
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
		w.ask, w.askWindow, w.what = swayFullscreen, swayWindow, "sway"
	case os.Getenv("HYPRLAND_INSTANCE_SIGNATURE") != "":
		w.ask, w.askWindow, w.what = hyprFullscreen, hyprWindow, "hyprland"
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

// ActiveWindow describes the focused window. The error is worth reporting
// to a caller here, unlike the fullscreen check: a ghost that cannot see
// what you are doing should say so once rather than pretend the desktop is
// empty.
func (w *Watcher) ActiveWindow() (Window, error) {
	if w.askWindow == nil {
		return Window{}, fmt.Errorf("no compositor IPC available")
	}
	return w.askWindow()
}

// --- sway ---

// swayFullscreen asks sway's IPC for the tree and looks for a focused node
// that is fullscreen. The protocol is a magic string, a little-endian
// length and type, then JSON.
func swayFullscreen() (bool, error) {
	body, err := swayTree()
	if err != nil {
		return false, err
	}
	var tree swayNode
	if err := json.Unmarshal(body, &tree); err != nil {
		return false, err
	}
	return tree.fullscreenFocused(), nil
}

// swayTree fetches the window tree over sway's IPC: a magic string, a
// little-endian length and type, then JSON.
func swayTree() ([]byte, error) {
	sock := os.Getenv("SWAYSOCK")
	if sock == "" {
		return nil, fmt.Errorf("SWAYSOCK is unset")
	}
	conn, err := net.DialTimeout("unix", sock, 2*time.Second)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(2 * time.Second))

	const getTree = 4
	var req []byte
	req = append(req, []byte("i3-ipc")...)
	req = binary.LittleEndian.AppendUint32(req, 0)
	req = binary.LittleEndian.AppendUint32(req, getTree)
	if _, err := conn.Write(req); err != nil {
		return nil, err
	}

	head := make([]byte, 14)
	if _, err := readFull(conn, head); err != nil {
		return nil, err
	}
	if string(head[:6]) != "i3-ipc" {
		return nil, fmt.Errorf("unexpected reply from sway")
	}
	n := binary.LittleEndian.Uint32(head[6:10])
	body := make([]byte, n)
	if _, err := readFull(conn, body); err != nil {
		return nil, err
	}
	return body, nil
}

// swayNode is the part of sway's tree this cares about.
type swayNode struct {
	Type           string `json:"type"`
	Focused        bool   `json:"focused"`
	FullscreenMode int    `json:"fullscreen_mode"`
	Name           string `json:"name"`
	AppID          string `json:"app_id"`
	Rect           struct {
		X, Y, Width, Height int
	} `json:"rect"`
	// WindowProperties carries the X11 class for XWayland windows, which
	// have no app_id.
	WindowProperties struct {
		Class string `json:"class"`
	} `json:"window_properties"`
	Nodes    []swayNode `json:"nodes"`
	Floating []swayNode `json:"floating_nodes"`
}

// swayWindow asks sway which window has focus.
func swayWindow() (Window, error) {
	body, err := swayTree()
	if err != nil {
		return Window{}, err
	}
	var tree swayNode
	if err := json.Unmarshal(body, &tree); err != nil {
		return Window{}, err
	}
	n, ok := tree.focusedWindow()
	if !ok {
		return Window{}, nil // a bare desktop is not an error
	}
	app := n.AppID
	if app == "" {
		app = n.WindowProperties.Class
	}
	return Window{
		AppID: app,
		Title: n.Name,
		X:     n.Rect.X, Y: n.Rect.Y, W: n.Rect.Width, H: n.Rect.Height,
	}, nil
}

// focusedWindow finds the focused container, if any window has focus.
func (n swayNode) focusedWindow() (swayNode, bool) {
	if n.Focused && (n.Type == "con" || n.Type == "floating_con") {
		return n, true
	}
	for _, c := range append(append([]swayNode{}, n.Nodes...), n.Floating...) {
		if got, ok := c.focusedWindow(); ok {
			return got, true
		}
	}
	return swayNode{}, false
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
	body, err := hyprAsk("j/activewindow")
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

// hyprAsk sends one command to Hyprland's socket.
func hyprAsk(cmd string) ([]byte, error) {
	sig := os.Getenv("HYPRLAND_INSTANCE_SIGNATURE")
	if sig == "" {
		return nil, fmt.Errorf("HYPRLAND_INSTANCE_SIGNATURE is unset")
	}
	runtime := os.Getenv("XDG_RUNTIME_DIR")
	if runtime == "" {
		return nil, fmt.Errorf("XDG_RUNTIME_DIR is unset")
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
		return nil, err
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(2 * time.Second))
	if _, err := conn.Write([]byte(cmd)); err != nil {
		return nil, err
	}
	return readAll(conn)
}

// hyprWindow asks Hyprland about the active window.
func hyprWindow() (Window, error) {
	body, err := hyprAsk("j/activewindow")
	if err != nil {
		return Window{}, err
	}
	var win struct {
		Class string `json:"class"`
		Title string `json:"title"`
		At    []int  `json:"at"`
		Size  []int  `json:"size"`
	}
	if err := json.Unmarshal(body, &win); err != nil {
		return Window{}, err
	}
	out := Window{AppID: win.Class, Title: win.Title}
	if len(win.At) == 2 && len(win.Size) == 2 {
		out.X, out.Y, out.W, out.H = win.At[0], win.At[1], win.Size[0], win.Size[1]
	}
	return out, nil
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
