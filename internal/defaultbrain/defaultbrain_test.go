package defaultbrain

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/aaronjauregui/haunt/internal/proto"
)

// run drives the built-in brain over the given events and returns the
// intents it emitted. Deliberately through Run rather than OnEvent: the
// brain's real interface is a stream of JSON lines, and testing it that way
// is also a check that everything it emits survives the wire.
func run(t *testing.T, events ...proto.Event) []proto.Intent {
	t.Helper()
	var in bytes.Buffer
	enc := json.NewEncoder(&in)
	for _, e := range events {
		if err := enc.Encode(e); err != nil {
			t.Fatal(err)
		}
	}
	var out bytes.Buffer
	Run(&in, &out)

	var intents []proto.Intent
	for _, line := range strings.Split(strings.TrimSpace(out.String()), "\n") {
		if line == "" {
			continue
		}
		var i proto.Intent
		if err := json.Unmarshal([]byte(line), &i); err != nil {
			t.Fatalf("unmarshalling intent %q: %v", line, err)
		}
		if err := i.Validate(); err != nil {
			t.Errorf("brain emitted an intent the driver would drop: %q: %v", line, err)
		}
		intents = append(intents, i)
	}
	return intents
}

// base is a fixed instant for click timing: the bus stamps every event, so
// a brain can measure a double-click without a clock of its own — and a
// recorded session replays identically.
var base = time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC)

// only returns the intents of one type.
func only(intents []proto.Intent, typ proto.IntentType) []proto.Intent {
	var out []proto.Intent
	for _, i := range intents {
		if i.Type == typ {
			out = append(out, i)
		}
	}
	return out
}

func TestBootGreetsAndChats(t *testing.T) {
	intents := run(t,
		proto.Event{Type: proto.EventBoot},
		proto.Event{Type: proto.EventMessage, Text: "hello there"},
	)
	says := only(intents, proto.IntentSay)
	if len(says) == 0 {
		t.Fatalf("no greeting; intents = %+v", intents)
	}
	chats := only(intents, proto.IntentChat)
	if len(chats) != 1 || chats[0].Text != "hello there" {
		t.Errorf("chat intents = %+v, want the message passed through", chats)
	}
	if chats[0].Silent || chats[0].SelfPrompt {
		t.Errorf("ordinary conversation must be neither silent nor self-prompted: %+v", chats[0])
	}
}

// The driver draws the waiting indicator, but the words beside it are the
// character's — so the brain has to supply them.
func TestWaitingWordsRideAlongWithTheRequest(t *testing.T) {
	intents := run(t, proto.Event{Type: proto.EventMessage, Text: "hello there"})
	chats := only(intents, proto.IntentChat)
	if len(chats) != 1 || chats[0].Wait == "" {
		t.Errorf("chat = %+v, want something to say while the model thinks", chats)
	}

	intents = run(t,
		proto.Event{Type: proto.EventBoot, KV: map[string]string{
			promptKeyPrefix + "a sunset": "sunset, warm light",
		}},
		proto.Event{Type: proto.EventMessage, Text: "draw a sunset"},
		proto.Event{Type: proto.EventMessage, Text: "draw again"},
	)
	for _, i := range only(intents, proto.IntentImagine) {
		if i.Wait == "" {
			t.Errorf("imagine = %+v, want something to say while it paints", i)
		}
	}
	// And nothing said the old way — only the boot greeting. A plain
	// balloon would sit there for its hush timer and, being something to
	// read, would hold the indicator off for the whole generation.
	if says := only(intents, proto.IntentSay); len(says) != 1 {
		t.Errorf("drawing said %+v as ordinary balloons; only the greeting should be there", says)
	}
}

