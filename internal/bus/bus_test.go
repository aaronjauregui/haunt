package bus

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/aaronjauregui/haunt/internal/proto"
)

func TestPublishAssignsSequenceAndTime(t *testing.T) {
	b := New(8)
	fixed := time.Date(2026, 8, 14, 12, 0, 0, 0, time.UTC)
	b.now = func() time.Time { return fixed }

	for i := 0; i < 3; i++ {
		if err := b.Publish(proto.Event{Type: proto.EventTick}); err != nil {
			t.Fatal(err)
		}
	}
	b.Close()

	var seqs []uint64
	for e := range b.Events() {
		seqs = append(seqs, e.Seq)
		if !e.Time.Equal(fixed) {
			t.Errorf("event time = %v, want %v", e.Time, fixed)
		}
	}
	if len(seqs) != 3 || seqs[0] != 1 || seqs[1] != 2 || seqs[2] != 3 {
		t.Errorf("seqs = %v, want [1 2 3]", seqs)
	}
}

func TestRecordAndReplayRoundtrip(t *testing.T) {
	rec := &bytes.Buffer{}
	b := New(8)
	b.SetRecorder(rec)

	in := []proto.Event{
		{Type: proto.EventBoot},
		{Type: proto.EventClick, X: 10, Y: 20},
		{Type: proto.EventMessage, Text: "hello"},
	}
	for _, e := range in {
		if err := b.Publish(e); err != nil {
			t.Fatal(err)
		}
	}
	b.Close()
	for range b.Events() {
	}

	events, err := ReadRecording(rec)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 3 {
		t.Fatalf("recorded %d events, want 3", len(events))
	}
	if events[1].Type != proto.EventClick || events[1].X != 10 || events[1].Y != 20 {
		t.Errorf("event 1 = %+v, want click at (10,20)", events[1])
	}

	// Replay into a fresh bus: payloads preserved, seq restamped.
	b2 := New(8)
	go func() {
		if err := b2.Replay(events, 0); err != nil {
			t.Error(err)
		}
		b2.Close()
	}()
	var got []proto.Event
	for e := range b2.Events() {
		got = append(got, e)
	}
	if len(got) != 3 {
		t.Fatalf("replayed %d events, want 3", len(got))
	}
	if got[2].Text != "hello" {
		t.Errorf("replayed text = %q, want hello", got[2].Text)
	}
	if got[0].Seq != 1 {
		t.Errorf("replayed seq = %d, want restamped to 1", got[0].Seq)
	}
}

func TestPublishAfterCloseFails(t *testing.T) {
	b := New(1)
	b.Close()
	if err := b.Publish(proto.Event{Type: proto.EventTick}); err == nil {
		t.Fatal("expected error publishing to closed bus")
	}
}

func TestReadRecordingSkipsBlankAndRejectsGarbage(t *testing.T) {
	events, err := ReadRecording(strings.NewReader(`{"type":"tick","seq":1,"time":"2026-08-14T12:00:00Z"}` + "\n\n"))
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 {
		t.Fatalf("got %d events, want 1", len(events))
	}
	if _, err := ReadRecording(strings.NewReader("not json\n")); err == nil {
		t.Fatal("expected error for garbage input")
	}
}
