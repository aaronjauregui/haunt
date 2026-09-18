package hermes

import (
	"strconv"
	"strings"
	"unicode"
)

// EnvelopePrefix opens every line that is the desktop speaking rather than
// the person. SOUL.md tells the model what it means (see Soul), and the
// person's own words never carry it, so the two cannot be confused.
const EnvelopePrefix = "[haunt event]"

// Field is one key=value pair in an envelope. A slice rather than a map
// because the order is part of the format: kind first, then whatever the
// event names, in the order the driver wrote them, so two envelopes of one
// kind read alike and a test can compare them as strings.
type Field struct {
	Key   string
	Value string
}

// F is a Field, for callers building envelopes inline.
func F(key string, value any) Field {
	switch v := value.(type) {
	case string:
		return Field{key, v}
	case int:
		return Field{key, strconv.Itoa(v)}
	case bool:
		return Field{key, strconv.FormatBool(v)}
	default:
		return Field{key, ""}
	}
}

// Envelope renders one non-verbal event as the single line the model sees:
//
//	[haunt event] kind=idle minutes=12 last_window="nvim: session.go"
//
// No prose, no instruction, no shared preamble: what to make of an event is
// the standing rules' business (SOUL.md), and an envelope that also told
// the model what to do is how one event's instruction ends up answered in
// place of another's. Empty values are left out, since a key with nothing
// after it is noise the model has to reason about.
func Envelope(kind string, fields ...Field) string {
	var b strings.Builder
	b.WriteString(EnvelopePrefix)
	b.WriteString(" kind=")
	b.WriteString(bare(kind))
	for _, f := range fields {
		if f.Value == "" || f.Key == "" || f.Key == "kind" {
			continue
		}
		b.WriteByte(' ')
		b.WriteString(bare(f.Key))
		b.WriteByte('=')
		b.WriteString(value(f.Value))
	}
	return b.String()
}

// bare keeps a key or kind to the characters the format promises.
func bare(s string) string {
	return strings.Map(func(r rune) rune {
		if r == '_' || r == '-' || unicode.IsLetter(r) || unicode.IsDigit(r) {
			return r
		}
		return '_'
	}, s)
}

// value writes a value bare when it is one plain word and quoted otherwise.
// Newlines are folded to spaces first: the envelope is one line by
// definition, and a window title or a neighbour's remark can contain
// anything.
func value(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	plain := s != ""
	for _, r := range s {
		if !(r == '_' || r == '-' || r == '.' || r == ':' || r == '/' || unicode.IsLetter(r) || unicode.IsDigit(r)) {
			plain = false
			break
		}
	}
	if plain {
		return s
	}
	return strconv.Quote(s)
}

// IsEnvelope reports whether a turn's input was the desktop rather than the
// person, for the history and the why-trace.
func IsEnvelope(input string) bool {
	return strings.HasPrefix(input, EnvelopePrefix)
}

// EnvelopeKind is an envelope's kind ("idle", "session", …), or "" for
// input that is not an envelope.
func EnvelopeKind(input string) string {
	rest, ok := strings.CutPrefix(input, EnvelopePrefix+" kind=")
	if !ok {
		return ""
	}
	kind, _, _ := strings.Cut(rest, " ")
	return kind
}

// NoReply is what the model answers with to say nothing.
const NoReply = "NO_REPLY"

// silentMax is the longest reply that may still be read as silence when
// the token is at one end of it rather than the whole of it. Small models
// wrap the token ("NO_REPLY — nothing to add") often enough to matter, and
// a long reply that happens to end with it is a reply.
const silentMax = 300

// Silent reports whether a finished reply means "say nothing": the token
// alone, or at the start or end of a reply under 300 characters.
func Silent(reply string) bool {
	r := strings.TrimSpace(reply)
	if r == "" {
		return true
	}
	if r == NoReply {
		return true
	}
	if len(r) >= silentMax {
		return false
	}
	return strings.HasPrefix(r, NoReply) || strings.HasSuffix(strings.TrimRight(r, ".!"), NoReply)
}

// MaybeSilent reports whether a reply still arriving could yet turn out to
// be silence by its opening, so a stream can hold it back from the balloon
// rather than flash the token on screen.
func MaybeSilent(partial string) bool {
	p := strings.TrimSpace(partial)
	if len(p) <= len(NoReply) {
		return strings.HasPrefix(NoReply, p)
	}
	return strings.HasPrefix(p, NoReply)
}
