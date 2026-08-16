package defaultbrain

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

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
// itself when asked.
func TestHelpListsWhatItAnswersTo(t *testing.T) {
	intents := run(t, proto.Event{Type: proto.EventMessage, Text: "Help"})
	says := only(intents, proto.IntentSay)
	if len(says) != 1 || !strings.Contains(says[0].Text, "draw") {
		t.Errorf("help said %+v", says)
	}
	if got := only(intents, proto.IntentChat); len(got) != 0 {
		t.Errorf("help went to the model: %+v", got)
	}
}
