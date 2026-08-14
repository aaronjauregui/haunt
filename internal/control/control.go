// Package control is the inbound message channel: a per-ghost Unix socket
// where external programs (scripts, git hooks, CI, the haunt CLI itself)
// push events to a running ghost. This is the SSTP replacement.
package control

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"

	"github.com/aaronjauregui/haunt/internal/proto"
)

// allowedInbound are the event types external clients may inject. Everything
// else (lifecycle, input, timers) is driver-internal.
var allowedInbound = map[proto.EventType]bool{
	proto.EventMessage: true,
}

// SocketDir returns the directory holding control sockets:
// HAUNT_RUNTIME_DIR if set (tests, custom setups), else
// $XDG_RUNTIME_DIR/haunt.
func SocketDir() string {
	if dir := os.Getenv("HAUNT_RUNTIME_DIR"); dir != "" {
		return dir
	}
	base := os.Getenv("XDG_RUNTIME_DIR")
	if base == "" {
		base = os.TempDir()
	}
	return filepath.Join(base, "haunt")
}

// SocketPath returns the control socket path for a ghost name.
func SocketPath(name string) (string, error) {
	dir := SocketDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	return filepath.Join(dir, name+".sock"), nil
}

// Server accepts inbound events and forwards them to a publish function.
type Server struct {
	ln      net.Listener
	path    string
	publish func(proto.Event) error
	log     func(string)
}

// Listen starts the control server for the named ghost. Stale sockets from
// crashed sessions are replaced.
func Listen(name string, publish func(proto.Event) error, log func(string)) (*Server, error) {
	path, err := SocketPath(name)
	if err != nil {
		return nil, err
	}
	// If a previous session crashed, the socket file lingers. If something
	// answers on it, another instance is live; otherwise remove and rebind.
	if conn, err := net.Dial("unix", path); err == nil {
		conn.Close()
		return nil, fmt.Errorf("ghost %q already running (socket %s is live)", name, path)
	}
	os.Remove(path)

	ln, err := net.Listen("unix", path)
	if err != nil {
		return nil, err
	}
	s := &Server{ln: ln, path: path, publish: publish, log: log}
	go s.accept()
	return s, nil
}

func (s *Server) accept() {
	for {
		conn, err := s.ln.Accept()
		if err != nil {
			return // listener closed
		}
		go s.serve(conn)
	}
}

// serve reads JSONL events; each line is either accepted and published or
// answered with an error line. The reply is "ok" or "error: ...".
func (s *Server) serve(conn net.Conn) {
	defer conn.Close()
	sc := bufio.NewScanner(conn)
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for sc.Scan() {
		if len(sc.Bytes()) == 0 {
			continue
		}
		reply := "ok"
		var e proto.Event
		if err := json.Unmarshal(sc.Bytes(), &e); err != nil {
			reply = "error: bad event json: " + err.Error()
		} else if !allowedInbound[e.Type] {
			reply = fmt.Sprintf("error: event type %q not accepted on the control socket", e.Type)
		} else if err := s.publish(e); err != nil {
			reply = "error: " + err.Error()
		}
		if reply != "ok" && s.log != nil {
			s.log("haunt: control: " + reply)
		}
		fmt.Fprintln(conn, reply)
	}
}

// Close stops the server and removes the socket file.
func (s *Server) Close() {
	s.ln.Close()
	os.Remove(s.path)
}

// Send delivers one event to the named ghost's control socket and waits for
// the acknowledgement.
func Send(name string, e proto.Event) error {
	path, err := SocketPath(name)
	if err != nil {
		return err
	}
	conn, err := net.Dial("unix", path)
	if err != nil {
		return fmt.Errorf("ghost %q does not appear to be running: %w", name, err)
	}
	defer conn.Close()
	b, err := json.Marshal(e)
	if err != nil {
		return err
	}
	if _, err := conn.Write(append(b, '\n')); err != nil {
		return err
	}
	reply, err := bufio.NewReader(conn).ReadString('\n')
	if err != nil {
		return fmt.Errorf("no acknowledgement from ghost: %w", err)
	}
	if reply != "ok\n" {
		return fmt.Errorf("ghost rejected event: %s", reply)
	}
	return nil
}
