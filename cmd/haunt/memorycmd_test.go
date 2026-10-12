package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aaronjauregui/haunt/internal/control"
	"github.com/aaronjauregui/haunt/internal/proto"
)

// writeMemory lays out a ghost state directory with two topic files, as
// a ghost or the person would have written them.
func writeMemory(t *testing.T) string {
	t.Helper()
	state := filepath.Join(t.TempDir(), "wisp")
	topics := filepath.Join(state, "memory", "topics")
	if err := os.MkdirAll(topics, 0o700); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		"food.md": "---\ntopic: food\nupdated: 2026-10-10T09:12:00+10:00\n---\n" +
			"- likes apples <!-- person t=412 2026-10-09 -->\n" +
			"- allergic to cats <!-- person t=98 2026-09-30 -->\n",
		"work.md": "---\ntopic: work\nupdated: 2026-10-10T09:12:00+10:00\n---\n" +
			"- writes Go late at night <!-- self t=7 2026-10-01 -->\n",
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(topics, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return state
}

func runMemory(t *testing.T, args ...string) (string, error) {
	t.Helper()
	var out bytes.Buffer
	err := memoryMain(&out, args)
	return out.String(), err
}

func TestMemoryLsShowRm(t *testing.T) {
	state := writeMemory(t)
	out, err := runMemory(t, "ls", "-state", state)
	if err != nil {
		t.Fatal(err)
	}
	if out != "food  2\nwork  1\n" {
		t.Errorf("ls =\n%s", out)
	}

	out, err = runMemory(t, "show", "-state", state, "food")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"  1  likes apples  (person, turn 412, 2026-10-09)", "  2  allergic to cats  (person, turn 98, 2026-09-30)"} {
		if !strings.Contains(out, want) {
			t.Errorf("show missing %q:\n%s", want, out)
		}
	}

	if _, err := runMemory(t, "rm", "-state", state, "food", "1"); err != nil {
		t.Fatal(err)
	}
	out, _ = runMemory(t, "show", "-state", state, "food")
	if strings.Contains(out, "apples") || !strings.Contains(out, "  1  allergic to cats") {
		t.Errorf("after rm food 1:\n%s", out)
	}
	if _, err := runMemory(t, "rm", "-state", state, "work"); err != nil {
		t.Fatal(err)
	}
	out, _ = runMemory(t, "ls", "-state", state)
	if out != "food  1\n" {
		t.Errorf("after rm work:\n%s", out)
	}
	if _, err := runMemory(t, "rm", "-state", state, "food", "5"); err == nil {
		t.Error("rm of a line that is not there succeeded")
	}
	if out, err := runMemory(t, "-h"); err != nil || !strings.Contains(out, "haunt memory reindex") {
		t.Errorf("-h = %q, %v", out, err)
	}
}

// TestMemoryReindexAlone: with no ghost on the socket, reindex opens
// state.db itself and builds the index from the files.
func TestMemoryReindexAlone(t *testing.T) {
	t.Setenv("HAUNT_RUNTIME_DIR", t.TempDir())
	state := writeMemory(t)
	out, err := runMemory(t, "reindex", "-state", state)
	if err != nil {
		t.Fatal(err)
	}
	if out != "reindexed 2 topics, 3 lines\n" {
		t.Errorf("reindex = %q", out)
	}
	if _, err := os.Stat(filepath.Join(state, "state.db")); err != nil {
		t.Error(err)
	}
}

// TestMemoryReindexRunning: with a ghost on the socket, reindex asks it
// and leaves state.db alone, reporting what the ghost answered.
func TestMemoryReindexRunning(t *testing.T) {
	t.Setenv("HAUNT_RUNTIME_DIR", t.TempDir())
	state := writeMemory(t)
	srv, err := control.Listen("wisp", func(proto.Event) error { return nil }, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	_, err = runMemory(t, "reindex", "-state", state)
	if err == nil || !strings.Contains(err.Error(), "wisp is running and answered: error:") {
		t.Errorf("reindex against a running ghost = %v", err)
	}
	if _, err := os.Stat(filepath.Join(state, "state.db")); !os.IsNotExist(err) {
		t.Errorf("state.db opened while the ghost runs: %v", err)
	}
}

// show prints the past after the facts, under a header and unnumbered,
// so rm's numbers are the facts' alone.
func TestMemoryShowPast(t *testing.T) {
	state := writeMemory(t)
	where := "---\ntopic: where\n---\n" +
		"- lives in Brisbane <!-- person t=9 2026-10-12 -->\n" +
		"- works from home <!-- person t=11 2026-10-12 -->\n\n" +
		"## Past\n- lives in Sydney <!-- person t=3 2026-09-30 --> <!-- retired 2026-10-12 -->\n"
	if err := os.WriteFile(filepath.Join(state, "memory", "topics", "where.md"), []byte(where), 0o600); err != nil {
		t.Fatal(err)
	}
	out, err := runMemory(t, "show", "-state", state, "where")
	if err != nil {
		t.Fatal(err)
	}
	want := "  1  lives in Brisbane  (person, turn 9, 2026-10-12)\n" +
		"  2  works from home  (person, turn 11, 2026-10-12)\n" +
		"past:\n" +
		"     lives in Sydney  (person, turn 3, 2026-09-30, retired 2026-10-12)\n"
	if !strings.HasSuffix(out, want) {
		t.Errorf("show =\n%s\nwant it to end\n%s", out, want)
	}
	if out, _ := runMemory(t, "ls", "-state", state); !strings.Contains(out, "where  2\n") {
		t.Errorf("ls counts the past:\n%s", out)
	}
	if _, err := runMemory(t, "rm", "-state", state, "where", "2"); err != nil {
		t.Fatal(err)
	}
	out, _ = runMemory(t, "show", "-state", state, "where")
	if strings.Contains(out, "works from home") || !strings.Contains(out, "lives in Sydney") {
		t.Errorf("after rm where 2:\n%s", out)
	}
}
