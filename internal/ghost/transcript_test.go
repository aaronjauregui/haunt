package ghost

import (
	"encoding/json"
	"testing"
)

// A call whose arguments are not JSON is still recorded, the arguments
// as a JSON string of what the model wrote, so its result row never
// answers a call the transcript lacks.
func TestTranscriptKeepsACallWithMalformedArgs(t *testing.T) {
	tr, err := openTranscript(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer tr.Close()
	bad := json.RawMessage(`{"topic": oops`)
	row := TranscriptRow{Kind: rowCalls, Calls: []TranscriptCall{{ID: "c1", Name: "recall", Args: bad}}}
	if err := tr.add(row); err != nil {
		t.Fatalf("add: %v", err)
	}
	rows, err := tr.all()
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || len(rows[0].Calls) != 1 {
		t.Fatalf("rows = %+v", rows)
	}
	var got string
	if err := json.Unmarshal(rows[0].Calls[0].Args, &got); err != nil || got != string(bad) {
		t.Errorf("args = %s, want the raw text as a JSON string (%v)", rows[0].Calls[0].Args, err)
	}
}
