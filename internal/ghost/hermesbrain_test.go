package ghost

import (
	"context"
	"encoding/json"
	"image"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aaronjauregui/haunt/internal/hermes"
	"github.com/aaronjauregui/haunt/internal/proto"
	"github.com/aaronjauregui/haunt/internal/wlclient"
)

// lockedStore is memStore for tests where the turn worker writes while the
// test reads.
type lockedStore struct {
	mu sync.Mutex
	m  map[string]string
}

func (s *lockedStore) Set(k, v string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.m[k] = v
	return nil
}

func (s *lockedStore) Get(k string) (string, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.m[k]
	return v, ok, nil
}

func (s *lockedStore) All() (map[string]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make(map[string]string, len(s.m))
	for k, v := range s.m {
		out[k] = v
	}
	return out, nil
}

func (s *lockedStore) Close() error { return nil }

// fakeTurner answers turns from a script, recording what it was sent.
type fakeTurner struct {
	mu     sync.Mutex
	inputs []string
	// reply answers one input. Nil answers NO_REPLY.
	reply func(ctx context.Context, req hermes.TurnRequest) (hermes.Reply, error)
	// turned is signalled after each turn.
	turned chan string
}

func (f *fakeTurner) Turn(ctx context.Context, req hermes.TurnRequest) (hermes.Reply, error) {
	f.mu.Lock()
	f.inputs = append(f.inputs, req.Input)
	reply := f.reply
	f.mu.Unlock()
	defer func() {
		if f.turned != nil {
			f.turned <- req.Input
		}
	}()
	if reply == nil {
		return hermes.Reply{Text: hermes.NoReply}, nil
	}
	return reply(ctx, req)
}

func (f *fakeTurner) sent() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.inputs...)
}

// hermesGhost is a ghost on a fake hermes, with a screen to show things on
// and a curiosity allowance of `curiosity` wakes a day.
func hermesGhost(t *testing.T, curiosity int) (*Ghost, *hermesBrain, *fakeTurner) {
	t.Helper()
	g, _ := choosingGhost(t)
	store := &lockedStore{m: map[string]string{}}
	g.store = store
	g.manifest = &Manifest{Ghost: GhostSection{Name: "wisp", Capabilities: []string{"llm", "message"}}}
	g.surface = &wlclient.SpriteSurface{}
	g.onHermes = true
	g.stopping = make(chan struct{})
	g.curiosity = newCuriosity(curiosity, store, g.log)
	g.obligation = newObligation(5, store, g.log)
	ft := &fakeTurner{turned: make(chan string, 16)}
	b := newHermesBrain(g, ft, nil)
	g.brain = b
	t.Cleanup(func() { b.Close() })
	return g, b, ft
}

func waitTurn(t *testing.T, ft *fakeTurner) string {
	t.Helper()
	select {
	case in := <-ft.turned:
		// The worker finishes the turn after the turner returns.
		time.Sleep(20 * time.Millisecond)
		return in
	case <-time.After(3 * time.Second):
		t.Fatal("no turn was sent")
		return ""
	}
}

func spent(a *allowance) int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return len(a.spends)
}

// The person's words go as they are, with no envelope; the reply is shown.
func TestHermesPersonWordsGoVerbatim(t *testing.T) {
	g, b, ft := hermesGhost(t, 0)
	ft.reply = func(_ context.Context, req hermes.TurnRequest) (hermes.Reply, error) {
		req.OnDelta("Of course.")
		return hermes.Reply{Text: "Of course."}, nil
	}
	g.markPrompted()
	b.Deliver(proto.Event{Type: proto.EventMessage, Text: "  remind me in ten to check the oven "})
	if in := waitTurn(t, ft); in != "remind me in ten to check the oven" {
		t.Errorf("sent %q", in)
	}
	if !g.blnShowing() {
		t.Error("the reply did not reach the balloon")
	}
}

// A wake the model answers with silence costs nothing and shows nothing;
// one it answers with words is paid for.
func TestHermesSilenceIsRefunded(t *testing.T) {
	g, b, ft := hermesGhost(t, 3)
	b.Deliver(proto.Event{Type: proto.EventIdle})
	if in := waitTurn(t, ft); !strings.HasPrefix(in, "[haunt event] kind=idle") {
		t.Fatalf("sent %q", in)
	}
	if n := spent(g.curiosity); n != 0 {
		t.Errorf("a silent wake was charged: %d spent", n)
	}
	if g.blnShowing() {
		t.Error("silence reached the balloon")
	}
	whys, _ := DecodeWhy(mustGet(t, g, whyKey))
	if len(whys) != 1 || !whys[0].Suppressed || !whys[0].SelfPrompt {
		t.Errorf("the why-trace does not record a suppressed turn: %+v", whys)
	}

	g.endResponding()
	ft.reply = func(context.Context, hermes.TurnRequest) (hermes.Reply, error) {
		return hermes.Reply{Text: "Still at it?"}, nil
	}
	b.Deliver(proto.Event{Type: proto.EventIdle})
	waitTurn(t, ft)
	if n := spent(g.curiosity); n != 1 {
		t.Errorf("a spoken wake was not charged: %d spent", n)
	}
	if !g.blnShowing() {
		t.Error("the remark did not reach the balloon")
	}
}

