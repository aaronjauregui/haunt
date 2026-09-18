//go:build eval

package main

import (
	"bytes"
	"encoding/json"
	"testing"
	"time"
)

// `haunt history -json` writes traceRecord and the eval harness reads
// TraceTurn; a record with every field set survives the trip whole, with
// no field on either side the other lacks.
func TestTraceRecordIsTraceTurn(t *testing.T) {
	rec := traceRecord{
		Time: time.Unix(1789614336, 0).UTC(), ProvokedBy: "person", Input: "remind me", Reply: "Done.",
		SelfPrompt: true, Silent: true,
		Acted:  []traceCall{{Tool: "intend", Args: `{"text":"oven"}`, Result: "hermes/1"}},
		Looked: []traceCall{{Tool: "skill_view", Args: `{"name":"x"}`, Result: "…"}},
		Window: &traceWindow{App: "foot", Title: "~"},
	}
	out, err := json.Marshal(rec)
	if err != nil {
		t.Fatal(err)
	}
	var turn TraceTurn
	dec := json.NewDecoder(bytes.NewReader(out))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&turn); err != nil {
		t.Fatalf("%s: %v", out, err)
	}
	back, err := json.Marshal(turn)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(out, back) {
		t.Errorf("history -json writes\n%s\nand the harness reads it as\n%s", out, back)
	}

	// Window is left out when nothing is known of it.
	rec.Window = nil
	out, _ = json.Marshal(rec)
	if bytes.Contains(out, []byte(`"window"`)) {
		t.Errorf("an unknown window is written: %s", out)
	}
}
