// Package bus is the single channel every trigger flows through: input from
// the renderer, timers, external messages, lifecycle. It assigns sequence
// numbers, optionally records sessions to JSONL, and can replay a recording
// deterministically.
package bus

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/aaronjauregui/haunt/internal/proto"
)

// Bus fan-ins events and delivers them, in order, to a single consumer
// channel. One bus per ghost.
type Bus struct {
	mu       sync.Mutex
	seq      uint64
	out      chan proto.Event
	recorder io.Writer
	closed   bool

	// now is replaceable for deterministic tests.
	now func() time.Time
}

// New creates a bus. buffer is the consumer channel depth; events beyond it
// block the publisher, which is the backpressure we want.
func New(buffer int) *Bus {
	return &Bus{
		out: make(chan proto.Event, buffer),
		now: time.Now,
	}
}

// Events is the consumer side: the brain host reads from this.
func (b *Bus) Events() <-chan proto.Event { return b.out }

// SetRecorder starts recording every published event as JSONL to w.
// Pass nil to stop recording.
func (b *Bus) SetRecorder(w io.Writer) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.recorder = w
}

// Publish stamps and delivers an event. Seq and Time are assigned here; any
// values already present are overwritten.
func (b *Bus) Publish(e proto.Event) error {
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return fmt.Errorf("bus is closed")
	}
	b.seq++
	e.Seq = b.seq
	e.Time = b.now()
	rec := b.recorder
	if rec != nil {
		line, err := json.Marshal(e)
		if err != nil {
			b.mu.Unlock()
			return err
		}
		if _, err := rec.Write(append(line, '\n')); err != nil {
			b.mu.Unlock()
			return fmt.Errorf("recording event: %w", err)
		}
	}
	b.mu.Unlock()

	b.out <- e
	return nil
}

// Close stops the bus; the consumer channel is closed after in-flight
// publishes complete.
func (b *Bus) Close() {
	b.mu.Lock()
	defer b.mu.Unlock()
	if !b.closed {
		b.closed = true
		close(b.out)
	}
}

// ReadRecording parses a JSONL session recording.
func ReadRecording(r io.Reader) ([]proto.Event, error) {
	var events []proto.Event
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	line := 0
	for sc.Scan() {
		line++
		if len(sc.Bytes()) == 0 {
			continue
		}
		var e proto.Event
		if err := json.Unmarshal(sc.Bytes(), &e); err != nil {
			return nil, fmt.Errorf("recording line %d: %w", line, err)
		}
		events = append(events, e)
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	return events, nil
}

// Replay publishes recorded events in order, preserving their original
// payloads but re-stamping seq/time (a replayed session is a new session).
// If speed > 0, inter-event gaps from the recording are reproduced divided
// by speed; with speed == 0 events are published as fast as the consumer
// accepts them.
func (b *Bus) Replay(events []proto.Event, speed float64) error {
	var prev time.Time
	for _, e := range events {
		if speed > 0 && !prev.IsZero() {
			gap := e.Time.Sub(prev)
			if gap > 0 {
				time.Sleep(time.Duration(float64(gap) / speed))
			}
		}
		prev = e.Time
		if err := b.Publish(e); err != nil {
			return err
		}
	}
	return nil
}
