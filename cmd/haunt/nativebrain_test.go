package main

import (
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/aaronjauregui/haunt/internal/testenv"
)

// TestNativeBrainAndCron runs a ghost with a native-process brain and an
// @every cron trigger; both must function (dev run auto-grants).
func TestNativeBrainAndCron(t *testing.T) {
	c := testenv.StartT(t)
	dir := t.TempDir()
	rt := filepath.Join(dir, "run")
	bin := filepath.Join(dir, "haunt")
	build(t, bin, ".")

	gDir := filepath.Join(dir, "native")
	writeGhost(t, gDir, `[ghost]
name = "native"
capabilities = ["message", "native-brain"]

[brain]
command = ["brain-native"]

[[trigger]]
cron = "@every 1s"
name = "pulse"
`)
	// The whisper brain compiled natively speaks the same protocol.
	build(t, filepath.Join(gDir, "brain-native"), "../../examples/whisper")

	record := filepath.Join(dir, "native.jsonl")
	g := startGhost(t, c, bin, gDir, filepath.Join(dir, "state"), record, rt, 100, 100)

	// Boot reached the native brain: its greeting balloon appears.
	waitBalloon(t, c, dir, 100, 100, true)
	// Cron trigger flows through the bus.
	waitRecordContains(t, record, `"type":"cron"`)
	waitRecordContains(t, record, `"name":"pulse"`)
	// Messaging works against a native brain too.
	say := exec.Command(bin, "say", "-ghost", "native", "over the wire")
	say.Env = append(c.Env(), "HAUNT_RUNTIME_DIR="+rt)
	if out, err := say.CombinedOutput(); err != nil {
		t.Fatalf("say: %v: %s", err, out)
	}
	waitRecordContains(t, record, `"over the wire"`)

	stopGhost(t, g)
}
