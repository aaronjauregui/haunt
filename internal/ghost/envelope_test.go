package ghost

import "testing"

func TestEnvelope(t *testing.T) {
	cases := []struct {
		got, want string
	}{
		{Envelope("boot", F("first_time", true)), `[haunt event] kind=boot first_time=true`},
		{Envelope("idle", F("minutes", 12), F("last_window", "nvim: session.go")),
			`[haunt event] kind=idle minutes=12 last_window="nvim: session.go"`},
		{Envelope("told", F("from", "Moth"), F("text", "hello\nthere \"you\"")),
			`[haunt event] kind=told from=Moth text="hello there \"you\""`},
		{Envelope("help", F("empty", ""), F("kind", "sneaky")), `[haunt event] kind=help`},
		{Envelope("occasion", F("name", "morning")), `[haunt event] kind=occasion name=morning`},
	}
	for _, c := range cases {
		if c.got != c.want {
			t.Errorf("got  %s\nwant %s", c.got, c.want)
		}
		if !IsEnvelope(c.got) {
			t.Errorf("%s is not recognised as an envelope", c.got)
		}
	}
	if IsEnvelope("remind me to check the oven") {
		t.Error("the person's words were taken for an envelope")
	}
}

func TestEnvelopeKind(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{Envelope("idle", F("minutes", 5)), "idle"},
		{Envelope("session", F("shape", "dwell")), "session"},
		{Envelope("help"), "help"},
		{"what can you do?", ""},
		{"kind=idle", ""},
	} {
		if got := EnvelopeKind(tc.in); got != tc.want {
			t.Errorf("EnvelopeKind(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
