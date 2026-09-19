package hermes

import (
	"regexp"
	"strings"
)

// A reply may open with the pose the ghost wears as it says it:
//
//	[happy] Oh, it's you.
//
// The tag is the model choosing its face without a tool call. Calling the
// face tool costs a model round trip before the words, which on a slow
// model is most of the wait for a greeting; the tag rides on the words.
// The driver wears the pose and shows the rest. Any leading tag of this
// shape is taken off, a pose the shell has or not: a stage direction
// ("[sighs]") is no more for the balloon than a pose name is.

// poseTagMax is the longest name a tag may carry.
const poseTagMax = 32

var poseTagRe = regexp.MustCompile(`^\s*\[([A-Za-z0-9_-]{1,32})\][ \t]*\n?[ \t]*`)

// CutPose splits a leading pose tag off a reply: the name in it, and the
// reply without it. A reply with no tag comes back whole, with no pose.
func CutPose(reply string) (pose, rest string) {
	m := poseTagRe.FindStringSubmatchIndex(reply)
	if m == nil {
		return "", reply
	}
	return reply[m[2]:m[3]], reply[m[1]:]
}

// MaybePose reports whether a reply still arriving could yet open with a
// tag, so a stream can hold it back rather than flash the bracket on
// screen.
func MaybePose(partial string) bool {
	p := strings.TrimLeft(partial, " \t\n")
	if p == "" {
		return true
	}
	if p[0] != '[' {
		return false
	}
	name := p[1:]
	if len(name) > poseTagMax {
		return false
	}
	for _, r := range name {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '-') {
			// A closing bracket completes the tag; anything else is not one.
			return false
		}
	}
	return true
}
