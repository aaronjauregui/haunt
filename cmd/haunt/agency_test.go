package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/aaronjauregui/haunt/internal/testenv"
)

// Agency, end to end: a real compositor, a real driver, a real MCP server
// on the end of a real pipe, and a file on disk that only exists if the
// ghost actually did something.
//
// Both paths are covered, because they are different decisions: the model
// asking for a tool in the middle of a conversation, and a brain asking for
// one specific thing at one specific moment.

// fakeToolBackend is an LLM that answers the first request with a tool call
// and the second — the one carrying the tool's result — with words. That is
// the whole shape of the loop the driver runs.
type fakeToolBackend struct {
	mu sync.Mutex
	// offered records whether the tools reached the model, and roles the
	// message sequence it was given, so the test can check the driver built
	// a conversation a real backend would accept.
	offered  bool
	sawTool  bool
	toolText string
}

func (b *fakeToolBackend) serve(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/chat/completions", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Messages []struct {
				Role       string `json:"role"`
				Content    string `json:"content"`
				ToolCallID string `json:"tool_call_id"`
			} `json:"messages"`
			Tools []struct {
				Function struct {
					Name       string          `json:"name"`
					Parameters json.RawMessage `json:"parameters"`
				} `json:"function"`
			} `json:"tools"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		b.mu.Lock()
		if len(req.Tools) > 0 {
			b.offered = true
		}
		var answered bool
		for _, m := range req.Messages {
			if m.Role == "tool" {
				b.sawTool = true
				b.toolText = m.Content
				answered = true
			}
		}
		wire := ""
		if len(req.Tools) > 0 {
			wire = req.Tools[0].Function.Name
		}
		b.mu.Unlock()

		w.Header().Set("Content-Type", "text/event-stream")
		fl := w.(http.Flusher)
		send := func(delta map[string]any) {
			raw, _ := json.Marshal(map[string]any{
				"choices": []map[string]any{{"delta": delta}},
			})
			fmt.Fprintf(w, "data: %s\n\n", raw)
			fl.Flush()
		}
		switch {
		case answered || wire == "":
			// The tool has been run and its result handed back — or there
			// was nothing on offer in the first place.
			send(map[string]any{"content": "there, it is written down"})
		default:
			send(map[string]any{"tool_calls": []map[string]any{{
				"index": 0, "id": "call_1", "type": "function",
				"function": map[string]any{"name": wire},
			}}})
			for _, frag := range []string{`{"text"`, `:"milk"}`} {
				send(map[string]any{"tool_calls": []map[string]any{{
					"index": 0, "function": map[string]any{"arguments": frag},
				}}})
			}
		}
		fmt.Fprint(w, "data: [DONE]\n\n")
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// actingGhostRun starts a ghost with one tool server and returns the paths
// the test needs. brainCmd is empty for the built-in brain.
type actingRun struct {
	dir     string
	bin     string
	rt      string
	record  string
	notes   string
	backend *fakeToolBackend
}

func startActingGhost(t *testing.T, c *testenv.Compositor, brain string) actingRun {
	t.Helper()
	backend := &fakeToolBackend{}
	srv := backend.serve(t)

	dir := t.TempDir()
	rt := filepath.Join(dir, "run")
	cfgDir := filepath.Join(dir, "config")
	if err := os.MkdirAll(cfgDir, 0o755); err != nil {
		t.Fatal(err)
	}
	config := fmt.Sprintf(`[ai.llm]
base_url = %q
model = "fake"
`, srv.URL+"/v1")
	if err := os.WriteFile(filepath.Join(cfgDir, "config.toml"), []byte(config), 0o644); err != nil {
		t.Fatal(err)
	}

	bin := filepath.Join(dir, "haunt")
	build(t, bin, ".")
	server := filepath.Join(dir, "toolserver")
	build(t, server, "./testdata/toolserver")

	notes := filepath.Join(dir, "notes.txt")
	gDir := filepath.Join(dir, "wisp")
	manifest := fmt.Sprintf(`[ghost]
name = "wisp"
capabilities = ["message", "llm", "tools"%s]

[brain]
%s

[persona]
system = "You are Wisp."