// A poke is answered locally: a click must not cost a language-model call,
// or clicking the ghost would be both slow and billable.
func TestClickIsAnsweredWithoutTheModel(t *testing.T) {
	intents := run(t,
		proto.Event{Type: proto.EventClick},
		proto.Event{Type: proto.EventClick},
	)
	if got := only(intents, proto.IntentChat); len(got) != 0 {
		t.Errorf("clicking asked the LLM: %+v", got)
	}
	says := only(intents, proto.IntentSay)
	if len(says) != 2 {
		t.Fatalf("say intents = %+v, want one per click", says)
	}
	if says[0].Text == says[1].Text {
		t.Errorf("both pokes answered %q; the rotation is not turning", says[0].Text)
	}
}

// Drawing is a chain: rewrite the request silently, paint the rewrite,
// remember the rewrite so asking again hits the driver's generation cache.
func TestDrawRewritesThenPaintsThenRemembers(t *testing.T) {
	intents := run(t,
		proto.Event{Type: proto.EventMessage, Text: "draw a sunset"},
	)
	asks := only(intents, proto.IntentChat)
	if len(asks) != 1 || !asks[0].Silent || asks[0].Tag == "" {
		t.Fatalf("expected one silent, tagged rewrite request; got %+v", asks)
	}
	if !strings.Contains(asks[0].Text, "a sunset") {
		t.Errorf("rewrite request lost the subject: %q", asks[0].Text)
	}
	if got := only(intents, proto.IntentImagine); len(got) != 0 {
		t.Errorf("painted before the rewrite came back: %+v", got)
	}

	intents = run(t,
		proto.Event{Type: proto.EventMessage, Text: "draw a sunset"},
		proto.Event{Type: proto.EventAIReply, Tag: rewriteTag, Text: `"sunset, wide shot, warm light"`},
	)
	imagine := only(intents, proto.IntentImagine)
	if len(imagine) != 1 {
		t.Fatalf("imagine intents = %+v, want one", imagine)
	}
	// The quoting a chatty model wraps its answer in is stripped, because
	// the quotes would otherwise become part of the image prompt.
	if imagine[0].Text != "sunset, wide shot, warm light" {
		t.Errorf("painted %q, want the unwrapped rewrite", imagine[0].Text)
	}
	if imagine[0].Fresh {
		t.Error("a first request must be cacheable")
	}
	kv := only(intents, proto.IntentKVSet)
	if len(kv) != 1 || kv[0].Name != promptKeyPrefix+"a sunset" || kv[0].Value != imagine[0].Text {
		t.Errorf("kv_set = %+v, want the rewrite remembered under the brain's prefix", kv)
	}
	if !strings.HasPrefix(kv[0].Name, KeyPrefix) {
		t.Errorf("built-in brain wrote outside its own prefix: %q", kv[0].Name)
	}
}

// The remembered rewrite is what makes a repeat request free: it is
// replayed rather than re-sampled, so the driver's cache is hit.
func TestRepeatedRequestReusesTheRememberedPrompt(t *testing.T) {
	intents := run(t,
		proto.Event{Type: proto.EventBoot, KV: map[string]string{
			promptKeyPrefix + "a sunset": "sunset, warm light",
		}},
		proto.Event{Type: proto.EventMessage, Text: "draw a sunset"},
	)
	if got := only(intents, proto.IntentChat); len(got) != 0 {
		t.Errorf("re-asked the model for a rewrite it already had: %+v", got)
	}
	imagine := only(intents, proto.IntentImagine)
	if len(imagine) != 1 || imagine[0].Text != "sunset, warm light" || imagine[0].Fresh {
		t.Errorf("imagine = %+v, want the remembered prompt, cacheable", imagine)
	}
}

