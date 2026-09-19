package ghost

import (
	"strings"

	"github.com/aaronjauregui/haunt/internal/config"
)

// Expression inference: dialogue arrives, the face follows. Hand-tagged
// emotion codes are what the old ecosystem used, and they cost the author
// nothing when the author writes every line — but when a model writes them
// there is no author present to tag anything, which is the whole reason
// this exists.
//
// A lexicon runs on scripted lines, and on a hermes reply when the model
// did not wear a face itself (its face tool). It is free and instant, so a
// ghost with no model at all still animates.
//
// A brain that sets an expression explicitly always wins: manual tags
// override inference, as the design has always said. That is enforced by
// ordering — inference runs before the brain's own expression lands.

// moodWords maps a pose name to the words that suggest it. Poses a shell
// does not declare are skipped, so this table can afford to be generous.
var moodWords = map[string][]string{
	"happy": {
		"haha", "lovely", "delight", "glad", "wonderful", "great", "thank",
		"congratulations", "excellent", "nice", "pleased", "welcome", "yay",
		"!",
	},
	"sad": {
		"sorry", "sad", "unfortunately", "afraid not", "alas", "miss you",
		"regret", "shame", "disappoint", "lonely", "sigh",
	},
	"surprised": {
		"really?", "what?", "oh!", "wow", "whoa", "no way", "goodness",
		"suddenly", "unexpected", "?!",
	},
	"asleep": {
		"goodnight", "good night", "sleep", "tired", "yawn", "bed", "dozing",
	},
	"thinking": {
		"hmm", "perhaps", "maybe", "i wonder", "possibly", "let me think",
		"suppose",
	},
}

// lexiconPose scores text against the mood table, returning the best pose
// the shell actually has. Empty when nothing suggests itself, which is the
// common case and correctly leaves the face alone.
func lexiconPose(text string, available []string) string {
	if len(available) == 0 {
		return ""
	}
	has := map[string]bool{}
	for _, p := range available {
		has[p] = true
	}
	lower := strings.ToLower(text)

	best, bestScore := "", 0
	for pose, words := range moodWords {
		if !has[pose] {
			continue
		}
		score := 0
		for _, w := range words {
			if strings.Contains(lower, w) {
				score++
			}
		}
		// Ties resolve by pose name so the same line always produces the
		// same face; a ghost whose expression flickered between runs would
		// read as broken rather than lively.
		if score > bestScore || (score == bestScore && score > 0 && pose < best) {
			best, bestScore = pose, score
		}
	}
	return best
}

// inferExpression sets the face to match a line of dialogue, returning
// the pose it put on, or "" when the face did not change.
func (g *Ghost) inferExpression(text string) string {
	if g.shell == nil {
		return ""
	}
	poses := g.shell.Poses()
	if len(poses) == 0 {
		return "" // a single-image shell has no face to arrange
	}
	if g.expressionMode == config.ExpressionOff {
		return ""
	}
	pose := lexiconPose(text, poses)
	if pose == "" || pose == g.shell.Pose() {
		return ""
	}
	g.setExpression(pose)
	return pose
}

// currentPose is the face the ghost is wearing, "" without a shell.
func (g *Ghost) currentPose() string {
	if g.shell == nil {
		return ""
	}
	return g.shell.Pose()
}

// The face at rest.
//
// Everything above this line runs on a line of dialogue: the face follows
// what is about to be said. Between sentences — nearly all of the time a
// companion is on screen — nothing moved it at all, so a ghost nobody had
// spoken to since login wore one face all day.
//
// The occasion for a change is something the driver knows and the brain
// does not: the shape of the working day (internal/session), which the
// brain already hears about as a `session` event and would otherwise only
// turn into words. So the driver *offers* — it marks the event MayPose —
// and the brain decides whether to wear anything and what. That division
// is deliberate: choosing a face with nothing to derive it from is closer
// to inventing character, which is the brain's job, and a driver that
// quietly rearranges a character's face is the same kind of overreach as
// one that quietly takes actions on its behalf.
//
// The offer costs nothing and so pays nothing: no words, no model call,
// nothing demanded of the person, and charging it against the curiosity
// allowance would be the same category error as charging a blink. What it
// does inherit is the other two idle rules.

// mayPose reports whether this is a moment the ghost's face may change for
// on its own account. The rules are the idle animation's, for the same
// reasons: a companion pulling faces over a game or a film is exactly the
// intrusion fullscreen means to exclude, and somebody who asked for less
// motion did not mean "except for the face".
//
// A brain setting an expression on a line it is saying is unaffected —
// that face arrives with words the person is already reading, and this
// gates only the changes nobody asked for.
func (g *Ghost) mayPose() bool {
	if g.reducedMotion {
		return false
	}
	if g.desktop != nil && g.desktop.Fullscreen() {
		return false
	}
	return true
}