[[tool]]
name = "notes"
command = [%q]
env = ["HAUNT_TEST_NOTES"]
`, brainCap(brain), brainLine(brain), server)
	if brain != "" {
		brainBin := filepath.Join(gDir, "brain")
		writeGhost(t, gDir, manifest)
		build(t, brainBin, "./testdata/"+brain)
	} else {
		writeGhost(t, gDir, manifest)
	}

	record := filepath.Join(dir, "session.jsonl")
	cmd := exec.Command(bin, "run", "-x", "100", "-y", "100",
		"-state", filepath.Join(dir, "state"), "-record", record, gDir)
	cmd.Env = append(c.Env(),
		"HAUNT_RUNTIME_DIR="+rt,
		"HAUNT_CONFIG_DIR="+cfgDir,
		"HAUNT_TEST_NOTES="+notes,
		// A variable the manifest does not name, to check it stays behind.
		"HAUNT_TEST_SECRET=do-not-pass-this-on",
	)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cmd.Process.Signal(syscall.SIGTERM)
		cmd.Wait()
	})
	waitFor(t, stdout, "READY")

	return actingRun{dir: dir, bin: bin, rt: rt, record: record, notes: notes, backend: backend}
}

// brainLine is the [brain] section's body: a native process, or the old
// built-in brain this test was written against.
func brainLine(brain string) string {
	if brain == "" {
		return "builtin = true"
	}
	return `command = ["./brain"]`
}

func brainCap(brain string) string {
	if brain == "" {
		return ""
	}
	return `, "native-brain"`
}

func (r actingRun) say(t *testing.T, text string) {
	t.Helper()
	sc := exec.Command(r.bin, "say", "-ghost", "wisp", text)
	sc.Env = append(os.Environ(), "HAUNT_RUNTIME_DIR="+r.rt)
	if out, err := sc.CombinedOutput(); err != nil {
		t.Fatalf("say %q: %v: %s", text, err, out)
	}
}

// TestGhostActsThroughTheModel: art, a persona, a tool server and no code
// anywhere. The model is offered the tool during an ordinary conversation,
// the driver runs it, and a file on disk changes — which is the difference
// between a companion that can do things and one that can describe doing
// them.
func TestGhostActsThroughTheModel(t *testing.T) {
	c := testenv.StartT(t)
	run := startActingGhost(t, c, "")

	run.say(t, "note that I need milk")

	// The thing itself: the tool ran and wrote to the file.
	waitFileContains(t, run.notes, "milk")
	// And then the model answered, with the tool's result in front of it.
	waitRecordContains(t, run.record, "there, it is written down")

	run.backend.mu.Lock()
	defer run.backend.mu.Unlock()
	if !run.backend.offered {
		t.Error("the tools never reached the model")
	}
	if !run.backend.sawTool {
		t.Fatal("the tool's result was never handed back to the model")
	}
	if !strings.Contains(run.backend.toolText, "appended milk") {
		t.Errorf("tool result = %q, want what the server said", run.backend.toolText)
	}
}

// Acting is visibly different from talking, and the journal is the mark
// that survives the moment: it is there whether or not anybody was watching
// the balloon while the tool ran.
func TestActingIsInTheJournal(t *testing.T) {
	c := testenv.StartT(t)
	run := startActingGhost(t, c, "")

	run.say(t, "note that I need milk")
	waitFileContains(t, run.notes, "milk")

	deadline := time.Now().Add(10 * time.Second)
	for {
		out, err := run.journal(t)
		if err == nil && strings.Contains(out, "notes.append") {
			if !strings.Contains(out, "milk") {
				t.Errorf("journal names the tool but not what it was given:\n%s", out)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("the journal never mentioned the action:\n%s (%v)", out, err)
		}
		time.Sleep(200 * time.Millisecond)
	}
}

func (r actingRun) journal(t *testing.T) (string, error) {
	t.Helper()
	cmd := exec.Command(r.bin, "journal", "-state", filepath.Join(r.dir, "state"), "wisp")
	cmd.Env = append(os.Environ(), "HAUNT_RUNTIME_DIR="+r.rt)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// TestBrainActsDirectly: the other path, where a brain asks for one
// particular thing rather than leaving it to the model. The result comes
// back as an `acted` event, which is what a brain waits on.
func TestBrainActsDirectly(t *testing.T) {
	c := testenv.StartT(t)
	run := startActingGhost(t, c, "actor")

	run.say(t, "remember the milk")

	waitFileContains(t, run.notes, "remember the milk")
	waitRecordContains(t, run.record, `"type":"acted"`)
	waitRecordContains(t, run.record, "appended remember the milk")
}

// The environment a tool server runs in is what the manifest named and
// nothing else: a package may ask for a credential and can never carry one,
// and it does not get handed the rest of the user's environment either.
func TestToolServerEnvironmentIsFiltered(t *testing.T) {
	c := testenv.StartT(t)
	run := startActingGhost(t, c, "")

	run.say(t, "note that I need milk")
	waitFileContains(t, run.notes, "milk")

	// The named variable reached the server: it wrote to the file that
	// variable points at, which is the only way it could have known where.
	// The unnamed one is reported by the server itself, since the absence
	// of something is not visible from out here.
	data, err := os.ReadFile(run.notes)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "leaked") {
		t.Errorf("a variable the manifest never named reached the tool server:\n%s", data)
	}
}

// waitFileContains polls a file the tool server writes to, which is how a
// test tells "the ghost did something" from "the ghost said it had".
func waitFileContains(t *testing.T, path, want string) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for {
		data, _ := os.ReadFile(path)
		if strings.Contains(string(data), want) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s never contained %q; have:\n%s", path, want, data)
		}
		time.Sleep(200 * time.Millisecond)
	}
}