func TestDrawAgainRerollsPastTheCache(t *testing.T) {
	intents := run(t,
		proto.Event{Type: proto.EventBoot, KV: map[string]string{
			promptKeyPrefix + "a sunset": "sunset, warm light",
		}},
		proto.Event{Type: proto.EventMessage, Text: "draw a sunset"},
		proto.Event{Type: proto.EventMessage, Text: "draw again"},
	)
	imagine := only(intents, proto.IntentImagine)
	if len(imagine) != 2 {
		t.Fatalf("imagine intents = %+v, want two", imagine)
	}
	if !imagine[1].Fresh || imagine[1].Text != imagine[0].Text {
		t.Errorf("reroll = %+v, want the same prompt with the cache bypassed", imagine[1])
	}

	// Nothing to reroll yet: say so instead of painting nothing.
	intents = run(t, proto.Event{Type: proto.EventMessage, Text: "draw again"})
	if got := only(intents, proto.IntentImagine); len(got) != 0 {
		t.Errorf("rerolled with no previous prompt: %+v", got)
	}
	if got := only(intents, proto.IntentSay); len(got) == 0 {
		t.Error("rerolling with nothing to reroll said nothing")
	}
}

// A failed rewrite still paints: the rewrite is a nicety, and the user's
// own words are a serviceable prompt.
func TestRewriteFailureFallsBackToTheUsersWords(t *testing.T) {
	intents := run(t,
		proto.Event{Type: proto.EventMessage, Text: "draw a whale"},
		proto.Event{Type: proto.EventAIError, Tag: rewriteTag, Text: "backend down"},
	)
	imagine := only(intents, proto.IntentImagine)
	if len(imagine) != 1 || imagine[0].Text != "a whale" {
		t.Errorf("imagine = %+v, want the request painted verbatim", imagine)
	}
}

func TestRememberJournalsWithoutAskingTheModel(t *testing.T) {
	intents := run(t, proto.Event{Type: proto.EventMessage, Text: "remember I like tea"})
	notes := only(intents, proto.IntentNote)
	if len(notes) != 1 || notes[0].Text != "I like tea" {
		t.Errorf("note intents = %+v", notes)
	}
	if got := only(intents, proto.IntentChat); len(got) != 0 {
		t.Errorf("remembering went to the model: %+v", got)
	}
}

// Speaking first must go through Initiate, not Chat: a stage direction sent
// as a chat is recorded as an utterance of the user's, and recall serves it
// back months later as something they said.
func TestIdleSpeaksFirstAsItselfAndUsesWhatItSaw(t *testing.T) {
	intents := run(t,
		proto.Event{Type: proto.EventWindow, Name: "firefox", Text: "haunt — issue 12"},
		proto.Event{Type: proto.EventIdle},
	)
	chats := only(intents, proto.IntentChat)
	if len(chats) != 1 || !chats[0].SelfPrompt {
		t.Fatalf("idle chats = %+v, want one self-prompted", chats)
	}
	if !strings.Contains(chats[0].Text, "firefox") {
		t.Errorf("the direction ignored what they were looking at: %q", chats[0].Text)
	}
	if got := only(intents, proto.IntentSay); len(got) != 0 {
		t.Errorf("idle said something directly rather than through the model: %+v", got)
	}
}

// A cron trigger is scheduled dialogue an author gets without writing any.
func TestCronTriggerSpeaksToTheOccasion(t *testing.T) {
	intents := run(t, proto.Event{Type: proto.EventCron, Name: "morning"})
	chats := only(intents, proto.IntentChat)
	if len(chats) != 1 || !chats[0].SelfPrompt || !strings.Contains(chats[0].Text, "morning") {
		t.Errorf("cron chats = %+v, want a self-prompted line about the occasion", chats)
	}
}

// Looking is deliberate and rationed, so what comes back is written down
// rather than narrated: "I see you have a terminal open" is the tiresome
// version of this feature.
func TestLookIsJournalledNotAnnounced(t *testing.T) {
	intents := run(t,
		proto.Event{Type: proto.EventMessage, Text: "look"},
		proto.Event{Type: proto.EventAILook, Name: "kitty", Text: "reading a stack trace"},
	)
	if got := only(intents, proto.IntentLook); len(got) != 1 {
		t.Fatalf("look intents = %+v, want one", got)
	}
	notes := only(intents, proto.IntentNote)
	if len(notes) != 1 || !strings.Contains(notes[0].Text, "reading a stack trace") {
		t.Fatalf("note intents = %+v, want what the model saw", notes)
	}
	for _, s := range only(intents, proto.IntentSay) {
		if strings.Contains(s.Text, "stack trace") {
			t.Errorf("the ghost narrated what it saw: %q", s.Text)
		}
	}
}

