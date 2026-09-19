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
// The driver wears the pose and shows the rest.
//
// Any short bracketed opening is taken off, a pose or not. Told to open
// with a bracket, a model also opens with labels for what it did
// ("[memory add]", "[tell to moth]") or stage directions ("[first boot —
// face neutral]"), and none of those is for the balloon either.

// labelMax is the longest bracketed opening taken off a reply.
const labelMax = 80

var (
	labelRe = regexp.MustCompile(`^\s*\[([^\[\]\n]{1,80})\][ \t]*\n?[ \t]*`)
	poseRe  = regexp.MustCompile(`^[A-Za-z0-9_-]{1,32}$`)
)

// CutPose splits a leading bracketed opening off a reply: the pose it
// names, if it is a single name, and the reply without it. A reply with
// no such opening comes back whole, with no pose.
func CutPose(reply string) (pose, rest string) {
	m := labelRe.FindStringSubmatchIndex(reply)
	if m == nil {
		return "", reply
	}
	if name := strings.TrimSpace(reply[m[2]:m[3]]); poseRe.MatchString(name) {
		pose = name
	}
	return pose, reply[m[1]:]
}

// MaybePose reports whether a reply still arriving could yet open with a
// bracket that CutPose takes off, so a stream can hold it back rather
// than flash it on screen.
func MaybePose(partial string) bool {
	p := strings.TrimLeft(partial, " \t\n")
	if p == "" {
		return true
	}
	if p[0] != '[' {
		return false
	}
	inside := p[1:]
	return len(inside) <= labelMax && !strings.ContainsAny(inside, "[]\n")
}
