package files

import (
	"errors"
	"io/fs"
	"os"
	"path"
	"strings"
	"syscall"
	"unicode"
)

// target is a model path resolved to its root: rel is clean, relative, and
// "." for the root itself.
type target struct {
	rt  *root
	rel string
}

// shown is the path in root/rel form.
func (t target) shown() string {
	if t.rel == "." {
		return t.rt.name
	}
	return t.rt.name + "/" + t.rel
}

// join is a path below t in root/rel form.
func (t target) join(name string) target {
	return target{rt: t.rt, rel: path.Join(t.rel, name)}
}

// parse turns a path from the model into a root and a path inside it. It
// refuses what could only mean somewhere else, before anything is opened;
// os.Root is the check that holds for everything it lets through.
func (rs *Roots) parse(p string) (target, error) {
	switch {
	case strings.ContainsRune(p, 0):
		return target{}, refusedf("a path cannot hold a NUL byte; %s", rs.folders())
	case strings.Contains(p, `\`):
		return target{}, refusedf("paths use forward slashes, like notes/todo.md; %s", rs.folders())
	case strings.HasPrefix(p, "~"):
		return target{}, refusedf("paths start with a shared folder's name, not ~, like notes/todo.md; %s", rs.folders())
	case strings.HasPrefix(p, "/"):
		return target{}, refusedf("paths start with a shared folder's name, not /, like notes/todo.md; %s", rs.folders())
	}
	name, rest, _ := strings.Cut(p, "/")
	rt := rs.find(name)
	if rt == nil {
		return target{}, refusedf("there is no shared folder called %s; %s", display(name), rs.folders())
	}
	rel := path.Clean("/" + rest)[1:]
	if rel == "" {
		rel = "."
	}
	// Cleaning against a leading slash swallows "..", so compare with the
	// plain clean to see whether it tried to climb out.
	if c := path.Clean(rest); c == ".." || strings.HasPrefix(c, "../") {
		return target{}, refusedf("%s leads outside the shared folder %s", display(p), rt.name)
	}
	return target{rt: rt, rel: rel}, nil
}

// folders names the shared folders for a refusal, so the model's next try
// can be a right one.
func (rs *Roots) folders() string {
	names := rs.Names()
	if len(names) == 0 {
		return "no folders are shared"
	}
	return "the shared folders are " + strings.Join(names, ", ")
}

// osRoot is the target's opened root, or the error saying it is not there.
func (rs *Roots) osRoot(t target) (*os.Root, error) {
	r := rs.open(t.rt)
	if r == nil {
		return nil, errors.New("the shared folder " + t.rt.name + " is unavailable (is the drive mounted?)")
	}
	return r, nil
}

// osErr says in root/rel words what went wrong with t. It never includes
// the operating system's own message, which names the host path.
func osErr(t target, err error) error {
	p := display(t.shown())
	var errno syscall.Errno
	switch {
	case isEscape(err):
		return refusedf("%s leads outside the shared folder %s", p, t.rt.name)
	case errors.Is(err, fs.ErrNotExist), errors.As(err, &errno) && errno == syscall.ENOTDIR:
		return errors.New("there is no " + p)
	case errors.Is(err, fs.ErrPermission):
		return errors.New(p + " cannot be read: permission denied")
	case errors.As(err, &errno) && errno == syscall.ELOOP:
		return errors.New(p + " is a loop of links")
	}
	return errors.New(p + " could not be read")
}

// isEscape reports os.Root refusing a path that leaves it. The error it
// uses is unexported, so its words are the only handle on it.
func isEscape(err error) bool {
	return err != nil && strings.Contains(err.Error(), "escapes from parent")
}

// display makes a name safe to put on a line: a filename may hold a
// newline or a control character, which would otherwise let a name write
// its own lines into a result. A long name is shortened.
func display(s string) string {
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return '�'
		}
		return r
	}, s)
	if len(s) > pathShown {
		cut := pathShown
		for cut > 0 && !runeStart(s[cut]) {
			cut--
		}
		s = s[:cut] + "…"
	}
	return s
}

// noControls replaces control characters, newlines among them.
func noControls(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return '\uFFFD'
		}
		return r
	}, s)
}

func runeStart(b byte) bool { return b&0xC0 != 0x80 }