// The user cannot read the source of a brain nobody wrote, so it explains
// itself — as a menu, since a person who has just met a ghost is more
// likely to click it than to know the word "help".
func TestHelpOffersAMenu(t *testing.T) {
	intents := run(t, proto.Event{Type: proto.EventMessage, Text: "Help"})
	says := only(intents, proto.IntentSay)
	if len(says) != 1 || len(says[0].Choices) < 2 {
		t.Fatalf("help said %+v, want a question with options", says)
	}
	if says[0].Tag == "" {
		t.Error("the menu carries no tag, so its answer cannot be told apart")
	}
	if got := only(intents, proto.IntentChat); len(got) != 0 {
		t.Errorf("help went to the model: %+v", got)
	}

	// And picking from it does what it says.
	intents = run(t,
		proto.Event{Type: proto.EventMessage, Text: "help"},
		proto.Event{Type: proto.EventChoice, Name: menuHelp, Tag: menuTag},
	)
	var explained bool
	for _, s := range only(intents, proto.IntentSay) {
		if strings.Contains(s.Text, "draw") {
			explained = true
		}
	}
	if !explained {
		t.Errorf("choosing %q explained nothing: %+v", menuHelp, intents)
	}
}

// Dismissing a menu is an answer, not an error: the brain must do nothing
// and certainly must not keep asking.
func TestDismissedMenuIsLetGo(t *testing.T) {
	intents := run(t,
		proto.Event{Type: proto.EventMessage, Text: "help"},
		proto.Event{Type: proto.EventChoice, Tag: menuTag}, // no name: dismissed
	)
	// One say: the menu itself. Nothing after it.
	if says := only(intents, proto.IntentSay); len(says) != 1 {
		t.Errorf("a dismissal produced %+v", says)
	}
}

// Two pokes in quick succession are a request for the menu; pokes on their
// own are just pokes. A counter used to do this job and was unpredictable
// in the hand — the click that waves a menu away never reaches the brain,
// so the rhythm drifted. A double-click is a gesture someone performs on
// purpose and can repeat at will.
func TestDoubleClickAsksForTheMenu(t *testing.T) {
	at := func(ms int) proto.Event {
		return proto.Event{Type: proto.EventClick, Time: base.Add(time.Duration(ms) * time.Millisecond)}
	}
	tally := func(intents []proto.Intent) (menus, quips int) {
		for _, in := range only(intents, proto.IntentSay) {
			if len(in.Choices) > 0 {
				menus++
			} else {
				quips++
			}
		}
		return menus, quips
	}

	// Slow pokes are pokes.
	if m, q := tally(run(t, at(0), at(2000), at(4000))); m != 0 || q != 3 {
		t.Errorf("three slow pokes gave %d menus and %d quips", m, q)
	}

	// Two close together are a question.
	if m, _ := tally(run(t, at(0), at(200))); m != 1 {
		t.Errorf("a double-click gave %d menus", m)
	}

	// A third click starts a fresh pair rather than re-opening the menu
	// that is already up.
	if m, _ := tally(run(t, at(0), at(150), at(300))); m != 1 {
		t.Errorf("a triple click gave %d menus, want 1", m)
	}

	// And it works again afterwards, however the last one ended — the part
	// that made the menu feel like a one-off.
	intents := run(t,
		at(0), at(150), // menu
		proto.Event{Type: proto.EventChoice, Tag: menuTag}, // waved away
		at(3000), at(3150), // menu again
		proto.Event{Type: proto.EventChoice, Name: menuNothing, Tag: menuTag},
		at(6000), at(6150), // and again
	)
	if m, _ := tally(intents); m != 3 {
		t.Errorf("three double-clicks over a session gave %d menus", m)
	}
}

