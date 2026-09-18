package main

import (
	"path/filepath"
	"testing"

	"github.com/aaronjauregui/haunt/internal/testenv"
)

// TestCrossGhostTell runs whisper and a caller ghost; the caller's tell
// intent must arrive at whisper as a message with From identity.
func TestCrossGhostTell(t *testing.T) {
	c := testenv.StartT(t)
	dir := t.TempDir()
	rt := filepath.Join(dir, "run")
	bin := filepath.Join(dir, "haunt")
	build(t, bin, ".")

	whisperDir := filepath.Join(dir, "whisper")
	writeGhost(t, whisperDir, `[ghost]
name = "whisper"
capabilities = ["message"]

[brain]
wasm = "brain.wasm"
`)
	build(t, filepath.Join(whisperDir, "brain.wasm"), "../../examples/whisper", "GOOS=wasip1", "GOARCH=wasm")

	callerDir := filepath.Join(dir, "caller")
	writeGhost(t, callerDir, `[ghost]
name = "caller"
capabilities = ["tell"]

[brain]
wasm = "brain.wasm"
`)
	build(t, filepath.Join(callerDir, "brain.wasm"), "./testdata/caller", "GOOS=wasip1", "GOARCH=wasm")

	record := filepath.Join(dir, "whisper.jsonl")
	w := startGhost(t, c, bin, whisperDir, filepath.Join(dir, "ws"), record, rt, 100, 100)
	cl := startGhost(t, c, bin, callerDir, filepath.Join(dir, "cs"), "", rt, 500, 100)

	waitRecordContains(t, record, `"ping from caller"`)
	waitRecordContains(t, record, `"from":"caller"`)

	stopGhost(t, cl)
	stopGhost(t, w)
}
