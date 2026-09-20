package backend

import (
	"github.com/aaronjauregui/haunt/internal/hlog"

	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// fakeVision serves an OpenAI-style (non-streaming) chat completion that
// answers every picture with the same sentence.
func fakeVision(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			http.NotFound(w, r)
			return
		}
		fmt.Fprint(w, `{"choices":[{"message":{"content":"a text editor"}}]}`)
	}))
}

func TestLookSpendsItsOwnBudget(t *testing.T) {
	srv := fakeVision(t)
	defer srv.Close()
	s := NewService(&Config{
		Vision: VisionConfig{BaseURL: srv.URL + "/v1", Model: "fake"},
		Budget: BudgetConfig{VisionRequestsPerHour: 1, ImageRequestsPerHour: 1},
	}, nil)
	got, err := s.Look(context.Background(), "what is this?", []byte("png"))
	if err != nil {
		t.Fatal(err)
	}
	if got != "a text editor" {
		t.Errorf("look = %q", got)
	}
	if _, err := s.Look(context.Background(), "and now?", []byte("png")); err == nil {
		t.Fatal("second look should exceed the vision budget")
	}
}

// Describing a picture the ghost painted comes out of the same ration as
// looking at the screen: it is the same model doing the same work.
func TestDescribeImageSharesTheVisionBudget(t *testing.T) {
	srv := fakeVision(t)
	defer srv.Close()
	s := NewService(&Config{
		Vision: VisionConfig{BaseURL: srv.URL + "/v1", Model: "fake"},
		Budget: BudgetConfig{VisionRequestsPerHour: 2},
	}, nil)
	got, err := s.DescribeImage(context.Background(), []byte("png"), "what is it?")
	if err != nil {
		t.Fatal(err)
	}
	if got != "a text editor" {
		t.Errorf("describe = %q", got)
	}
	if _, err := s.Look(context.Background(), "and the screen?", []byte("png")); err != nil {
		t.Fatalf("the second call of two was refused: %v", err)
	}
	if _, err := s.DescribeImage(context.Background(), []byte("png"), "again?"); err == nil {
		t.Error("a third call went through an allowance of two")
	}

	unconfigured := NewService(&Config{}, nil)
	if _, err := unconfigured.DescribeImage(context.Background(), []byte("png"), "?"); err == nil {
		t.Error("describing without a vision backend should fail")
	}
}

func TestBudgetWindowSlides(t *testing.T) {
	b := newBudget(2)
	now := time.Now()
	b.now = func() time.Time { return now }
	if err := b.spend(); err != nil {
		t.Fatal(err)
	}
	if err := b.spend(); err != nil {
		t.Fatal(err)
	}
	if err := b.spend(); err == nil {
		t.Fatal("third spend should fail")
	}
	now = now.Add(61 * time.Minute)
	if err := b.spend(); err != nil {
		t.Fatalf("spend after window slid: %v", err)
	}
}

func TestUnconfiguredBackendsError(t *testing.T) {
	s := NewService(&Config{}, nil)
	if _, err := s.GenerateImage(context.Background(), "x"); err == nil {
		t.Error("image without backend should fail")
	}
	if _, err := s.Look(context.Background(), "x", nil); err == nil {
		t.Error("look without backend should fail")
	}
	// A model with no endpoint is not a vision backend: there is no
	// dialogue endpoint left to borrow.
	s = NewService(&Config{Vision: VisionConfig{Model: "fake"}}, nil)
	if s.VisionEnabled() {
		t.Error("vision enabled without a base_url")
	}
}

func TestGenerateImage(t *testing.T) {
	payload := []byte("fake-png-bytes")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/images/generations" {
			http.NotFound(w, r)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]string{{"b64_json": base64.StdEncoding.EncodeToString(payload)}},
		})
	}))
	defer srv.Close()

	s := NewService(&Config{
		Image:  ImageConfig{BaseURL: srv.URL + "/v1", Model: "fake"},
		Budget: BudgetConfig{ImageRequestsPerHour: 5},
	}, nil)
	got, err := s.GenerateImage(context.Background(), "a ghost")
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(payload) {
		t.Errorf("image bytes = %q", got)
	}
}

// TestPromptTracing: requests stay out of the log until asked for, and then
// show what was sent.
func TestPromptTracing(t *testing.T) {
	srv := fakeVision(t)
	defer srv.Close()
	newSvc := func(logPrompts bool) (*Service, func() []string) {
		// With the prompt topic off unless the switch is on, so this also
		// checks that tracing stays off in the shipping configuration.
		rec, lines := hlog.Recorder()
		if !logPrompts {
			rec, lines = hlog.RecorderAt(hlog.LevelInfo)
		}
		return NewService(&Config{
			Vision:     VisionConfig{BaseURL: srv.URL + "/v1", Model: "fake"},
			LogPrompts: logPrompts,
		}, rec), lines
	}

	s, quiet := newSvc(false)
	if _, err := s.Look(context.Background(), "what are they doing?", []byte("png")); err != nil {
		t.Fatal(err)
	}
	if got := quiet(); len(got) != 0 {
		t.Errorf("tracing off still logged: %q", got)
	}

	s, loud := newSvc(true)
	if _, err := s.Look(context.Background(), "what are they doing?", []byte("png")); err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(loud(), "\n")
	for _, want := range []string{"model=fake", "[question] what are they doing?", "a text editor"} {
		if !strings.Contains(joined, want) {
			t.Errorf("trace missing %q; got:\n%s", want, joined)
		}
	}

	// Asking for the prompt topic turns tracing on without touching the
	// config. (That HAUNT_BACKEND_DEBUG=1 still selects that topic is
	// internal/hlog's business, and tested there.)
	byTopic, viaTopic := hlog.RecorderAt(hlog.LevelInfo, hlog.Prompt)
	s = NewService(&Config{Vision: VisionConfig{BaseURL: srv.URL + "/v1", Model: "fake"}}, byTopic)
	if _, err := s.Look(context.Background(), "x", []byte("png")); err != nil {
		t.Fatal(err)
	}
	if len(viaTopic()) == 0 {
		t.Error("the prompt topic produced no trace")
	}
}

// The tool allowance is its own: acting rations something other than
// pictures, and one turn can contain several actions.
func TestToolBudgetIsItsOwn(t *testing.T) {
	s := NewService(&Config{
		Budget: BudgetConfig{ImageRequestsPerHour: 10, ToolRequestsPerHour: 2},
	}, nil)
	for i := range 2 {
		if err := s.SpendToolCall(); err != nil {
			t.Fatalf("call %d was refused: %v", i+1, err)
		}
	}
	if err := s.SpendToolCall(); err == nil {
		t.Error("a third call went through an allowance of two")
	}
}
