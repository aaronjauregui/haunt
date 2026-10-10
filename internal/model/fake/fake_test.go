package fake

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

func post(t *testing.T, s *Server, body string) string {
	t.Helper()
	resp, err := http.Post(s.BaseURL()+"/chat/completions", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return string(b)
}

// argFragments is the non-empty argument fragments in a raw stream.
func argFragments(stream string) []string {
	var out []string
	for _, line := range strings.Split(stream, "\n") {
		data, ok := strings.CutPrefix(line, "data: ")
		if !ok {
			continue
		}
		var c struct {
			Choices []struct {
				Delta struct {
					ToolCalls []struct {
						Function struct {
							Arguments string `json:"arguments"`
						} `json:"function"`
					} `json:"tool_calls"`
				} `json:"delta"`
			} `json:"choices"`
		}
		json.Unmarshal([]byte(data), &c)
		for _, ch := range c.Choices {
			for _, tc := range ch.Delta.ToolCalls {
				if tc.Function.Arguments != "" {
					out = append(out, tc.Function.Arguments)
				}
			}
		}
	}
	return out
}

func TestArgFrames(t *testing.T) {
	args := `{"text":"check the oven","in":"10m"}`
	s := NewT(t, Rule{Calls: []Call{{Name: "intend", Args: json.RawMessage(args)}}})
	out := post(t, s, `{"messages":[{"role":"user","content":"hi"}]}`)
	frags := argFragments(out)
	if len(frags) != 3 || strings.Join(frags, "") != args {
		t.Errorf("fragments = %q", frags)
	}
	if !strings.HasSuffix(out, "data: [DONE]\n\n") {
		t.Error("no [DONE]")
	}
}

// Rules answer by the last user message and by whether a tool result
// is what the request ends with; a prefill does not count as the end.
func TestPick(t *testing.T) {
	s := NewT(t,
		Rule{Match: "oven", AfterTool: "saved", Reply: "after-saved"},
		Rule{Match: "oven", AfterTool: "*", Reply: "after-any"},
		Rule{Match: "oven", Reply: "fresh"},
		Rule{Reply: "default"},
	)
	for _, tc := range []struct{ msgs, want string }{
		{`[{"role":"user","content":"the OVEN"}]`, "fresh"},
		{`[{"role":"user","content":"the oven"},{"role":"assistant","content":null},{"role":"tool","content":"saved it"}]`, "after-saved"},
		{`[{"role":"user","content":"the oven"},{"role":"assistant","content":null},{"role":"tool","content":"no"}]`, "after-any"},
		{`[{"role":"user","content":"the oven"},{"role":"assistant","content":null},{"role":"tool","content":"saved"},{"role":"assistant","content":"Well, "}]`, "after-saved"},
		{`[{"role":"user","content":"the oven"},{"role":"assistant","content":"Well, "}]`, "fresh"},
		{`[{"role":"user","content":"hello"}]`, "default"},
	} {
		out := post(t, s, `{"messages":`+tc.msgs+`}`)
		var got strings.Builder
		for _, line := range strings.Split(out, "\n") {
			data, ok := strings.CutPrefix(line, "data: ")
			if !ok {
				continue
			}
			var c struct {
				Choices []struct {
					Delta struct {
						Content string `json:"content"`
					} `json:"delta"`
				} `json:"choices"`
			}
			json.Unmarshal([]byte(data), &c)
			for _, ch := range c.Choices {
				got.WriteString(ch.Delta.Content)
			}
		}
		if got.String() != tc.want {
			t.Errorf("%s: answered %q, want %q", tc.msgs, got.String(), tc.want)
		}
	}
	if n := len(s.Received()); n != 6 {
		t.Errorf("received %d, want 6", n)
	}
}

func TestStatus(t *testing.T) {
	s := NewT(t, Rule{Status: 429, RetryAfter: "3", Reply: "slow down"})
	resp, err := http.Post(s.BaseURL()+"/chat/completions", "application/json", strings.NewReader(`{"messages":[]}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 429 || resp.Header.Get("Retry-After") != "3" || !strings.Contains(string(b), "slow down") {
		t.Errorf("%d %q %s", resp.StatusCode, resp.Header.Get("Retry-After"), b)
	}
}
