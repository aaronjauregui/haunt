package ghost

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/aaronjauregui/haunt/internal/hlog"
	"github.com/aaronjauregui/haunt/internal/proto"
)

// Looking at the screen. The most invasive thing in the project, so it is
// hedged accordingly: the manifest must ask for the `screen` capability,
// the user must grant it, a vision model must be configured, the focused
// application must not be on the ignore list, nothing may be fullscreen,
// and the ghost shows that it is looking while it looks.
//
// Only the focused window is captured, never the whole screen. That is not
// only politeness: a single window at native resolution is legible to a
// model, while a downscaled multi-monitor desktop is a blur, and the
// compositor already knows which window is which so there is nothing for a
// model to guess at.

// lookQuestion is asked of the vision model unless the brain supplies its
// own. A specific question earns a usable answer; "comment on this" earns
// fluent noise.
const lookQuestion = "In one sentence, what is the person doing in this window? " +
	"Name the application and the specific thing on screen — a file, an error, " +
	"a page title — rather than describing the layout."

// execLook captures the focused window and asks the vision model about it.
func (g *Ghost) execLook(in proto.Intent) {
	tag := in.Tag
	// The capability was settled before the lock (see consent.go).
	if g.aiSvc == nil || !g.aiSvc.VisionEnabled() {
		g.publishAIError("no vision model configured in the haunt config", tag)
		return
	}
	if g.desktop == nil || g.desktop.Mechanism() == "none" {
		g.publishAIError("no compositor IPC, so there is no way to know what to look at", tag)
		return
	}
	if g.desktop.Fullscreen() {
		// Whatever is fullscreen is a game or a film, and interrupting it
		// is what the rest of the design already refuses to do.
		g.publishAIError("not while something is fullscreen", tag)
		return
	}

	win, err := g.desktop.ActiveWindow()
	if err != nil {
		g.publishAIError("cannot see the screen: "+err.Error(), tag)
		return
	}
	if win.W <= 0 || win.H <= 0 {
		g.publishAIError("nothing is focused", tag)
		return
	}
	if g.ignoredApp(win.AppID) {
		g.log.Info(hlog.Screen, "not looking at %s: it is on the ignore list", win.AppID)
		g.publishAIError("that application is on the ignore list", tag)
		return
	}

	// Visibly indicated while active, as the design requires: a ghost that
	// can look at your screen should never do it invisibly.
	g.showBalloon("(looking…)")

	png, err := captureWindow(win.X, win.Y, win.W, win.H)
	if err != nil {
		g.log.Warn(hlog.Screen, "capture: %v", err)
		g.publishAIError("cannot capture the screen: "+err.Error(), tag)
		return
	}

	question := lookQuestion
	if strings.TrimSpace(in.Text) != "" {
		question = in.Text
	}
	answer, err := g.aiSvc.Look(context.Background(), question, png)
	if err != nil {
		g.log.Warn(hlog.Screen, "look: %v", err)
		g.publishAIError(err.Error(), tag)
		return
	}

	// The answer goes to the brain, not to the balloon: what the ghost
	// does with having looked is its own business, and saying "I see you
	// have a terminal open" unprompted is the tiresome version of this
	// feature.
	g.hideBalloon()
	g.publish(proto.Event{
		Type: proto.EventAILook,
		Text: answer,
		Name: win.AppID,
		Tag:  tag,
	})
}

// captureWindow grabs one window with grim. Screen capture has no client
// protocol we can use — wlr-screencopy is not in the vendored tree — and
// grim is the standard tool for it on wlroots compositors.
func captureWindow(x, y, w, h int) ([]byte, error) {
	if _, err := exec.LookPath("grim"); err != nil {
		return nil, fmt.Errorf("grim is not installed; it is how screenshots are taken")
	}
	f, err := os.CreateTemp("", "haunt-look-*.png")
	if err != nil {
		return nil, err
	}
	path := f.Name()
	f.Close()
	defer os.Remove(path)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "grim",
		"-g", fmt.Sprintf("%d,%d %dx%d", x, y, w, h), path)
	if out, err := cmd.CombinedOutput(); err != nil {
		return nil, fmt.Errorf("grim: %w: %s", err, strings.TrimSpace(string(out)))
	}
	png, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		return nil, err
	}
	if len(png) == 0 {
		return nil, fmt.Errorf("grim produced an empty image")
	}
	return png, nil
}
