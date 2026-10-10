package model

import (
	"testing"
	"time"
)

func TestTrust(t *testing.T) {
	for _, tc := range []struct {
		url, trust string
		local      bool
	}{
		{"http://127.0.0.1:8080/v1", "", true},
		{"http://localhost:8080/v1", "", true},
		{"http://LOCALHOST./v1", "", true},
		{"http://10.0.0.5:8080/v1", "", true},
		{"http://192.168.1.2/v1", "", true},
		{"http://172.16.4.4/v1", "", true},
		{"http://[::1]:8080/v1", "", true},
		{"http://[fd00::5]/v1", "", true},
		{"https://api.z.ai/api/paas/v4", "", false},
		{"https://openrouter.ai/api/v1", "", false},
		{"http://172.32.0.1/v1", "", false},
		{"http://8.8.8.8/v1", "", false},
		{"", "", false},
		{"https://api.z.ai/api/paas/v4", "local", true},
		{"http://127.0.0.1:8080/v1", "cloud", false},
	} {
		c := Config{BaseURL: tc.url, Trust: tc.trust}
		if got := c.IsLocal(); got != tc.local {
			t.Errorf("IsLocal(%q, trust %q) = %v, want %v", tc.url, tc.trust, got, tc.local)
		}
	}
}

func TestFold(t *testing.T) {
	yes, no := true, false
	for _, tc := range []struct {
		url  string
		over *bool
		want bool
	}{
		{"http://127.0.0.1:8080/v1", nil, false},
		{"https://api.z.ai/api/paas/v4", nil, true},
		{"http://127.0.0.1:8080/v1", &yes, true},
		{"https://api.z.ai/api/paas/v4", &no, false},
	} {
		c := Config{BaseURL: tc.url, FoldOverride: tc.over}
		if got := c.Fold(); got != tc.want {
			t.Errorf("Fold(%q, %v) = %v, want %v", tc.url, tc.over, got, tc.want)
		}
	}
}

func TestOver(t *testing.T) {
	f := func(v float64) *float64 { return &v }
	base := Config{
		BaseURL: "http://127.0.0.1:8080/v1", Name: "gemma", ContextLength: 32768,
		Thinking: ThinkingLow, Sampling: Sampling{Temperature: f(0.7), MinP: f(0.05)},
	}
	got := base.Over(Config{Name: "qwen", Sampling: Sampling{Temperature: f(1.1)}})
	if got.Name != "qwen" || got.BaseURL != base.BaseURL || got.ContextLength != 32768 || got.Thinking != ThinkingLow {
		t.Errorf("Over = %+v", got)
	}
	if *got.Sampling.Temperature != 1.1 || *got.Sampling.MinP != 0.05 {
		t.Errorf("sampling = temperature %v, min_p %v; want 1.1, 0.05", *got.Sampling.Temperature, *got.Sampling.MinP)
	}
	if base.Name != "gemma" || *base.Sampling.Temperature != 0.7 {
		t.Error("Over changed its receiver")
	}
}

func TestDeadlineDefaults(t *testing.T) {
	c := Config{BaseURL: "https://api.z.ai/api/paas/v4"}
	if c.FirstToken() != 30*time.Second || c.Stall() != DefaultStall {
		t.Errorf("cloud defaults = %v, %v", c.FirstToken(), c.Stall())
	}
	if l := (Config{BaseURL: "http://127.0.0.1:8080/v1"}); l.FirstToken() != 120*time.Second {
		t.Errorf("local first-token default = %v", l.FirstToken())
	}
	c.FirstTokenSeconds, c.StallSeconds = 5, 7
	if c.FirstToken().Seconds() != 5 || c.Stall().Seconds() != 7 {
		t.Errorf("set = %v, %v", c.FirstToken(), c.Stall())
	}
}
