package control

// Requests: the half of the control socket that asks something of a
// running ghost and waits for the answer, rather than dropping an event on
// it. Keeping them on this socket keeps the socket the one door into a
// running ghost, and so the driver the one writer of its store.
//
// A request is a line whose "type" names a handler the ghost installed
// (Handle). Everything else on the line is read as an event, exactly as
// before, so `haunt say` and anything scripted against the socket see no
// difference. A connection that sends a request gets one line back and is
// closed: one request per connection means the client hanging up is the
// cancellation, with nothing to route.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// Handler answers one request with one line.
type Handler func(ctx context.Context) string

// Handle installs h as the answer to requests of type typ. It may be
// called at any point, including while the server is serving; nil removes
// the handler, and the line is then read as an event again.
func (s *Server) Handle(typ string, h Handler) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.handlers == nil {
		s.handlers = map[string]Handler{}
	}
	if h == nil {
		delete(s.handlers, typ)
		return
	}
	s.handlers[typ] = h
}

// handler is the handler for a line, if the line is a request one is
// installed for.
func (s *Server) handler(line []byte) Handler {
	var head struct {
		Type string `json:"type"`
	}
	if json.Unmarshal(line, &head) != nil || head.Type == "" {
		return nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.handlers[head.Type]
}

// replyTimeout bounds writing an answer, so a client that stopped reading
// cannot pin the goroutine.
const replyTimeout = 10 * time.Second

// NotRunningError is what the clients return when nothing answers on the
// ghost's socket. It is a type so that a caller which can do the work
// itself when the ghost is not running (`haunt memory reindex`) can tell
// "not running" from "running and said no".
type NotRunningError struct {
	Ghost string
	Err   error
}

func (e *NotRunningError) Error() string {
	return fmt.Sprintf("ghost %q does not appear to be running: %v", e.Ghost, e.Err)
}

func (e *NotRunningError) Unwrap() error { return e.Err }

// IsNotRunning reports whether err means the ghost was not there to ask.
func IsNotRunning(err error) bool {
	var nr *NotRunningError
	return errors.As(err, &nr)
}
