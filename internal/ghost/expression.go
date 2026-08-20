package ghost

import (
	"context"
	"fmt"
	"strings"

	"github.com/aaronjauregui/haunt/internal/ai"
	"github.com/aaronjauregui/haunt/internal/hlog"
)

// Expression inference: dialogue arrives, the face follows. Hand-tagged
// emotion codes are what the old ecosystem used, and they cost the author
// nothing when the author writes every line — but when a model writes them
// there is no author present to tag anything, which is the whole reason
// this exists.
//
// Two tiers, because they have different costs:
//
//   - A lexicon runs on every line, scripted or generated. It is free and
//     instant, so a ghost with no LLM at all still animates, and it is the
//     fallback whenever the model declines or fails.
//   - In "llm" mode the model classifies generated lines into the shell's
//     poses and its answer wins. Understanding what a line means is the
//     thing a model is for; a word table is a stand-in for it, not a
//     supervisor of it.
//
// The classifier is allowed to answer "none", because most dialogue has no
// particular mood and the right response to that is to leave the face
// alone. A model too small to use that option well is a reason to leave
// the mode at its "lexicon" default, not a reason to bend the design
// around it.
//
// A brain that sets an expression explicitly always wins: manual tags
// override inference, as the design has always said. That is enforced by
// ordering — inference runs before the brain sees the reply event, so
// anything the brain does lands last.

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

// inferExpression sets the face to match a line of dialogue. generated says
// whether a model wrote it, which is what earns the extra classifying call.
func (g *Ghost) inferExpression(text string, generated bool) {
	if g.shell == nil {
		return
	}
	poses := g.shell.Poses()
	if len(poses) == 0 {
		return // a single-image shell has no face to arrange
	}

	mode := ai.ExpressionLexicon
	if g.aiSvc != nil {
		mode = g.aiSvc.ExpressionMode()
	}
	if mode == ai.ExpressionOff {
		return
	}

	// In llm mode the model has the say on generated lines; the lexicon
	// answers for scripted ones, and for anything the model declined or
	// could not be asked about.
	pose := ""
	if generated && mode == ai.ExpressionLLM {
		pose = g.classifyExpression(text, poses)
	}
	if pose == "" {
		pose = lexiconPose(text, poses)
	}
	if pose == "" || pose == g.shell.Pose() {
		return
	}
	g.setExpression(pose)
}

// classifyExpression asks the model which of the shell's poses fits a line.
// Silent, so the classification never reaches the balloon or the
// conversation; failures return empty and leave the lexicon's answer alone,
// because a ghost whose face stops working when a backend hiccups is worse
// than one whose face is occasionally wrong.
func (g *Ghost) classifyExpression(text string, poses []string) string {
	if g.aiSvc == nil || !g.aiSvc.LLMEnabled() || !g.capabilityActive("llm") {
		return ""
	}
	// "none" is offered because most dialogue carries no particular mood,
	// and a classifier with no way to say so will invent one — labelling
	// "the file is in that directory" as sad.
	prompt := fmt.Sprintf(
		"Which single word from this list best fits the mood of the line below?\n"+
			"List: %s\nLine: %s\n\nAnswer with one word from the list, "+
			"or \"none\" if the line carries no particular mood. Nothing else.",
		strings.Join(poses, ", "), text)

	reply, err := g.aiSvc.Chat(context.Background(), []ai.Message{
		{Role: "system", Content: "You label the mood of a line of dialogue. Answer with exactly one word."},
		{Role: "user", Content: prompt},
	}, nil, ai.Options{Silent: true, Tag: "expression"})
	if err != nil {
		g.log.Warn(hlog.Anim, "expression inference: %v", err)
		return ""
	}

	// Small models pad, quote and explain; take the first declared pose
	// that appears in the answer.
	lower := strings.ToLower(reply)
	if strings.Contains(lower, "none") {
		return ""
	}
	for _, p := range poses {
		if strings.Contains(lower, strings.ToLower(p)) {
			return p
		}
	}
	g.log.Debug(hlog.Anim, "expression inference: %q names none of %v", strings.TrimSpace(reply), poses)
	return ""
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
// brain already hears about as a `session` event and until now could only
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