// A picture the model drew in a turn is not painted over by the words that
// follow it: they wait for the picture to have had its time.
func TestHermesPictureOutlastsTheReply(t *testing.T) {
	g, b, ft := hermesGhost(t, 0)
	ft.reply = func(_ context.Context, req hermes.TurnRequest) (hermes.Reply, error) {
		req.OnTool(hermes.ToolEvent{Name: "mcp__haunt__draw"})
		g.showImageBalloon(image.NewNRGBA(image.Rect(0, 0, 8, 8)))
		req.OnTool(hermes.ToolEvent{Name: "mcp__haunt__draw", Done: true})
		req.OnDelta("There you are.")
		time.Sleep(150 * time.Millisecond)
		req.OnDelta(" A sunset.")
		return hermes.Reply{Text: "There you are. A sunset.", Tools: []string{"mcp__haunt__draw"}}, nil
	}
	g.markPrompted()
	b.Deliver(proto.Event{Type: proto.EventMessage, Text: "draw a sunset"})
	waitTurn(t, ft)
	if !g.pictureShowing() {
		t.Error("the reply replaced the picture")
	}
}

// The driver decides whether an event reaches the model at all: with the
// allowance spent, an idle event is never sent; the person's words are.
func TestHermesGateIsBeforeSend(t *testing.T) {
	g, b, ft := hermesGhost(t, 0)
	b.Deliver(proto.Event{Type: proto.EventIdle})
	b.Deliver(proto.Event{Type: proto.EventObserved, From: "Moth", Name: proto.ActedSpoke, Text: "hi"})
	time.Sleep(50 * time.Millisecond)
	if got := ft.sent(); len(got) != 0 {
		t.Fatalf("an unaffordable wake was sent: %q", got)
	}
	g.markPrompted()
	b.Deliver(proto.Event{Type: proto.EventMessage, Text: "hello"})
	if in := waitTurn(t, ft); in != "hello" {
		t.Errorf("sent %q", in)
	}
}

// Talking through the model is the llm capability. Without it, nothing
// unprompted is sent; the person's words ask for it first, in the balloon.
func TestHermesNeedsTheLLMCapability(t *testing.T) {
	g, b, ft := hermesGhost(t, 5)
	r := &recorder{}
	g.opts.Granted = r.granted
	g.opts.Refused = r.refusedCap
	g.opts.Consent = r.record

	b.Deliver(proto.Event{Type: proto.EventIdle})
	time.Sleep(50 * time.Millisecond)
	if got := ft.sent(); len(got) != 0 {
		t.Fatalf("an ungranted wake was sent: %q", got)
	}
	if n := spent(g.curiosity); n != 0 {
		t.Errorf("an ungranted wake was charged: %d spent", n)
	}

	g.markPrompted()
	answer(t, g, consentAllow)
	b.Deliver(proto.Event{Type: proto.EventMessage, Text: "hello"})
	if in := waitTurn(t, ft); in != "hello" {
		t.Errorf("sent %q", in)
	}
	if !r.granted("llm") {
		t.Error("the grant was not recorded")
	}
}

// While a turn is in flight, the person's words wait their turn and an
// unprompted event is dropped.
func TestHermesOneTurnAtATime(t *testing.T) {
	_, b, ft := hermesGhost(t, 5)
	release := make(chan struct{})
	ft.reply = func(ctx context.Context, req hermes.TurnRequest) (hermes.Reply, error) {
		if req.Input == "first" {
			<-release
		}
		return hermes.Reply{Text: "ok"}, nil
	}
	b.Deliver(proto.Event{Type: proto.EventMessage, Text: "first"})
	deadline := time.Now().Add(time.Second)
	for !b.busy.Load() && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	b.Deliver(proto.Event{Type: proto.EventMessage, Text: "second"})
	b.Deliver(proto.Event{Type: proto.EventIdle})
	close(release)
	waitTurn(t, ft)
	waitTurn(t, ft)
	time.Sleep(50 * time.Millisecond)
	got := ft.sent()
	if len(got) != 2 || got[0] != "first" || got[1] != "second" {
		t.Errorf("sent %q, want first then second and no idle", got)
	}
}

