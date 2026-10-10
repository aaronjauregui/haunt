package hermes

import (
	"encoding/json"
	"slices"
	"strings"
)

// memoryWrites names the tools that write a ghost's memory, with the
// actions that write; nil means every call does. Memory is hermes's, so
// the store is whichever the person chose: hermes's own `memory` (add,
// replace, remove, or a batch of those; it has no read), or an external
// provider's tools, which hermes files under the memory toolset. Only
// writes are listed: a search, probe or list reads, and holographic's
// fact_feedback rates a fact without saying anything new. A provider not
// listed here is invisible to everything that uses MemoryWrite: the eval
// graders and `haunt history`'s echo check.
var memoryWrites = map[string][]string{
	"memory":     nil,
	"fact_store": {"add", "update", "remove"}, // holographic
}

// MemoryWrite reports whether a call of tool with args saved, changed or
// removed something the ghost remembers, and returns the words it saved:
// the content of an add or a replace, every operation's in a batch. A
// removal saves nothing, and its words are "".
func MemoryWrite(tool, args string) (saved string, ok bool) {
	actions, ok := memoryWrites[tool]
	if !ok {
		return "", false
	}
	// The built-in tool calls the new text content, or new_text; a batch
	// is a list of operations of the same shape.
	type op struct {
		Action  string `json:"action"`
		Content string `json:"content"`
		NewText string `json:"new_text"`
	}
	var a struct {
		op
		Operations []op `json:"operations"`
	}
	if err := json.Unmarshal([]byte(args), &a); err != nil {
		// A tool that only writes wrote something, even when what it
		// wrote cannot be read here.
		return "", actions == nil
	}
	if actions != nil && !slices.Contains(actions, a.Action) {
		return "", false
	}
	var words []string
	for _, o := range append([]op{a.op}, a.Operations...) {
		for _, s := range []string{o.Content, o.NewText} {
			if s = strings.TrimSpace(s); s != "" {
				words = append(words, s)
			}
		}
	}
	return strings.Join(words, "\n"), true
}
