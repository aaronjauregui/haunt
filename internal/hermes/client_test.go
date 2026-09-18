package hermes

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"testing/fstest"

	"gopkg.in/yaml.v3"
)

// fakeHermes is an API server that replays captured streams.
type fakeHermes struct {
	t        *testing.T
	mu       sync.Mutex
	sessions map[string]bool
	bodies   []map[string]any
	// stream answers a chat turn with a fixture's contents.
	stream func(session, input string) string
	// history, if set, is the session's stored messages as the API gives
	// them; unset, a two-turn session with one lookup and one of the
	// ghost's calls.
	history string
}

func newFakeHermes(t *testing.T) (*fakeHermes, *httptest.Server) {
	f := &fakeHermes{t: t, sessions: map[string]bool{}}
	mux := http.NewServeMux()
	auth := func(h http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("Authorization") != "Bearer sekrit" {
				http.Error(w, `{"error":{"message":"no","code":"gateway_auth_failed"}}`, 401)
				return
			}
			if r.Header.Get("X-Hermes-Session-Key") != "moth" {
				t.Errorf("session key header was %q", r.Header.Get("X-Hermes-Session-Key"))
			}
			h(w, r)
		}
	}
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"status":"ok"}`))
	})
	mux.HandleFunc("POST /api/sessions", auth(func(w http.ResponseWriter, r *http.Request) {
		var body struct{ ID string }
		json.NewDecoder(r.Body).Decode(&body)
		f.mu.Lock()
		defer f.mu.Unlock()
		if f.sessions[body.ID] {
			w.WriteHeader(409)
			w.Write([]byte(`{"error":{"message":"exists","type":"invalid_request_error","code":"session_exists"}}`))
			return
		}
		f.sessions[body.ID] = true
		w.WriteHeader(201)
		json.NewEncoder(w).Encode(map[string]any{"session": map[string]string{"id": body.ID}})
	}))
	mux.HandleFunc("POST /api/sessions/{id}/chat/stream", auth(func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		f.mu.Lock()
		known := f.sessions[id]
		f.bodies = append(f.bodies, body)
		f.mu.Unlock()
		if !known {
			w.WriteHeader(404)
			w.Write([]byte(`{"error":{"message":"gone","code":"session_not_found"}}`))
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		input, _ := body["input"].(string)
		w.Write([]byte(f.stream(id, input)))
	}))
	mux.HandleFunc("GET /api/sessions/{id}/messages", auth(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("order") != "latest" || r.URL.Query().Get("limit") != "10" {
			t.Errorf("history asked with %s", r.URL.RawQuery)
		}
		if f.history != "" {
			w.Write([]byte(f.history))
			return
		}
		w.Write([]byte(`{"object":"list","data":[
			{"role":"user","content":"hello","timestamp":1789614336.5},
			{"role":"assistant","content":"Let me think.","tool_calls":[
				{"id":"c1","type":"function","function":{"name":"session_search","arguments":"{\"query\": \"oven\"}"}},
				{"id":"c2","type":"function","function":{"name":"mcp__haunt__face","arguments":"{}"}}]},
			{"role":"tool","tool_call_id":"c1","content":"{\"results\": [\"they burned the bread\"]}"},
			{"role":"tool","tool_call_id":"c2","content":"{\"result\":\"ok\"}"},
			{"role":"assistant","content":"hi there","timestamp":1789614337},
			{"role":"user","content":"bye"},
			{"role":"assistant","content":"bye!"}]}`))
	}))
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return f, srv
}

func fixture(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func testClient(srv *httptest.Server, saved *string) *Client {
	return &Client{
		BaseURL:     srv.URL,
		Key:         "sekrit",
		Ghost:       "moth",
		LoadSession: func() string { return *saved },
		SaveSession: func(id string) { *saved = id },
	}
}

// A captured tool turn: the deltas stream, the tool is reported, and the
// completed text is the reply.
func TestTurnStreamsAToolTurn(t *testing.T) {
	f, srv := newFakeHermes(t)
	f.stream = func(string, string) string {
		return strings.ReplaceAll(fixture(t, "remind.sse"), `"haunt-fake"`, `"haunt-moth"`)
	}
	var saved string
	c := testClient(srv, &saved)

	var deltas strings.Builder
	var tools []ToolEvent
	reply, err := c.Turn(context.Background(), TurnRequest{
		Input:   "Can you remind me in ten minutes to check the oven?",
		Context: "Now: Thu 17 Sep 14:03 (afternoon).",
		OnDelta: func(d string) { deltas.WriteString(d) },
		OnTool:  func(e ToolEvent) { tools = append(tools, e) },
	})
	if err != nil {
		t.Fatal(err)
	}
	want := "Done. I'll remind you about the oven in ten minutes."
	if reply.Text != want || strings.TrimSpace(deltas.String()) != want {
		t.Errorf("reply %q, streamed %q", reply.Text, deltas.String())
	}
	if len(reply.Tools) != 1 || reply.Tools[0] != "mcp__haunt__intend" {
		t.Errorf("tools %v", reply.Tools)
	}
	if len(tools) != 2 || tools[0].Done || !tools[1].Done {
		t.Errorf("tool events %+v", tools)
	}
	if saved != "haunt-moth" {
		t.Errorf("session saved as %q", saved)
	}
	b := f.bodies[0]
	if b["input"] != "Can you remind me in ten minutes to check the oven?" || b["system_message"] != "Now: Thu 17 Sep 14:03 (afternoon)." {
		t.Errorf("turn body %v", b)
	}
}

// The captured NO_REPLY turn comes back as the token, for the caller to
// suppress.
func TestTurnReturnsSilence(t *testing.T) {
	f, srv := newFakeHermes(t)
	f.stream = func(string, string) string { return fixture(t, "idle.sse") }
	saved := ""
	c := testClient(srv, &saved)
	reply, err := c.Turn(context.Background(), TurnRequest{Input: Envelope("idle")})
	if err != nil {
		t.Fatal(err)
	}
	if !Silent(reply.Text) {
		t.Errorf("reply %q is not silence", reply.Text)
	}
	// The fixture's session id differs from ours, as after compression:
	// the client follows it.
	if saved != "haunt-fake" {
		t.Errorf("did not follow the session to %q, have %q", "haunt-fake", saved)
	}
}

// A session that exists already is adopted; one that has vanished is made
// again.
func TestSessionIsFoundAndRemade(t *testing.T) {
	f, srv := newFakeHermes(t)
	f.sessions["haunt-moth"] = true
	f.stream = func(id, _ string) string {
		return "event: assistant.completed\ndata: {\"session_id\":\"" + id + "\",\"content\":\"hi\"}\n\nevent: done\ndata: {}\n\n"
	}
	saved := ""
	c := testClient(srv, &saved)
	if _, err := c.Turn(context.Background(), TurnRequest{Input: "hello"}); err != nil {
		t.Fatal(err)
	}
	if saved != "haunt-moth" {
		t.Errorf("saved %q", saved)
	}

	saved = "api_gone"
	c = testClient(srv, &saved)
	reply, err := c.Turn(context.Background(), TurnRequest{Input: "hello"})
	if err != nil || reply.Text != "hi" {
		t.Fatalf("reply %q, %v", reply.Text, err)
	}
	if saved != "haunt-moth" {
		t.Errorf("a vanished session was not replaced: %q", saved)
	}
}

func TestTurnReportsFailure(t *testing.T) {
	f, srv := newFakeHermes(t)
	f.sessions["haunt-moth"] = true
	f.stream = func(string, string) string {
		return "event: run.started\ndata: {}\n\n: keepalive\n\nevent: error\ndata: {\"message\":\"context window too small\"}\n\nevent: done\ndata: {}\n\n"
	}
	saved := "haunt-moth"
	_, err := testClient(srv, &saved).Turn(context.Background(), TurnRequest{Input: "x"})
	if err == nil || !strings.Contains(err.Error(), "context window too small") {
		t.Errorf("error %v", err)
	}
	saved = "haunt-moth"
	c := testClient(srv, &saved)
	c.Key = "wrong"
	if _, err := c.Turn(context.Background(), TurnRequest{Input: "x"}); err == nil || !strings.Contains(err.Error(), "401") {
		t.Errorf("unauthorised turn: %v", err)
	}
}

func TestMessages(t *testing.T) {
	f, srv := newFakeHermes(t)
	f.sessions["haunt-moth"] = true
	saved := "haunt-moth"
	msgs, err := testClient(srv, &saved).Messages(context.Background(), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 5 || msgs[0].Text != "hello" || msgs[2].Text != "hi there" || msgs[0].Time.Unix() != 1789614336 ||
		!msgs[1].Interim || msgs[2].Interim {
		t.Fatalf("messages %+v", msgs)
	}
	want := []Lookup{{Tool: "session_search", Args: `{"query": "oven"}`, Result: `{"results": ["they burned the bread"]}`}}
	if !slices.Equal(msgs[2].Looked, want) || msgs[1].Looked != nil || msgs[4].Looked != nil {
		t.Errorf("lookups %+v, want %+v on the first turn's last reply only", msgs[2].Looked, want)
	}
	acted := []Lookup{{Tool: "face", Args: "{}", Result: `{"result":"ok"}`}}
	if !slices.Equal(msgs[2].Acted, acted) || msgs[4].Acted != nil {
		t.Errorf("the ghost's calls %+v, want %+v on the first turn's last reply only", msgs[2].Acted, acted)
	}
}

// Messages cuts a call to a line for a person; Transcript keeps it whole
// for a reader that matches on it.
func TestTranscriptKeepsCallsWhole(t *testing.T) {
	long := strings.Repeat("a line of notes\n", 20)
	if got := clip(long, lookupResult); got == long || len([]rune(got)) > lookupResult {
		t.Fatalf("clip left %q", got)
	}
	f, srv := newFakeHermes(t)
	f.sessions["haunt-moth"] = true
	f.history = `{"object":"list","data":[
		{"role":"user","content":"what's in my notes?"},
		{"role":"assistant","content":"","tool_calls":[
			{"id":"c1","type":"function","function":{"name":"mcp__haunt__notes_read","arguments":"{\"file\": \"todo\"}"}}]},
		{"role":"tool","tool_call_id":"c1","content":` + strconv.Quote(long) + `},
		{"role":"assistant","content":"Mostly lines."}]}`
	saved := "haunt-moth"
	c := testClient(srv, &saved)
	whole, err := c.Transcript(context.Background(), 10)
	if err != nil {
		t.Fatal(err)
	}
	want := []Lookup{{Tool: "notes_read", Args: `{"file": "todo"}`, Result: long}}
	if len(whole) != 2 || !slices.Equal(whole[1].Acted, want) {
		t.Fatalf("transcript %+v", whole)
	}
	short, err := c.Messages(context.Background(), 10)
	if err != nil {
		t.Fatal(err)
	}
	if r := short[1].Acted[0].Result; r == long || strings.Contains(r, "\n") {
		t.Errorf("history result not cut to a line: %q", r)
	}
}

// The profile is the ghost's: haunt's keys are rewritten, the person's are
// kept, and the key and port are chosen once.
func TestEnsureProfile(t *testing.T) {
	root := t.TempDir()
	c := Config{Home: root}
	spec := ProfileSpec{
		Ghost:   "Moth",
		Persona: Persona{Name: "Moth", System: "You are Moth, dusty and kind.", Poses: []string{"happy", "sad"}},
		Serve:   []string{"/usr/bin/haunt", "mcp-serve", "--ghost", "Moth"},
		Env:     map[string]string{"HAUNT_RUNTIME_DIR": "/run/x"},
	}
	p, err := c.EnsureProfile(spec)
	if err != nil {
		t.Fatal(err)
	}
	if p.Dir != filepath.Join(root, "profiles", "haunt-moth") || len(p.Key) != 64 || p.Port == 0 {
		t.Fatalf("profile %+v", p)
	}
	soul, _ := os.ReadFile(filepath.Join(p.Dir, "SOUL.md"))
	if !strings.HasPrefix(string(soul), "You are Moth, dusty and kind.") || !strings.Contains(string(soul), "NO_REPLY") ||
		!strings.Contains(string(soul), "happy, sad") {
		t.Errorf("SOUL.md:\n%s", soul)
	}

	if _, err := os.Stat(filepath.Join(p.Dir, noBundledSkills)); err != nil {
		t.Errorf("a new profile was not opted out of bundled skills: %v", err)
	}
	// Once hermes has seeded skills, opting back in is the person's call.
	os.MkdirAll(filepath.Join(p.Dir, "skills"), 0o700)
	os.Remove(filepath.Join(p.Dir, noBundledSkills))

	// The person picks a model and adds a key of their own.
	cfgPath := filepath.Join(p.Dir, "config.yaml")
	var doc map[string]any
	raw, _ := os.ReadFile(cfgPath)
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	if doc["_config_version"] != configVersion {
		t.Errorf("_config_version = %v, want %d", doc["_config_version"], configVersion)
	}
	doc["_config_version"] = configVersion + 1 // hermes migrated it
	doc["model"] = map[string]any{"default": "their-model"}
	doc["platform_toolsets"] = map[string]any{"api_server": []string{"terminal"}}
	out, _ := yaml.Marshal(doc)
	os.WriteFile(cfgPath, out, 0o600)
	f, _ := os.OpenFile(filepath.Join(p.Dir, ".env"), os.O_APPEND|os.O_WRONLY, 0)
	f.WriteString("OPENROUTER_API_KEY=theirs\n")
	f.Close()

	spec.Persona.System = "You are Moth, now tidier."
	p2, err := c.EnsureProfile(spec)
	if err != nil {
		t.Fatal(err)
	}
	if p2.Key != p.Key || p2.Port != p.Port {
		t.Errorf("key or port moved: %+v then %+v", p, p2)
	}
	raw, _ = os.ReadFile(cfgPath)
	doc = nil
	yaml.Unmarshal(raw, &doc)
	if m, _ := doc["model"].(map[string]any); m["default"] != "their-model" {
		t.Errorf("the person's model was lost: %v", doc["model"])
	}
	if doc["_config_version"] != configVersion+1 {
		t.Errorf("a migrated _config_version was reset: %v", doc["_config_version"])
	}
	ts := doc["platform_toolsets"].(map[string]any)["api_server"].([]any)
	if len(ts) != 4 || ts[3] != "haunt" {
		t.Errorf("toolsets not restricted: %v", ts)
	}
	if doc["tools"].(map[string]any)["tool_search"].(map[string]any)["enabled"] != "off" {
		t.Errorf("tool search not off: %v", doc["tools"])
	}
	srv := doc["mcp_servers"].(map[string]any)["haunt"].(map[string]any)
	if srv["command"] != "/usr/bin/haunt" || srv["timeout"] != 600 || srv["env"].(map[string]any)["HAUNT_RUNTIME_DIR"] != "/run/x" {
		t.Errorf("mcp server entry %v", srv)
	}
	if doc["memory"].(map[string]any)["write_approval"] != false {
		t.Errorf("memory %v", doc["memory"])
	}
	env, _ := readEnv(filepath.Join(p.Dir, ".env"))
	if env["OPENROUTER_API_KEY"] != "theirs" || env["HAUNT_GHOST"] != "Moth" {
		t.Errorf(".env %v", env)
	}
	if _, err := os.Stat(filepath.Join(p.Dir, noBundledSkills)); err == nil {
		t.Errorf("the skills opt-out came back after the person opted in")
	}
	soul, _ = os.ReadFile(filepath.Join(p.Dir, "SOUL.md"))
	if !strings.HasPrefix(string(soul), "You are Moth, now tidier.") {
		t.Errorf("SOUL.md was not regenerated")
	}

	// A profile left naming the deleted memory provider loses it.
	raw, _ = os.ReadFile(cfgPath)
	stale := strings.Replace(string(raw), "memory:\n", "memory:\n    provider: haunt\n", 1)
	if stale == string(raw) {
		t.Fatalf("no memory section to plant a provider in:\n%s", raw)
	}
	if err := os.WriteFile(cfgPath, []byte(stale), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := c.EnsureProfile(spec); err != nil {
		t.Fatal(err)
	}
	raw, _ = os.ReadFile(cfgPath)
	if strings.Contains(string(raw), "provider: haunt") {
		t.Errorf("stale memory provider kept:\n%s", raw)
	}

	opened, err := c.OpenProfile("Moth")
	if err != nil || opened.Key != p.Key || opened.Port != p.Port {
		t.Errorf("OpenProfile %+v %v", opened, err)
	}
}

func TestProfileName(t *testing.T) {
	for in, want := range map[string]string{"wisp": "haunt-wisp", "Moth Two": "haunt-moth-two", "ü": "haunt--"} {
		if got := ProfileName(in); got != want {
			t.Errorf("ProfileName(%q) = %q, want %q", in, got, want)
		}
	}
}

// Skills are copied into the profile, read-only, Markdown only, and named
// in skills.external_dirs beside whatever the person listed there.
func TestEnsureProfileSkills(t *testing.T) {
	root := t.TempDir()
	c := Config{Home: root}
	skill := func(name string) *fstest.MapFile {
		return &fstest.MapFile{Data: []byte("---\nname: " + name + "\ndescription: x\n---\n# " + name + "\n")}
	}
	pkg := fstest.MapFS{
		"kitchen/SKILL.md":           skill("kitchen"),
		"kitchen/references/oven.md": {Data: []byte("the oven runs hot")},
		"kitchen/scripts/run.sh":     {Data: []byte("rm -rf ~")},
		"kitchen/.hidden.md":         {Data: []byte("no")},
		"focus-session/SKILL.md":     skill("focus-session"),
		"notaskill/README.md":        {Data: []byte("no SKILL.md")},
		".secret/SKILL.md":           skill("secret"),
	}
	own := fstest.MapFS{
		"focus-session/SKILL.md":    {Data: []byte("haunt's own, shadowed by the package's")},
		"morning-check-in/SKILL.md": skill("morning-check-in"),
	}
	spec := ProfileSpec{
		Ghost: "Moth",
		Serve: []string{"/usr/bin/haunt", "mcp-serve", "--ghost", "Moth"},
		Skills: []SkillSource{
			{Name: "ghost", About: `Moth's "own"`, FS: pkg},
			{Name: "haunt", About: "haunt's", FS: own},
		},
	}
	p, err := c.EnsureProfile(spec)
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(p.Dir, SkillsDir)
	var got []string
	filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			rel, _ := filepath.Rel(dir, path)
			got = append(got, rel)
			if info, _ := d.Info(); info.Mode().Perm()&0o222 != 0 {
				t.Errorf("%s is writable: %v", rel, info.Mode())
			}
		}
		if err == nil && d.IsDir() {
			if info, _ := d.Info(); info.Mode().Perm()&0o222 != 0 {
				t.Errorf("directory %s is writable: %v", path, info.Mode())
			}
		}
		return err
	})
	want := []string{
		"ghost/DESCRIPTION.md", "ghost/focus-session/SKILL.md", "ghost/kitchen/SKILL.md",
		"ghost/kitchen/references/oven.md", "haunt/DESCRIPTION.md", "haunt/morning-check-in/SKILL.md",
	}
	if !slices.Equal(got, want) {
		t.Errorf("installed %v, want %v", got, want)
	}
	desc, _ := os.ReadFile(filepath.Join(dir, "ghost", "DESCRIPTION.md"))
	var fm struct{ Description string }
	if err := yaml.Unmarshal(bytes.Trim(desc, "-\n"), &fm); err != nil || fm.Description != `Moth's "own"` {
		t.Errorf("DESCRIPTION.md = %q (%v)", desc, err)
	}
	// Hermes cannot change or add to them.
	if err := os.WriteFile(filepath.Join(dir, "haunt", "morning-check-in", "SKILL.md"), []byte("x"), 0o600); err == nil {
		t.Error("an installed skill could be overwritten")
	}
	if err := os.Mkdir(filepath.Join(dir, "haunt", "new"), 0o700); err == nil {
		t.Error("a skill could be added beside haunt's")
	}

	cfgPath := filepath.Join(p.Dir, "config.yaml")
	readSkills := func() map[string]any {
		t.Helper()
		var doc map[string]any
		raw, _ := os.ReadFile(cfgPath)
		if err := yaml.Unmarshal(raw, &doc); err != nil {
			t.Fatal(err)
		}
		s, _ := doc["skills"].(map[string]any)
		return s
	}
	s := readSkills()
	if d, _ := s["external_dirs"].([]any); len(d) != 1 || d[0] != SkillsDir {
		t.Errorf("external_dirs = %v", s["external_dirs"])
	}
	if s["guard_agent_created"] != true {
		t.Errorf("guard_agent_created = %v", s["guard_agent_created"])
	}

	// The person adds a directory of their own and turns the guard off;
	// the package drops its skills.
	raw, _ := os.ReadFile(cfgPath)
	raw = []byte(strings.Replace(string(raw), "guard_agent_created: true", "guard_agent_created: false", 1))
	raw = []byte(strings.Replace(string(raw), "- "+SkillsDir, "- ~/my-skills\n        - "+SkillsDir, 1))
	os.WriteFile(cfgPath, raw, 0o600)
	spec.Skills[0].FS = fstest.MapFS{}
	if _, err := c.EnsureProfile(spec); err != nil {
		t.Fatal(err)
	}
	s = readSkills()
	if d, _ := s["external_dirs"].([]any); len(d) != 2 || d[0] != "~/my-skills" || d[1] != SkillsDir {
		t.Errorf("external_dirs after the person's edit = %v", s["external_dirs"])
	}
	if s["guard_agent_created"] != false {
		t.Errorf("the person's guard_agent_created was reset: %v", s["guard_agent_created"])
	}
	if _, err := os.Stat(filepath.Join(dir, "ghost")); !os.IsNotExist(err) {
		t.Errorf("the package's old skills are still installed (%v)", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "haunt", "focus-session", "SKILL.md")); err != nil {
		t.Errorf("haunt's focus-session is not installed once the package's is gone: %v", err)
	}

	// What the profile holds, by where it came from.
	local := filepath.Join(p.Dir, "skills")
	for rel, body := range map[string]string{
		"reminders/SKILL.md":          "---\nname: reminders\ndescription: How they like reminders.\n---\nUse mcp__haunt__intend.",
		"shell/SKILL.md":              "---\nname: shell\ndescription: x\n---\nRun it with terminal, then read_file and terminal again.",
		"notes/hermes-agent/SKILL.md": "---\nname: hermes-agent\ndescription: y\n---\n",
		".archive/old/SKILL.md":       "---\nname: old\n---\n",
		".bundled_manifest":           "hermes-agent:abc\n",
	} {
		os.MkdirAll(filepath.Dir(filepath.Join(local, rel)), 0o700)
		os.WriteFile(filepath.Join(local, rel), []byte(body), 0o600)
	}
	list, err := c.Skills("Moth")
	if err != nil {
		t.Fatal(err)
	}
	var summary []string
	for _, sk := range list {
		summary = append(summary, sk.Source+":"+sk.Name+":"+strings.Join(sk.Assumes, "+"))
	}
	wantList := []string{"hermes:hermes-agent:", "learned:reminders:", "learned:shell:terminal+read_file",
		"haunt:focus-session:", "haunt:morning-check-in:"}
	if !slices.Equal(summary, wantList) {
		t.Errorf("Skills = %v, want %v", summary, wantList)
	}
	if list[1].Description != "How they like reminders." || list[1].Dir != filepath.Join(local, "reminders") {
		t.Errorf("learned skill = %+v", list[1])
	}

	// No skills at all: haunt's entry goes, the person's stays.
	spec.Skills = nil
	if _, err := c.EnsureProfile(spec); err != nil {
		t.Fatal(err)
	}
	s = readSkills()
	if d, _ := s["external_dirs"].([]any); len(d) != 1 || d[0] != "~/my-skills" {
		t.Errorf("external_dirs with no skills = %v", s["external_dirs"])
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Errorf("%s is still there with no skills (%v)", dir, err)
	}
}
