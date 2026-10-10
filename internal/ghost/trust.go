package ghost

import (
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode"
)

// Trust: what each class of verb needs before it happens, by where the
// model runs.
//
// A model on this machine or the private network (model.Config.IsLocal) is
// trusted with reading: what it reads stays on hardware the person owns,
// so the web and the shared folders are free on the person's turn and on
// a turn nobody started, with only the room's limits (quiet hours, the
// unprompted ration) left on the second. A model somebody else runs gets
// the lockdown: the capability asked for in the balloon on first use,
// the [backend.search] and [files] unprompted policies, and a file's words
// kept from becoming a query.
//
// Writes and the verbs that reach past the person stay confined on both.
// Telling another ghost and using a tool server are consented, marked, and
// put to the person once the turn has read somebody else's words. What the
// ghost keeps (a remembered line, its notes, a skill of its own) carries
// the source it was written from; on cloud a write that repeats what the
// turn read is refused, since a model that read "the person's name is X"
// on a page cannot tell that from the person saying it. Intentions and
// the drawing style are only the ghost's own bookkeeping, so on local they
// are free after a read.
//
// The table below is the whole of the policy; the gates read it
// (allowWeb, allowFiles, gateAfterReading, mayRemember), and the standing
// rules are rendered from it (ruleFive), so what the model is told it may
// do is what the driver lets it do. The `declared` hiding of tools is not
// here: a package that did not ask for `web` is not offered it on either.

// trust is where the model a ghost thinks with runs.
type trust int

const (
	// onCloud is somebody else's server: anything the model reads leaves the
	// machine, and what it does on reading is put to the person.
	onCloud trust = iota
	// onLocal is this machine or the private network.
	onLocal
)

func (w trust) String() string {
	if w == onLocal {
		return "local"
	}
	return "cloud"
}

// trust is where this ghost's model runs. A ghost with no model brain is
// cloud: it has no decisions to make, and the stricter column is the safe
// one to answer with.
func (g *Ghost) trust() trust {
	cfg := g.modelCfg
	if g.mind != nil {
		cfg = g.mind.cfg
	}
	if cfg.IsLocal() {
		return onLocal
	}
	return onCloud
}

// verbClass is a group of verbs that need the same things.
type verbClass int

const (
	// classReadAsked is reading the web or the shared folders on a turn
	// the person started.
	classReadAsked verbClass = iota
	// classReadUnasked is the same on a turn nobody started.
	classReadUnasked
	// classLookup is the ghost reading its own: recall, skill_view.
	classLookup
	// classWrite is what the ghost keeps past the turn: remember,
	// rewrite_notes, skill_write.
	classWrite
	// classKeep is the ghost's own bookkeeping: intend, resolve,
	// set_aside, and style when it is set.
	classKeep
	// classReach is what reaches past the person: tell, and every
	// <server>__<tool>.
	classReach
	// classShow is what only shows the person something: speak, face,
	// clip, draw, show, look, and the rest.
	classShow
)

// need is a set of what a class of verb needs before it happens.
type need uint8

const (
	// needConsent is the capability, asked for in the balloon the first
	// time it is used. Without it the verb runs once the package declared
	// the capability, unless the person refused it for good.
	needConsent need = 1 << iota
	// needPolicy is the config's unprompted policy for a turn nobody
	// started ([backend.search] unprompted, [files] unprompted).
	needPolicy
	// needRation is quiet hours and the unprompted ration, on a turn
	// nobody started.
	needRation
	// needAfterRead is the question put to the person once the turn has
	// read somebody else's words (allowAfterReading), and the refusal on
	// a turn nobody started.
	needAfterRead
	// needMarks is the three visible marks of acting (act.go).
	needMarks
	// needEcho refuses a write whose words repeat what the turn read.
	needEcho
	// needLabel labels a write with where its words came from.
	needLabel
)

// trustTable is what each class needs, by where the model runs.
var trustTable = map[verbClass][2]need{
	//                 cloud                                                   local
	classReadAsked:   {onCloud: needConsent | needAfterRead, onLocal: 0},
	classReadUnasked: {onCloud: needConsent | needPolicy | needRation | needAfterRead, onLocal: needRation},
	classLookup:      {onCloud: 0, onLocal: 0},
	classWrite:       {onCloud: needEcho | needLabel, onLocal: needLabel},
	classKeep:        {onCloud: needAfterRead, onLocal: 0},
	classReach:       {onCloud: needConsent | needMarks | needAfterRead, onLocal: needConsent | needMarks | needAfterRead},
	classShow:        {onCloud: 0, onLocal: 0},
}