// Envelopes carry fields, not instructions.
func TestHermesEnvelopes(t *testing.T) {
	g, b, ft := hermesGhost(t, 5)
	b.Deliver(proto.Event{Type: proto.EventBoot})
	if in := waitTurn(t, ft); in != "[haunt event] kind=boot first_time=true" {
		t.Errorf("boot sent %q", in)
	}
	g.endResponding()
	b.Deliver(proto.Event{Type: proto.EventMessage, From: "Moth", Text: "boo"})
	if in := waitTurn(t, ft); in != `[haunt event] kind=told from=Moth text=boo` {
		t.Errorf("told sent %q", in)
	}
	g.endResponding()
	b.Deliver(proto.Event{Type: proto.EventSession, Session: &proto.SessionFact{Kind: "dwell", App: "nvim", Title: "x.go", Minutes: 42}})
	if in := waitTurn(t, ft); in != `[haunt event] kind=session shape=dwell app=nvim title=x.go minutes=42` {
		t.Errorf("session sent %q", in)
	}
	g.endResponding()
	g.markObliged()
	b.Deliver(proto.Event{Type: proto.EventCron, Name: "morning"})
	if in := waitTurn(t, ft); in != `[haunt event] kind=occasion name=morning` {
		t.Errorf("occasion sent %q", in)
	}
}

// A reminder comes due as an envelope and is discharged once it was said.
func TestHermesErrandIsResolvedOnceRaised(t *testing.T) {
	g, b, ft := hermesGhost(t, 0)
	g.markPrompted()
	g.execIntend(proto.Intent{Name: "hermes/oven", Text: "check the oven", DelayMS: 60000})
	g.endResponding()
	ft.reply = func(context.Context, hermes.TurnRequest) (hermes.Reply, error) {
		return hermes.Reply{Text: "The oven!"}, nil
	}
	g.fireIntention("hermes/oven")
	b.Deliver(proto.Event{Type: proto.EventIntention, Name: "hermes/oven"})
	in := waitTurn(t, ft)
	if in != `[haunt event] kind=intention_due name=hermes/oven text="check the oven" asked=true` {
		t.Errorf("sent %q", in)
	}
	if _, ok := g.intentionNamed("hermes/oven"); ok {
		t.Error("the errand is still held after it was raised")
	}
	if n := spent(g.obligation); n != 1 {
		t.Errorf("the errand was not paid from the obligation allowance: %d", n)
	}
}

// An errand that came due and was not dealt with rides on every turn until
// it is resolved; one still to come does not.
func TestHermesTurnContextCarriesWhatIsOwed(t *testing.T) {
	g, _, _ := hermesGhost(t, 0)
	g.execIntend(proto.Intent{Name: "hermes/oven", Text: "check the oven", DelayMS: 60000})
	g.execIntend(proto.Intent{Name: "hermes/later", Text: "stretch", DelayMS: 3600000})
	now := time.Date(2026, 9, 17, 14, 3, 0, 0, time.Local)
	if ctx := g.turnContext(now); !strings.HasPrefix(ctx, "Now: Thu 17 Sep 14:03 (afternoon).") ||
		strings.Contains(ctx, "oven") {
		t.Errorf("before it came due: %q", ctx)
	}
	g.fireIntention("hermes/oven")
	ctx := g.turnContext(now)
	if !strings.Contains(ctx, `"check the oven" (hermes/oven)`) || strings.Contains(ctx, "stretch") {
		t.Errorf("once owed: %q", ctx)
	}
	g.execResolve("hermes/oven")
	if ctx := g.turnContext(now); strings.Contains(ctx, "oven") {
		t.Errorf("after it was resolved: %q", ctx)
	}
}

// The tool surface lists the ghost's verbs and refuses unknown ones in
// words.
func TestHermesToolList(t *testing.T) {
	g, _, _ := hermesGhost(t, 0)
	tools := hermesTools{g}
	names := map[string]bool{}
	for _, s := range tools.List() {
		names[s.Name] = true
		if !json.Valid(s.Schema) {
			t.Errorf("%s has an invalid schema", s.Name)
		}
	}
	for _, want := range []string{"say", "ask", "intend", "face", "draw", "look", "tell"} {
		if !names[want] {
			t.Errorf("%s is not offered", want)
		}
	}
	// Beliefs are hermes memory's, and a correction is an edit to it
	// (docs/hermes-brain.md, section 13).
	for _, not := range []string{"remember", "note", "move", "watch", "correct", "beliefs"} {
		if names[not] {
			t.Errorf("%s should not be offered", not)
		}
	}
	r := tools.Call(context.Background(), "nope", nil)
	if !r.IsError || !strings.HasPrefix(r.Text, "That was not allowed") {
		t.Errorf("unknown tool answered %+v", r)
	}
	r = tools.Call(context.Background(), "intend", json.RawMessage(`{"text":"water the plants","delay_ms":600000}`))
	if r.IsError || !strings.HasPrefix(r.Text, "hermes/") {
		t.Errorf("intend answered %+v", r)
	}
	r = tools.Call(context.Background(), "intentions", nil)
	if !strings.Contains(r.Text, "water the plants") {
		t.Errorf("intentions answered %+v", r)
	}
}

func mustGet(t *testing.T, g *Ghost, key string) string {
	t.Helper()
	v, _, err := g.store.Get(key)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func itoa(n int) string {
	b, _ := json.Marshal(n)
	return string(b)
}
