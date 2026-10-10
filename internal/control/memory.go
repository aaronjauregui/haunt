package control

// Memory requests: `haunt memory reindex` asking a running ghost to
// rebuild its memory index. The index lives in the ghost's state.db, and
// the running driver is that file's one writer, so while it runs the CLI
// asks it rather than opening the file itself.
//
// The request is one line, {"type":"memory_reindex"}, answered with one
// line: "ok" and what was indexed, or "error: ...". The driver's handler
// for it belongs with the brain that owns the memory store; until a
// running ghost installs one, the socket reads the line as an event and
// answers that the type is not accepted, and the CLI reports that answer
// as it is.

import (
	"bufio"
	"encoding/json"
	"net"
	"strings"
	"time"
)

// ReqMemoryReindex is the request type that asks for a memory reindex.
const ReqMemoryReindex = "memory_reindex"

// memoryReplyTimeout bounds the wait for an answer. A reindex reads a
// few dozen small files; a ghost that takes longer is not answering.
const memoryReplyTimeout = 30 * time.Second

// MemoryReindex asks the named ghost to rebuild its memory index, and
// returns its one-line answer as it gave it. A *NotRunningError means no
// ghost of that name is listening, and the caller may open state.db
// itself.
func MemoryReindex(name string) (string, error) {
	path, err := SocketPath(name)
	if err != nil {
		return "", err
	}
	conn, err := net.Dial("unix", path)
	if err != nil {
		return "", &NotRunningError{Ghost: name, Err: err}
	}
	defer conn.Close()
	b, err := json.Marshal(struct {
		Type string `json:"type"`
	}{ReqMemoryReindex})
	if err != nil {
		return "", err
	}
	conn.SetDeadline(time.Now().Add(memoryReplyTimeout))
	if _, err := conn.Write(append(b, '\n')); err != nil {
		return "", err
	}
	reply, err := bufio.NewReader(conn).ReadString('\n')
	if err != nil && reply == "" {
		return "", err
	}
	return strings.TrimSpace(reply), nil
}