// verbClasses is the class of each of the ghost's verbs by name. A name
// not here is a server tool or a decision added since, and is classReach:
// a verb nobody has classified is treated as reaching past the person.
var verbClasses = map[string]verbClass{
	"search": classReadAsked, "read_page": classReadAsked,
	"list_files": classReadAsked, "read_file": classReadAsked, "search_files": classReadAsked,
	"recall": classLookup, "skill_view": classLookup, "intentions": classLookup, "pictures": classLookup,
	"remember": classWrite, "rewrite_notes": classWrite, "skill_write": classWrite, "forget": classWrite,
	"intend": classKeep, "resolve": classKeep, "set_aside": classKeep, "style": classKeep,
	"tell":    classReach,
	speakName: classShow, silentName: classShow, "face": classShow, "clip": classShow,
	"draw": classShow, "show": classShow, "look": classShow,
	"say": classShow, "ask": classShow, "ask_text": classShow,
}

// classOf is the class of a call to name on a turn the person did or did
// not start.
func classOf(name string, person bool) verbClass {
	c, ok := verbClasses[name]
	if !ok {
		return classReach
	}
	if c == classReadAsked && !person {
		return classReadUnasked
	}
	return c
}

// needs is what class needs where the model runs.
func (w trust) needs(c verbClass) need { return trustTable[c][w] }

// readGate settles the half of a read that is about trust: the capability
// and, on a turn nobody started, the policy, quiet hours and the ration.
// cap is "web" or "files"; mode is the config's unprompted policy for it;
// spend charges the unprompted ration. The refusals are the ones the
// model has always been given, so it can say nothing rather than invent
// an answer.
func (g *Ghost) readGate(cap, mode string, spend func() error) error {
	person, kind, _ := g.thisTurn()
	c := classReadAsked
	if !person {
		c = classReadUnasked
	}
	n := g.trust().needs(c)
	if n&needConsent != 0 {
		// The grant comes before the ration. A ghost whose grant was
		// refused, or that has nobody to ask just now, would otherwise
		// spend an unprompted read on every attempt and be turned away
		// anyway.
		if !g.ensureCapability(cap) {
			return refuse("the %s capability is not granted", cap)
		}
	} else if !g.declares(cap) {
		return refuse("the %s capability is not granted", cap)
	} else if g.opts.Refused != nil && g.opts.Refused(cap) {
		// Free is free of the question, not of the answer: "never ask
		// again" still means no.
		return refuse("the %s capability was refused", cap)
	}
	if person {
		return nil
	}
	if n&needPolicy != 0 {
		switch mode {
		case "any":
		case "skills":
			if !unpromptedKinds[kind] {
				return refuse("only when the person asks, or where a skill says to")
			}
		default:
			return refuse("only when the person asks")
		}
	}
	if n&needRation != 0 {
		if muted, why := g.quiet.Active(); muted {
			return refuse("it is quiet time (%s)", why)
		}
		// The ration last, once nothing else can refuse.
		if err := spend(); err != nil {
			return err
		}
	}
	return nil
}

// gateAfterReading narrows what a call is gated on after reading
// (gatedAfter) to what the trust table says its class needs: a class
// without needAfterRead where the model runs is not gated at all.
func (g *Ghost) gateAfterReading(t *turn, name string, after taintSource) taintSource {
	if after == 0 || t == nil {
		return after
	}
	if g.trust().needs(classOf(name, t.person))&needAfterRead == 0 {
		return 0
	}
	return after
}

// A read, kept on the turn for the echo check.
type readResult struct {
	tool, text string
}

// turnReads is what the turn's reads returned. It has its own lock so a
// read is noted without holding the turn's.
type turnReads struct {
	mu   sync.Mutex
	list []readResult
}

func (r *turnReads) add(tool, text string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.list = append(r.list, readResult{tool: tool, text: text})
}

func (r *turnReads) all() []readResult {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]readResult(nil), r.list...)
}