// A gesture nobody mentions is a gesture nobody performs, so the ghost
// introduces itself — once.
func TestFirstMeetingExplainsTheGesture(t *testing.T) {
	first := run(t, proto.Event{Type: proto.EventBoot})
	says := only(first, proto.IntentSay)
	if len(says) != 1 || !strings.Contains(says[0].Text, "ouble-click") {
		t.Errorf("first greeting = %+v, want the gesture mentioned", says)
	}
	var remembered string
	for _, in := range only(first, proto.IntentKVSet) {
		if in.Name == metKey {
			remembered = in.Value
		}
	}
	if remembered == "" {
		t.Fatal("the ghost did not remember having introduced itself")
	}

	// Next time it just says hello.
	again := run(t, proto.Event{Type: proto.EventBoot, KV: map[string]string{metKey: remembered}})
	says = only(again, proto.IntentSay)
	if len(says) != 1 || strings.Contains(says[0].Text, "ouble-click") {
		t.Errorf("second greeting = %+v, want no lecture", says)
	}
}

// Lines carry their own lifetime now, so the driver can time them from when
// they appear. A brain-side timer could not: it does not know how long a
// model will take, which is how a slow reply used to be erased by a hush
// scheduled beside a quip half a minute earlier.
func TestLinesCarryTheirOwnLifetime(t *testing.T) {
	intents := run(t,
		proto.Event{Type: proto.EventBoot},
		proto.Event{Type: proto.EventClick},
		proto.Event{Type: proto.EventMessage, Text: "hello"},
	)
	for _, in := range intents {
		if in.Type == proto.IntentSetTimer {
			t.Errorf("the brain is still scheduling its own hide: %+v", in)
		}
	}
	for _, in := range only(intents, proto.IntentSay) {
		if len(in.Choices) == 0 && in.LingerMS == 0 {
			t.Errorf("line %q stays up forever", in.Text)
		}
	}
	chats := only(intents, proto.IntentChat)
	if len(chats) != 1 || chats[0].LingerMS == 0 {
		t.Errorf("chat = %+v, want the reply's lifetime riding along", chats)
	}
}

// The shape of the day is what initiative should be spent on: a remark
// lands because it is about something, where a timer against idleness is a
// party trick. The brain only tries — the driver decides whether it may.
func TestSessionFactsProvokeARemark(t *testing.T) {
	intents := run(t, proto.Event{
		Type: proto.EventSession,
		Name: "dwell",
		Text: "42 minutes in nvim (session.go)",
		Session: &proto.SessionFact{
			Kind: "dwell", App: "nvim", Title: "session.go", Minutes: 42,
			Summary: "42 minutes in nvim (session.go)",
		},
	})
	chats := only(intents, proto.IntentChat)
	if len(chats) != 1 || !chats[0].SelfPrompt {
		t.Fatalf("session chats = %+v, want one self-prompted", chats)
	}
	if !strings.Contains(chats[0].Text, "42 minutes in nvim") {
		t.Errorf("the direction did not carry what was noticed: %q", chats[0].Text)
	}
	// Unprompted, so it must not be recorded as something the user said —
	// that is what SelfPrompt is for, and why Initiate exists.
	if got := only(intents, proto.IntentSay); len(got) != 0 {
		t.Errorf("it announced the observation directly: %+v", got)
	}
}

// And what it noticed outlasts the moment: when the ghost later speaks
// first, "forty minutes on one file" is a better thing to go on than the
// last window title.
func TestWhatWasNoticedFeedsSpeakingFirst(t *testing.T) {
	intents := run(t,
		proto.Event{Type: proto.EventWindow, Name: "firefox", Text: "a wiki page"},
		proto.Event{Type: proto.EventSession, Name: "recurrence", Text: "back at firefox (build failed) for the 3rd time in the last hour",
			Session: &proto.SessionFact{Kind: "recurrence", App: "firefox", Title: "build failed", Count: 3,
				Summary: "back at firefox (build failed) for the 3rd time in the last hour"}},
		proto.Event{Type: proto.EventIdle},
	)
	chats := only(intents, proto.IntentChat)
	if len(chats) != 2 {
		t.Fatalf("chats = %d, want one for the observation and one for going idle", len(chats))
	}
	idle := chats[1]
	if !strings.Contains(idle.Text, "3rd time") {
		t.Errorf("speaking first ignored what had been noticed: %q", idle.Text)
	}
}

