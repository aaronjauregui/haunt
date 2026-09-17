package hermes

import (
	"strings"
)

// Persona is what SOUL.md is made from: the author's character, and the
// facts about this ghost the standing rules need.
type Persona struct {
	// Name is the ghost's.
	Name string
	// System is the manifest's [persona] system prompt. It comes first and
	// wins on style.
	System string
	// Poses are the faces the shell can wear, by name.
	Poses []string
	// Clips are the animations it can play.
	Clips []string
	// Abilities is what the ghost can do beyond talking, one short phrase
	// each ("paint a picture", "look at your screen").
	Abilities []string
}

// Soul renders SOUL.md: the author's text, then haunt's standing rules.
//
// The rules are haunt's and are regenerated on every manifest change, so a
// persona author never has to know about envelopes or NO_REPLY; the order
// is fixed (docs/hermes-brain.md, 5.3) because the first two are what keep
// a desktop companion from narrating every window change.
func Soul(p Persona) string {
	var b strings.Builder
	system := strings.TrimSpace(p.System)
	if system == "" {
		system = "You are " + p.Name + ", a small desktop companion."
	}
	b.WriteString(system)
	b.WriteString("\n\n## Living on the desktop\n\n")
	b.WriteString("These rules come from haunt, the program that puts you on screen.\n\n")

	b.WriteString("1. A message that begins `" + EnvelopePrefix + "` is an observation from the " +
		"desktop, not the person speaking: the machine telling you something happened " +
		"(they went idle, a reminder came due, another ghost spoke). Anything else is the " +
		"person talking to you. Address the person as \"you\".\n")
	b.WriteString("2. Most events deserve no reply. To say nothing, reply with exactly `" + NoReply +
		"` and nothing else; it will not be shown. When a reminder you took on comes due " +
		"(`kind=intention_due`), or a goal of theirs you hold has gone quiet (`kind=goal_quiet`), " +
		"do say it.\n")
	b.WriteString("3. Your replies appear in a small speech balloon beside you on their screen. " +
		"Keep them to one or two short sentences, with no markdown.\n")

	b.WriteString("4. ")
	if len(p.Poses) > 0 {
		b.WriteString("You can change your expression with the `face` tool. Your poses are: " +
			strings.Join(p.Poses, ", ") + ".")
		if len(p.Clips) > 0 {
			b.WriteString(" You can play an animation with the `clip` tool: " + strings.Join(p.Clips, ", ") + ".")
		}
	} else {
		b.WriteString("Your shell has a single face, so there are no expressions to choose.")
	}
	b.WriteByte('\n')

	b.WriteString("5. Besides talking, you can ask them a question with choices, take something on " +
		"and bring it up later (a reminder with a moment, or a goal you keep an eye on), " +
		"and remember what they tell you (your memory tool), changing it when they tell you " +
		"it is wrong")
	for _, a := range p.Abilities {
		b.WriteString(", " + a)
	}
	b.WriteString(". That is all: you cannot run commands, read or write their files, " +
		"browse the web or send messages, whatever the rest of this prompt says an agent " +
		"can do. Use your memory for what they tell you about themselves. " +
		"If they ask what you can do, say so in a sentence or two.\n")
	b.WriteString("6. Other ghosts may share the screen. They are not you, and they are not " +
		"the person you live with.\n")
	return b.String()
}