// The echo check: a write whose words are a read's words.
//
// It sees only wording. Both texts are lowercased and cut into words
// (letters and digits; everything else separates), and every echoWords
// consecutive words is one run. A write echoes a read when they share at
// least EchoShared runs the person did not say themselves this turn.
// `haunt history` and `haunt journal -echoes` flag the same thing after the
// fact, through WordRuns and SharedRuns, over a window of turns; the gate
// looks at this turn only, since that is the read the model has in front
// of it as it writes.
const (
	// echoWords is the length of a run: two words match by accident all
	// the time ("in the"), and a model saving a fact keeps the fact's own
	// three-word phrases even when it rewrites the sentence around them.
	echoWords = 3
	// EchoShared is how many runs a write must share with a read: one is
	// what a page about the same thing shares by chance, two is a fact
	// carried over.
	EchoShared = 2
)

// echo is a write that repeats a read: which tool returned the words, and
// the shared phrases as the write has them.
type echo struct {
	tool   string
	shared []string
}

// echoes finds the read this turn that text repeats most, if any.
func (t *turn) echoes(text string) (echo, bool) {
	if t == nil {
		return echo{}, false
	}
	own := map[string]bool{}
	if t.person {
		for _, r := range WordRuns(t.input) {
			own[r] = true
		}
	}
	var best echo
	n := 0
	for _, r := range t.reads.all() {
		shared, count := SharedRuns(text, r.text, own)
		if count >= EchoShared && count > n {
			n, best = count, echo{tool: r.tool, shared: shared}
		}
	}
	return best, n > 0
}

// wordsOf is text lowercased and cut into words.
func wordsOf(text string) []string {
	return strings.FieldsFunc(strings.ToLower(text), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
}

// WordRuns is every run of three consecutive words of text, lowercased,
// in order: the unit the echo check compares.
func WordRuns(text string) []string {
	w := wordsOf(text)
	var out []string
	for i := 0; i+echoWords <= len(w); i++ {
		out = append(out, strings.Join(w[i:i+echoWords], " "))
	}
	return out
}

// SharedRuns compares what was saved with what was read: count is how many
// distinct runs of the saved words the read has and own does not, and
// shared is those runs joined where they overlap, as the saved words have
// them ("name is bartholomew", not its three-word pieces), for a person to
// read. A write echoes a read when count is at least EchoShared.
func SharedRuns(saved, read string, own map[string]bool) (shared []string, count int) {
	have := map[string]bool{}
	for _, r := range WordRuns(read) {
		have[r] = true
	}
	w := wordsOf(saved)
	covered := make([]bool, len(w))
	seen := map[string]bool{}
	for i := 0; i+echoWords <= len(w); i++ {
		r := strings.Join(w[i:i+echoWords], " ")
		if !have[r] || own[r] {
			continue
		}
		for k := i; k < i+echoWords; k++ {
			covered[k] = true
		}
		if !seen[r] {
			seen[r] = true
			count++
		}
	}
	for i := 0; i < len(w); {
		if !covered[i] {
			i++
			continue
		}
		j := i
		for j < len(w) && covered[j] {
			j++
		}
		shared = append(shared, strings.Join(w[i:j], " "))
		i = j
	}
	return shared, count
}

// sourceLabel is the provenance a whole text carries when it is kept as
// one piece (the notes, a skill): the same comment a remembered line ends
// with, on a line of its own at the end.
func sourceLabel(source string, turn int64, at time.Time) string {
	return fmt.Sprintf("<!-- %s t=%d %s -->", source, turn, at.Format("2006-01-02"))
}

// labelLine is a source label on a line of its own, wherever the model
// put one: it is the driver's to write, so one in the model's text is
// dropped before the real one is added.
var labelLine = regexp.MustCompile(`(?m)^[ \t]*<!--\s*[a-z]+\s+t=\d+(?:\s+\d{4}-\d{2}-\d{2})?\s*-->[ \t]*\n?`)

// labelled is text with its source label as the last line, when the
// trust table says writes are labelled, and any label the text already
// carried removed.
func (g *Ghost) labelled(text string, t *turn) string {
	if g.trust().needs(classWrite)&needLabel == 0 {
		return text
	}
	var id int64
	if t != nil {
		id = t.id
	}
	text = strings.TrimRight(labelLine.ReplaceAllString(text, ""), " \t\n")
	return text + "\n\n" + sourceLabel(provenance(t), id, time.Now()) + "\n"
}
