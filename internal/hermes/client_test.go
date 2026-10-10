package hermes

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
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
	"time"

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
				http.Error(w, `{"error":{"message":"no","code":"gateway_auth_failed"}}`, http.StatusUnauthorized)
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
		if q := r.URL.Query(); q.Get("order") != "latest" || q.Get("limit") != "10" || q.Get("include_compacted") != "true" {
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
			{"role":"assistant","content":"[happy] hi there","timestamp":1789614337},
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

// streamParts is a turn's stream as the caller sees it: the parts OnInterim
// ends, each with what OnDelta delivered for it and hermes's own text, and
// the order the callbacks came in.
type streamParts struct {
	cur     strings.Builder
	interim []string // hermes's text for each part OnInterim ended
	deltas  []string // what OnDelta delivered for those parts
	order   []string
}

func (p *streamParts) request(input string) TurnRequest {
	return TurnRequest{
		Input: input,
		OnDelta: func(d string) {
			p.cur.WriteString(d)
			if n := len(p.order); n == 0 || p.order[n-1] != "delta" {
				p.order = append(p.order, "delta")
			}
		},
		OnInterim: func(text string) {
			p.interim = append(p.interim, text)
			p.deltas = append(p.deltas, p.cur.String())
			p.cur.Reset()
			p.order = append(p.order, "interim")
		},
		OnTool: func(e ToolEvent) {
			if !e.Done {
				p.order = append(p.order, "tool")
			}
		},
	}
}

// Words written alongside a tool call stream as deltas and are then marked
// as commentary, before the call starts. They are not the reply: the reply
// is what follows the mark, which hermes opens with a blank line, and
// assistant.completed carries the reply alone.
func TestTurnMarksInterimWords(t *testing.T) {
	f, srv := newFakeHermes(t)
	f.stream = func(string, string) string {
		return strings.ReplaceAll(fixture(t, "lookup.sse"), `"haunt-fake"`, `"haunt-moth"`)
	}
	saved := ""
	var p streamParts
	reply, err := testClient(srv, &saved).Turn(context.Background(), p.request("Did I burn anything last week?"))
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(p.order, []string{"delta", "interim", "tool", "delta"}) {
		t.Errorf("callbacks came %v", p.order)
	}
	// The deltas also carry the blank line the model wrote before its
	// call; the commentary is the words alone.
	const interim = "I will check your history."
	if !slices.Equal(p.interim, []string{interim}) || len(p.deltas) != 1 || strings.TrimSpace(p.deltas[0]) != interim {
		t.Errorf("interim %q, streamed as %q", p.interim, p.deltas)
	}
	const want = "I found nothing about you burning anything last week."
	if reply.Text != want || p.cur.String() != "\n\n"+want {
		t.Errorf("reply %q, streamed after the mark %q", reply.Text, p.cur.String())
	}
	if !slices.Equal(reply.Tools, []string{"session_search"}) {
		t.Errorf("tools %v", reply.Tools)
	}
}

// With display.interim_assistant_messages off hermes sends no commentary,
// and the tool call is what marks the words before it as interim.
func TestTurnMarksInterimWordsAtTheCall(t *testing.T) {
	f, srv := newFakeHermes(t)
	sse := strings.ReplaceAll(fixture(t, "lookup-interim-off.sse"), `"haunt-fake"`, `"haunt-moth"`)
	if strings.Contains(sse, "assistant.commentary") {
		t.Fatal("lookup-interim-off.sse has commentary")
	}
	f.stream = func(string, string) string { return sse }
	saved := ""
	var p streamParts
	reply, err := testClient(srv, &saved).Turn(context.Background(), p.request("Did I burn anything last week?"))
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(p.order, []string{"delta", "interim", "tool", "delta"}) {
		t.Errorf("callbacks came %v", p.order)
	}
	// The part is what streamed, the model's trailing blank line with it.
	if len(p.interim) != 1 || strings.TrimSpace(p.interim[0]) != "I will check." {
		t.Errorf("interim %q", p.interim)
	}
	const want = "I found nothing about you burning anything last week."
	if reply.Text != want || strings.TrimSpace(p.cur.String()) != want {
		t.Errorf("reply %q, streamed after the mark %q", reply.Text, p.cur.String())
	}
}

// Silence after interim words is the reply's: the part after the mark is
// the token, though the stream as a whole does not open with it.
func TestTurnSilenceAfterInterimWords(t *testing.T) {
	f, srv := newFakeHermes(t)
	f.stream = func(string, string) string { return fixture(t, "lookup-silent.sse") }
	saved := ""
	var p streamParts
	reply, err := testClient(srv, &saved).Turn(context.Background(), p.request(Envelope("idle")))
	if err != nil {
		t.Fatal(err)
	}
	if !Silent(reply.Text) {
		t.Errorf("reply %q is not silence", reply.Text)
	}
	if after := p.cur.String(); !MaybeSilent(after) || !Silent(after) {
		t.Errorf("the part after the mark, %q, does not read as silence", after)
	}
	if whole := strings.Join(p.deltas, "") + p.cur.String(); MaybeSilent(whole) {
		t.Errorf("the whole stream %q reads as silence; the test proves nothing", whole)
	}
}

// A stream that ends without assistant.completed answers with what it
// streamed after the last mark, not the interim words before it.
func TestTurnWithoutCompletionAnswersTheLastPart(t *testing.T) {
	f, srv := newFakeHermes(t)
	f.sessions["haunt-moth"] = true
	f.stream = func(string, string) string {
		return "event: assistant.delta\ndata: {\"delta\":\"One moment.\"}\n\n" +
			"event: assistant.commentary\ndata: {\"text\":\"One moment.\",\"already_streamed\":true}\n\n" +
			"event: tool.started\ndata: {\"tool_name\":\"memory\"}\n\n" +
			"event: tool.completed\ndata: {\"tool_name\":\"memory\"}\n\n" +
			"event: assistant.delta\ndata: {\"delta\":\"\\n\\nFound it.\"}\n\n" +
			"event: done\ndata: {}\n\n"
	}
	saved := "haunt-moth"
	reply, err := testClient(srv, &saved).Turn(context.Background(), TurnRequest{Input: "x"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(reply.Text) != "Found it." {
		t.Errorf("reply %q", reply.Text)
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

// A turn hermes gives up on, its provider unreachable: failed.sse sends
// assistant.completed with hermes's own notice and completed false, then
// run.failed. The notice is not the ghost's words, and the turn fails with
// what the provider said and nothing else of the notice's.
func TestTurnReportsAFailedTurn(t *testing.T) {
	sse := strings.ReplaceAll(fixture(t, "failed.sse"), `"haunt-fake"`, `"haunt-moth"`)
	const said = `\n\nProvider said: Connection error.`
	if !strings.Contains(sse, said) {
		t.Fatal("failed.sse has no provider line")
	}
	for _, tc := range []struct {
		name, sse, want string
	}{
		{"failed.sse", sse, "hermes: the turn failed: Connection error"},
		// A notice without the provider's line, as hermes writes for a
		// failure that is not the provider's.
		{"no provider line", strings.Replace(sse, said, "", 1), "hermes: the turn failed"},
		// Advice after the provider's line is not the provider's.
		{"advice after", strings.Replace(sse, said, said+`\n\nThe connection kept dropping.`, 1), "hermes: the turn failed: Connection error"},
	} {
		f, srv := newFakeHermes(t)
		f.stream = func(string, string) string { return tc.sse }
		saved := ""
		var p streamParts
		reply, err := testClient(srv, &saved).Turn(context.Background(), p.request("Hello, are you there?"))
		if err == nil || err.Error() != tc.want {
			t.Errorf("%s: error %v, want %q", tc.name, err, tc.want)
		}
		if reply.Text != "" {
			t.Errorf("%s: the notice was taken as the reply: %q", tc.name, reply.Text)
		}
		if len(p.order) != 0 {
			t.Errorf("%s: callbacks came %v", tc.name, p.order)
		}
	}
}

// A finished turn's assistant.completed without the completed field is
// still the reply.
func TestTurnCompletedWithoutTheField(t *testing.T) {
	f, srv := newFakeHermes(t)
	f.sessions["haunt-moth"] = true
	f.stream = func(string, string) string {
		return "event: assistant.completed\ndata: {\"content\":\"hi\"}\n\nevent: run.completed\ndata: {}\n\nevent: done\ndata: {}\n\n"
	}
	saved := "haunt-moth"
	reply, err := testClient(srv, &saved).Turn(context.Background(), TurnRequest{Input: "x"})
	if err != nil || reply.Text != "hi" {
		t.Errorf("reply %q, error %v", reply.Text, err)
	}
}

// OnEvent hears every event on the stream by name, the reasoning that no
// other callback reports among them, and not the keepalive comments a
// stream carries while hermes waits.
func TestTurnReportsEveryEvent(t *testing.T) {
	f, srv := newFakeHermes(t)
	stream := fixture(t, "lookup-silent.sse")
	var want []string
	for _, line := range strings.Split(stream, "\n") {
		if name, ok := strings.CutPrefix(line, "event: "); ok {
			want = append(want, name)
		}
	}
	f.stream = func(string, string) string {
		return strings.ReplaceAll(stream, "event: tool.progress", ": keepalive\n\n: keepalive\n\nevent: tool.progress")
	}
	saved := ""
	var got []string
	_, err := testClient(srv, &saved).Turn(context.Background(), TurnRequest{
		Input:   "x",
		OnEvent: func(name string) { got = append(got, name) },
	})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(got, want) {
		t.Errorf("events heard:\n%v\nwant:\n%v", got, want)
	}
	if !slices.Contains(got, "tool.progress") {
		t.Error("the model's reasoning was not heard")
	}
}

// stoppableHermes is a session stream whose run waits, as one riding out a
// provider outage does, until it is stopped; it ends settle after the
// stop, as hermes's agent thread winds down after answering "stopping".
type stoppableHermes struct {
	mu      sync.Mutex
	stops   []string  // the runs asked to stop
	ended   time.Time // when the run was over
	status  string
	stopErr int // a status to fail the stop with
	settle  time.Duration
	stopped chan struct{}
}

func newStoppableHermes(t *testing.T) (*stoppableHermes, *httptest.Server) {
	h := &stoppableHermes{status: "running", stopped: make(chan struct{}), settle: 300 * time.Millisecond}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/sessions/{id}/chat/stream", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Write([]byte("event: run.started\ndata: {\"run_id\":\"run_abc\",\"session_id\":\"haunt-moth\"}\n\n" +
			"event: message.started\ndata: {\"run_id\":\"run_abc\",\"session_id\":\"haunt-moth\"}\n\n"))
		w.(http.Flusher).Flush()
		// Hermes does not see the client go until its next write.
		<-h.stopped
		time.Sleep(h.settle)
		h.mu.Lock()
		h.status, h.ended = "cancelled", time.Now()
		h.mu.Unlock()
	})
	mux.HandleFunc("POST /v1/runs/{id}/stop", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer sekrit" {
			t.Errorf("stop sent without the key")
		}
		h.mu.Lock()
		h.stops = append(h.stops, r.PathValue("id"))
		fail := h.stopErr
		h.mu.Unlock()
		if fail != 0 {
			w.WriteHeader(fail)
			w.Write([]byte(`{"error":{"message":"no","code":"boom"}}`))
			return
		}
		if r.PathValue("id") != "run_abc" {
			w.WriteHeader(404)
			w.Write([]byte(`{"error":{"message":"Run not found","code":"run_not_found"}}`))
			return
		}
		close(h.stopped)
		w.Write([]byte(`{"run_id":"run_abc","status":"stopping"}`))
	})
	mux.HandleFunc("GET /v1/runs/{id}", func(w http.ResponseWriter, r *http.Request) {
		h.mu.Lock()
		defer h.mu.Unlock()
		fmt.Fprintf(w, `{"object":"hermes.run","run_id":"run_abc","status":%q}`, h.status)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return h, srv
}

// A turn given up on is stopped in hermes, by the run id its stream
// carries, and Turn returns once the run is over and not before.
func TestTurnStopsTheRunGivenUpOn(t *testing.T) {
	h, srv := newStoppableHermes(t)
	saved := "haunt-moth"
	c := testClient(srv, &saved)
	var logged []string
	c.Logf = func(f string, a ...any) { logged = append(logged, fmt.Sprintf(f, a...)) }
	ctx, cancel := context.WithCancel(context.Background())
	_, err := c.Turn(ctx, TurnRequest{Input: "x", OnEvent: func(name string) {
		if name == "message.started" {
			cancel()
		}
	}})
	returned := time.Now()
	if !errors.Is(err, context.Canceled) {
		t.Errorf("error %v", err)
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if !slices.Equal(h.stops, []string{"run_abc"}) {
		t.Errorf("stops asked for %v", h.stops)
	}
	if h.ended.IsZero() || returned.Before(h.ended) {
		t.Errorf("Turn returned at %v, before the run was over at %v", returned, h.ended)
	}
	if len(logged) != 0 {
		t.Errorf("logged %q", logged)
	}
}

// A stop that fails is logged, and the turn fails as it would have.
func TestTurnStopFailing(t *testing.T) {
	h, srv := newStoppableHermes(t)
	h.stopErr = 500
	defer close(h.stopped)
	saved := "haunt-moth"
	c := testClient(srv, &saved)
	var logged []string
	c.Logf = func(f string, a ...any) { logged = append(logged, fmt.Sprintf(f, a...)) }
	ctx, cancel := context.WithCancel(context.Background())
	_, err := c.Turn(ctx, TurnRequest{Input: "x", OnEvent: func(name string) {
		if name == "message.started" {
			cancel()
		}
	}})
	if !errors.Is(err, context.Canceled) {
		t.Errorf("error %v", err)
	}
	if len(logged) != 1 || !strings.Contains(logged[0], "stopping run run_abc") || !strings.Contains(logged[0], "500") {
		t.Errorf("logged %q", logged)
	}
}

// A turn whose run hermes has said is over is not stopped.
func TestTurnOverIsNotStopped(t *testing.T) {
	f, srv := newFakeHermes(t)
	f.sessions["haunt-moth"] = true
	f.stream = func(string, string) string {
		return "event: run.started\ndata: {\"run_id\":\"run_x\"}\n\nevent: assistant.completed\ndata: {\"run_id\":\"run_x\",\"content\":\"hi\",\"completed\":true}\n\nevent: run.completed\ndata: {\"run_id\":\"run_x\"}\n\n: keepalive\n\n"
	}
	saved := "haunt-moth"
	c := testClient(srv, &saved)
	var logged []string
	c.Logf = func(f string, a ...any) { logged = append(logged, fmt.Sprintf(f, a...)) }
	ctx, cancel := context.WithCancel(context.Background())
	c.Turn(ctx, TurnRequest{Input: "x", OnEvent: func(name string) {
		if name == "run.completed" {
			cancel()
		}
	}})
	// The fake has no stop endpoint: asking would have been logged.
	if len(logged) != 0 {
		t.Errorf("logged %q", logged)
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

// Interim words are kept in the session on the message that made the call,
// and read back as interim, their pose tag taken off as the reply's is.
func TestMessagesInterimWords(t *testing.T) {
	f, srv := newFakeHermes(t)
	f.sessions["haunt-moth"] = true
	f.history = `{"object":"list","data":[
		{"role":"user","content":"Did I burn anything last week?"},
		{"role":"assistant","content":"[thinking] Let me look.","tool_calls":[
			{"id":"c1","type":"function","function":{"name":"session_search","arguments":"{\"query\": \"burned\"}"}}]},
		{"role":"tool","tool_call_id":"c1","content":"{\"results\": [\"they burned the bread on Tuesday\"]}"},
		{"role":"assistant","content":"[sad] Yes: the bread, on Tuesday."}]}`
	saved := "haunt-moth"
	msgs, err := testClient(srv, &saved).Messages(context.Background(), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 3 || msgs[1].Text != "Let me look." || !msgs[1].Interim ||
		msgs[2].Text != "Yes: the bread, on Tuesday." || msgs[2].Interim {
		t.Fatalf("messages %+v", msgs)
	}
	if len(msgs[2].Looked) != 1 || msgs[1].Looked != nil {
		t.Errorf("the lookup rides on %+v, want the reply it led to", msgs)
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

// Hermes closes a failed turn with a notice of its own in the ghost's
// place; it reads as a failed turn, not as words, and keeps the turn's
// calls.
func TestMessagesFailedTurn(t *testing.T) {
	f, srv := newFakeHermes(t)
	f.sessions["haunt-moth"] = true
	f.history = `{"data":[
		{"role":"user","content":"what did I burn?","timestamp":1789614336},
		{"role":"assistant","content":"","tool_calls":[{"id":"c1","function":{"name":"session_search","arguments":"{}"}}],"timestamp":1789614337},
		{"role":"tool","tool_call_id":"c1","content":"nothing","timestamp":1789614338},
		{"role":"assistant","content":"This turn did not complete. Some actions may already have run; verify their effects before resending.","display_kind":"failed_turn","timestamp":1789614339},
		{"role":"user","content":"hello?","timestamp":1789614340},
		{"role":"assistant","content":"Here.","timestamp":1789614341}
	]}`
	saved := "haunt-moth"
	msgs, err := testClient(srv, &saved).Messages(context.Background(), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 4 || !msgs[1].Failed || msgs[1].Text != "" || msgs[1].Role != "assistant" ||
		msgs[3].Failed || msgs[3].Text != "Here." {
		t.Fatalf("messages %+v", msgs)
	}
	if len(msgs[1].Looked) != 1 || msgs[1].Looked[0].Tool != "session_search" || msgs[1].Looked[0].Result != "nothing" {
		t.Errorf("the failed turn's calls %+v", msgs[1].Looked)
	}

	// history-failed.json is hermes's answer after a turn whose provider
	// never answered: the notice for a turn in which nothing ran.
	f.history = fixture(t, "history-failed.json")
	msgs, err = testClient(srv, &saved).Messages(context.Background(), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 2 || msgs[0].Text != "Hello, are you there?" || !msgs[1].Failed || msgs[1].Text != "" ||
		msgs[1].Looked != nil || msgs[1].Time.Unix() != 1791618802 {
		t.Fatalf("messages %+v", msgs)
	}
}

// A compacted session is read whole, and its handoff is not a turn. The
// fixtures are hermes's own answers, from its session store and API
// handler: history-compacted.json is an in-place compaction in the middle
// of a turn, after a lookup and before the reply it led to;
// history-lineage.json is a session a compression ended and the child
// that carries it on.
func TestMessagesAcrossCompaction(t *testing.T) {
	for _, tc := range []struct {
		fixture string
		want    []Message
	}{
		{"history-compacted.json", []Message{
			{Role: "user", Text: "question 1"},
			{Role: "assistant", Text: "answer 1", Acted: []Lookup{{Tool: "face", Args: `{"pose": "happy"}`, Result: `{"result":"ok"}`}}},
			{Role: "user", Text: "what did I burn last week?"},
			{Role: "assistant", Text: "The bread.",
				Looked: []Lookup{{Tool: "session_search", Args: `{"query": "burn"}`, Result: `{"results": ["the bread"]}`}},
				Acted:  []Lookup{{Tool: "face", Args: `{"pose": "sad"}`, Result: `{"result":"ok"}`}}},
		}},
		{"history-lineage.json", []Message{
			{Role: "user", Text: "question 1"},
			{Role: "assistant", Text: "answer 1"},
			{Role: "user", Text: "question 2"},
			{Role: "assistant", Text: "answer 2"},
			{Role: "user", Text: "question 3"},
			{Role: "assistant", Text: "answer 3"},
		}},
	} {
		f, srv := newFakeHermes(t)
		f.sessions["haunt-moth"] = true
		f.history = fixture(t, tc.fixture)
		saved := "haunt-moth"
		msgs, err := testClient(srv, &saved).Messages(context.Background(), 10)
		if err != nil {
			t.Fatal(err)
		}
		var got []string
		for _, m := range msgs {
			got = append(got, m.Role+": "+m.Text)
		}
		var want []string
		for _, m := range tc.want {
			want = append(want, m.Role+": "+m.Text)
		}
		if !slices.Equal(got, want) {
			t.Errorf("%s: messages\n%q\nwant\n%q", tc.fixture, got, want)
			continue
		}
		for i, w := range tc.want {
			if w.Looked != nil && !slices.Equal(msgs[i].Looked, w.Looked) || w.Acted != nil && !slices.Equal(msgs[i].Acted, w.Acted) {
				t.Errorf("%s: %q looked %+v acted %+v, want %+v and %+v", tc.fixture, w.Text, msgs[i].Looked, msgs[i].Acted, w.Looked, w.Acted)
			}
		}
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
	if gw, _ := doc["gateway"].(map[string]any); gw["standalone"] != true {
		t.Errorf("gateway = %v, want standalone: true", doc["gateway"])
	}
	doc["_config_version"] = configVersion + 1 // hermes migrated it
	doc["model"] = map[string]any{"default": "their-model"}
	doc["platform_toolsets"] = map[string]any{"api_server": []string{"terminal"}}
	doc["agent"].(map[string]any)["auto_recovery_cycles"] = 5
	doc["skills"] = map[string]any{"inline_shell": true}
	doc["security"] = map[string]any{"allow_lazy_installs": true}
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
	if a := doc["agent"].(map[string]any); a["auto_recovery_cycles"] != autoRecoveryCycles {
		t.Errorf("auto_recovery_cycles = %v, want %d", a["auto_recovery_cycles"], autoRecoveryCycles)
	}
	if s := doc["skills"].(map[string]any); s["inline_shell"] != false {
		t.Errorf("inline_shell = %v, want false", s["inline_shell"])
	}
	if s := doc["security"].(map[string]any); s["allow_lazy_installs"] != false {
		t.Errorf("allow_lazy_installs = %v, want false", s["allow_lazy_installs"])
	}
	if tg := doc["auxiliary"].(map[string]any)["title_generation"].(map[string]any); tg["model_upgrade_enabled"] != false {
		t.Errorf("title_generation = %v, want model_upgrade_enabled false", tg)
	}
	srv := doc["mcp_servers"].(map[string]any)["haunt"].(map[string]any)
	if srv["command"] != "/usr/bin/haunt" || srv["timeout"] != 600 || srv["env"].(map[string]any)["HAUNT_RUNTIME_DIR"] != "/run/x" {
		t.Errorf("mcp server entry %v", srv)
	}
	if _, ok := doc["memory"]; ok {
		t.Errorf("haunt wrote a memory section: %v", doc["memory"])
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

	// Memory is hermes's: the backend the person chose with hermes stays.
	doc["memory"] = map[string]any{"provider": "holographic", "memory_enabled": false}
	out, _ = yaml.Marshal(doc)
	if err := os.WriteFile(cfgPath, out, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := c.EnsureProfile(spec); err != nil {
		t.Fatal(err)
	}
	raw, _ = os.ReadFile(cfgPath)
	doc = nil
	yaml.Unmarshal(raw, &doc)
	if m, _ := doc["memory"].(map[string]any); len(m) != 2 || m["provider"] != "holographic" || m["memory_enabled"] != false {
		t.Errorf("the person's memory settings were changed: %v", doc["memory"])
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
		"clips/SKILL.md":              "---\nname: clips\ndescription: z\ndeps: [ffmpeg, ' ', yt-dlp]\n---\n",
		"one-dep/SKILL.md":            "---\nname: one-dep\ndescription: z\ndeps: ripgrep\n---\n",
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
		summary = append(summary, sk.Source+":"+sk.Name+":"+strings.Join(sk.Assumes, "+")+":"+strings.Join(sk.Deps, "+"))
	}
	wantList := []string{"learned:clips::ffmpeg+yt-dlp", "hermes:hermes-agent::", "learned:one-dep::ripgrep",
		"learned:reminders::", "learned:shell:terminal+read_file:", "haunt:focus-session::", "haunt:morning-check-in::"}
	if !slices.Equal(summary, wantList) {
		t.Errorf("Skills = %v, want %v", summary, wantList)
	}
	if list[3].Description != "How they like reminders." || list[3].Dir != filepath.Join(local, "reminders") {
		t.Errorf("learned skill = %+v", list[3])
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
