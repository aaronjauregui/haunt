package ghost

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/aaronjauregui/haunt/internal/files"
	"github.com/aaronjauregui/haunt/internal/hlog"
)

// The person's files, as a ghost reaches them: three tools, and the rules
// around them.
//
// The folders are the person's to name, in haunt's config, and the reading
// is the driver's to do (internal/files): the model never sees a file or a
// path on the host, only what these tools hand back, in root/rel form.
// That is what lets the rules here apply at all — a read the driver does
// not see is one it cannot taint a turn with, journal, or refuse on a turn
// nobody started.
//
// The gates mirror the web's (web.go), in the same order and by the same
// trust table (trust.go), with the one difference that files start
// closed:
//
//   - Something shared. With no folders in the config there is nothing to
//     read, whatever the package asked for.
//   - The `files` capability: on cloud asked for in the balloon like any
//     other, and refused rather than asked on a turn nobody started; on
//     local needing no question unless it was refused for good.
//   - On a turn nobody started: on cloud the [files] unprompted policy,
//     "never" by default because files are more personal than the web,
//     then quiet hours, then a ration of its own; on local only quiet
//     hours and the ration.
//   - Taint, afterwards. A file in ~/Downloads is a stranger's words as
//     much as a page is, so text from a file marks the turn (taintFile),
//     and what reaches past the person, or on cloud would carry the
//     file's words off the machine, is asked about again (web.go,
//     gatedAfter).
//
// Listing is gated like reading — knowing what files exist is already
// personal — but does not taint: a list of names is the person's own
// organising, not somebody else's words.

// filesRef is what a file tool's answer is logged as when it was too long
// to read in full. internal/files keeps every result under the clip, so
// this is a guard rather than a path taken.
func filesRef(tool string) toolRef { return toolRef{Server: "files", Tool: tool} }

// allowFiles settles whether a file tool may run now, and charges what an
// unprompted one costs. The refusal says which rule stopped it, so the
// model can say nothing rather than invent what the file says.
func (g *Ghost) allowFiles() error {
	if g.files == nil || g.files.Empty() {
		return refuse("no folders are shared with you")
	}
	return g.readGate("files", g.filesCfg.UnpromptedMode(), func() error {
		if g.backend == nil {
			return refuse("only when the person asks")
		}
		if err := g.backend.SpendUnpromptedFile(); err != nil {
			return refuse("%v", err)
		}
		return nil
	})
}

// fileError is how a failure from internal/files reaches the model. Its
// words are already safe to show — root/rel paths, never the host's — so
// all that is left is to say whether the driver would not or could not.
func fileError(err error) error {
	if errors.Is(err, files.ErrRefused) {
		return refuse("%s", err.Error())
	}
	return err
}

func toolListFiles(g *Ghost, _ context.Context, args json.RawMessage) (string, error) {
	var a struct {
		Path string `json:"path"`
	}
	if err := decode(args, &a); err != nil {
		return "", err
	}
	if err := g.allowFiles(); err != nil {
		return "", err
	}
	out, err := g.files.List(a.Path)
	if err != nil {
		return "", fileError(err)
	}
	what := "the shared folders"
	if p := strings.Trim(a.Path, "/"); p != "" {
		what = clip(p, 200)
	}
	g.execNote("listed " + what)
	g.log.Info(hlog.Tool, "listed %s", what)
	// No taint: names are the person's own organising, and a turn that
	// only looked at them has read nobody else's words.
	return g.readable(filesRef("list_files"), out), nil
}

func toolReadFile(g *Ghost, ctx context.Context, args json.RawMessage) (string, error) {
	var a struct {
		Path     string `json:"path"`
		From     int64  `json:"from"`
		Question string `json:"question"`
	}
	if err := decode(args, &a); err != nil {
		return "", err
	}
	if strings.TrimSpace(a.Path) == "" {
		return "", fmt.Errorf("read which file? list_files shows what there is")
	}
	if a.From < 0 {
		a.From = 0
	}
	if err := g.allowFiles(); err != nil {
		return "", err
	}
	f, err := g.files.Read(ctx, a.Path, a.From)
	if err != nil {
		return "", fileError(err)
	}
	if f.Kind == files.KindImage {
		return g.describeFile(ctx, f, a.Question)
	}
	g.execNote("read " + clip(f.Path, 200))
	g.log.Info(hlog.Tool, "read %s from %d", clip(f.Path, 200), a.From)
	// Only words from the file taint: a read past the end brought back a
	// note that there is nothing more, and nobody else's words.
	if f.HasText {
		g.taint(taintFile)
	}
	return g.readable(filesRef("read_file"), f.Text), nil
}

// describeFile shows a picture from a shared folder to the vision model.
//
// No screen grant is in it — nothing on the person's screen is being looked
// at, and the files grant already covers the folder — but the vision ration
// is, since it is the same model doing the same work (DescribeImage).
func (g *Ghost) describeFile(ctx context.Context, f files.File, question string) (string, error) {
	if g.backend == nil || !g.backend.VisionEnabled() {
		return "", fmt.Errorf("there is no vision model configured, so pictures cannot be read")
	}
	question = strings.TrimSpace(question)
	if question == "" {
		question = files.DefaultQuestion
	}
	desc, err := g.backend.DescribeImage(ctx, f.PNG, clip(question, 300))
	if err != nil {
		g.log.Warn(hlog.Backend, "describing %s: %v", f.Path, err)
		return "", err
	}
	g.execNote("looked at " + clip(f.Path, 200))
	g.log.Info(hlog.Tool, "looked at %s", clip(f.Path, 200))
	// A description is the vision model's words, but the writing it reports
	// is the file's: a photographed note taints like a typed one.
	g.taint(taintFile)
	return g.readable(filesRef("read_file"), files.RenderPicture(f, desc)), nil
}

func toolSearchFiles(g *Ghost, ctx context.Context, args json.RawMessage) (string, error) {
	var a struct {
		Text      string `json:"text"`
		Path      string `json:"path"`
		NamesOnly bool   `json:"names_only"`
	}
	if err := decode(args, &a); err != nil {
		return "", err
	}
	if strings.TrimSpace(a.Text) == "" {
		return "", fmt.Errorf("search for what?")
	}
	if err := g.allowFiles(); err != nil {
		return "", err
	}
	found, err := g.files.Search(ctx, a.Text, a.Path, a.NamesOnly)
	if err != nil {
		return "", fileError(err)
	}
	where := ""
	if p := strings.Trim(a.Path, "/"); p != "" {
		where = " in " + clip(p, 120)
	}
	matches := fmt.Sprintf("%d %s", found.Matches, plural(found.Matches, "match", "matches"))
	g.execNote(fmt.Sprintf("searched files%s for %q (%s)", where, clip(a.Text, 120), matches))
	g.log.Info(hlog.Tool, "searched files%s for %q: %s", where, clip(a.Text, 120), matches)
	// A line from inside a file is the file's words; a list of names that
	// matched is not.
	if found.HasContent {
		g.taint(taintFile)
	}
	return g.readable(filesRef("search_files"), found.Text), nil
}
