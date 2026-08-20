package control

import (
	"net"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Who you were talking to. With one ghost running, `haunt say hello` has
// exactly one place to go. With three, it needs an answer that is not "pass
// -ghost every time", and the honest one is the ghost you last interacted
// with: you poked it, you typed at it, you answered its question. That is
// the conversation you are in.
//
// It is a file per ghost with nothing in it, next to the sockets, touched
// on interaction — mtime is the whole record. A single "most recent" file
// would be smaller and wrong: it can name a ghost that has since stopped,
// and then `haunt say` fails while a perfectly good ghost is on screen.
// Here liveness and recency are separate questions, and both are asked.

const recentSuffix = ".last"

// Touch records that the user has just interacted with a ghost. Failures
// are silent by design: this decides a default, and losing it costs the
// user one -ghost flag rather than anything they would want an error about.
func Touch(name string) {
	dir := SocketDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return
	}
	path := filepath.Join(dir, name+recentSuffix)
	now := time.Now()
	if err := os.Chtimes(path, now, now); err == nil {
		return
	}
	if f, err := os.Create(path); err == nil {
		f.Close()
	}
}

// Forget removes a ghost's recency mark, called when it stops.
func Forget(name string) {
	os.Remove(filepath.Join(SocketDir(), name+recentSuffix))
}

// Running lists the ghosts with a live control socket, sorted by name.
// A socket file left by a crashed session is not live and is not counted.
func Running() []string {
	socks, err := filepath.Glob(filepath.Join(SocketDir(), "*.sock"))
	if err != nil {
		return nil
	}
	var names []string
	for _, s := range socks {
		conn, err := net.Dial("unix", s)
		if err != nil {
			continue
		}
		conn.Close()
		names = append(names, strings.TrimSuffix(filepath.Base(s), ".sock"))
	}
	sort.Strings(names)
	return names
}

// Recent returns the running ghost the user last interacted with. It falls
// back to the only running ghost when nothing has been touched yet — a
// ghost you have not spoken to is still the one you meant — and returns ""
// when the choice is genuinely ambiguous, so the caller can say so.
func Recent() string {
	live := Running()
	if len(live) == 0 {
		return ""
	}
	best, bestAt := "", time.Time{}
	for _, n := range live {
		fi, err := os.Stat(filepath.Join(SocketDir(), n+recentSuffix))
		if err != nil {
			continue
		}
		if fi.ModTime().After(bestAt) {
			best, bestAt = n, fi.ModTime()
		}
	}
	if best != "" {
		return best
	}
	if len(live) == 1 {
		return live[0]
	}
	return ""
}