// TestMenuOffersToWatchUntilItIs: window awareness is the one capability
// with no moment of use to ask at — its consumer is a background poller —
// so the menu entry *is* the question. It has to be there when nothing is
// being watched, and gone once something is, or it is an option that does
// nothing.
func TestMenuOffersToWatchUntilItIs(t *testing.T) {
	menu := func(intents []proto.Intent) []string {
		for _, i := range only(intents, proto.IntentSay) {
			if len(i.Choices) > 0 {
				return i.Choices
			}
		}
		return nil
	}
	doubleClick := []proto.Event{
		{Type: proto.EventClick, Time: base},
		{Type: proto.EventClick, Time: base.Add(200 * time.Millisecond)},
	}

	options := menu(run(t, doubleClick...))
	if !contains(options, menuWatch) {
		t.Errorf("the menu does not offer to watch: %v", options)
	}

	// Once window events are arriving, the offer is redundant.
	watched := append([]proto.Event{
		{Type: proto.EventWindow, Time: base, Name: "nvim", Text: "session.go"},
	}, doubleClick...)
	if options := menu(run(t, watched...)); contains(options, menuWatch) {
		t.Errorf("still offering to start something already running: %v", options)
	}
}

// TestPickingWatchAsksTheDriver: the brain emits `watch` and nothing else —
// there is no answer to wait for, and whether the question is even put is
// the driver's decision, not the brain's.
func TestPickingWatchAsksTheDriver(t *testing.T) {
	intents := run(t, proto.Event{
		Type: proto.EventChoice, Time: base, Tag: menuTag, Name: menuWatch,
	})
	if n := len(only(intents, proto.IntentWatch)); n != 1 {
		t.Fatalf("emitted %d watch intents, want 1: %+v", n, intents)
	}
	// Notably not a chat: asking to be watched is not a conversation.
	if n := len(only(intents, proto.IntentChat)); n != 0 {
		t.Errorf("billed a model for a permission request: %+v", intents)
	}
}

func contains(ss []string, want string) bool {
	for _, s := range ss {
		if s == want {
			return true
		}
	}
	return false
}

// A ghost that can do things has to be able to say so. Nobody wrote this
// brain, so there is no source for a user to read and no author to ask —
// what it can do has to be discoverable from the ghost itself.
func TestHelpNamesTheToolsItHas(t *testing.T) {
	intents := run(t,
		proto.Event{Type: proto.EventBoot, Tools: []string{"notes.append", "notes.search"}},
		proto.Event{Type: proto.EventMessage, Text: "help"},
		proto.Event{Type: proto.EventChoice, Name: menuHelp, Tag: menuTag},
	)
	var explained string
	for _, s := range only(intents, proto.IntentSay) {
		if strings.Contains(s.Text, "draw") {
			explained = s.Text
		}
	}
	if explained == "" {
		t.Fatalf("nothing explained the ghost: %+v", intents)
	}
	for _, want := range []string{"notes.append", "notes.search"} {
		if !strings.Contains(explained, want) {
			t.Errorf("help does not mention %q:\n%s", want, explained)
		}
	}
}

// And a ghost with none says nothing about tools, rather than an empty
// promise.
func TestHelpSaysNothingAboutToolsItLacks(t *testing.T) {
	intents := run(t,
		proto.Event{Type: proto.EventBoot},
		proto.Event{Type: proto.EventMessage, Text: "help"},
		proto.Event{Type: proto.EventChoice, Name: menuHelp, Tag: menuTag},
	)
	for _, s := range only(intents, proto.IntentSay) {
		if strings.Contains(s.Text, "I can also do things") {
			t.Errorf("a ghost with no tools offered to do things:\n%s", s.Text)
		}
	}
}
