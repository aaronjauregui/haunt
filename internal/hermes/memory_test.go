package hermes

import "testing"

// A write is any store's write, and only a write; what it saved is every
// new text in it.
func TestMemoryWrite(t *testing.T) {
	for _, tc := range []struct {
		tool, args string
		saved      string
		write      bool
	}{
		{"memory", `{"action":"add","target":"user","content":"takes coffee black"}`, "takes coffee black", true},
		{"memory", `{"action":"replace","old_text":"tea","new_text":"coffee"}`, "coffee", true},
		{"memory", `{"action":"remove","old_text":"tea"}`, "", true},
		{"memory", `{"action":"batch","target":"user","operations":[{"action":"replace","old_text":"tea","content":"coffee"},` +
			`{"action":"add","content":"black, no sugar"}]}`, "coffee\nblack, no sugar", true},
		{"memory", `not json`, "", true},
		{"fact_store", `{"action":"add","content":"takes coffee black"}`, "takes coffee black", true},
		{"fact_store", `{"action":"update","fact_id":3,"content":"black"}`, "black", true},
		{"fact_store", `{"action":"remove","fact_id":3}`, "", true},
		{"fact_store", `{"action":"search","query":"coffee black"}`, "", false},
		{"fact_store", `not json`, "", false},
		{"fact_feedback", `{"action":"helpful","fact_id":3}`, "", false},
		{"session_search", `{"query":"black"}`, "", false},
	} {
		saved, write := MemoryWrite(tc.tool, tc.args)
		if saved != tc.saved || write != tc.write {
			t.Errorf("MemoryWrite(%s %s) = %q, %v; want %q, %v", tc.tool, tc.args, saved, write, tc.saved, tc.write)
		}
	}
}
