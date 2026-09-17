package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aaronjauregui/haunt/internal/testenv"
)

// syncBuffer collects the daemon's stderr while the test reads it.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	os.Stderr.Write(p)
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// waitRecordCount polls until the recording holds want occurrences of s.
func waitRecordCount(t *testing.T, path, s string, want int) {
	t.Helper()
	for i := 0; i < 100; i++ {
		data, _ := os.ReadFile(path)
		if n := strings.Count(string(data), s); n >= want {
			if n > want {
				t.Fatalf("recording has %d occurrences of %q, want %d", n, s, want)
			}
			return
		}
		wait100ms()
	}
	data, _ := os.ReadFile(path)
	t.Fatalf("recording never held %d occurrences of %q; have:\n%s", want, s, data)
}

// waitFileCount polls until glob matches want files.
func waitFileCount(t *testing.T, glob string, want int) {
	t.Helper()
	for i := 0; i < 100; i++ {
		if m, err := filepath.Glob(glob); err == nil && len(m) == want {
			return
		}
		wait100ms()
	}
	m, _ := filepath.Glob(glob)
	t.Fatalf("%s: %d files, want %d", glob, len(m), want)
}

// waitOrange polls for the generated image's orange pixels beside the sprite.
func waitOrange(t *testing.T, c *testenv.Compositor, dir string, spriteX, spriteY int) {
	t.Helper()
	path := filepath.Join(dir, "orange-poll.png")
	for i := 0; i < 100; i++ {
		img := shot(t, c, path)
		for y := spriteY; y < spriteY+200; y++ {
			for x := spriteX + 160; x < min(spriteX+560, img.Bounds().Max.X); x++ {
				r, g, b, _ := img.At(x, y).RGBA()
				if r>>8 > 200 && g>>8 > 90 && g>>8 < 170 && b>>8 < 60 {
					return
				}
			}
		}
		wait100ms()
	}
	t.Fatal("generated orange image never appeared")
}

func wait100ms() { time.Sleep(100 * time.Millisecond) }

// waitFileContains polls a file a tool server writes to, which is how a
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

// waitFake waits until the fake hermes has logged want entries matching.
func waitFake(t *testing.T, path string, want int, d time.Duration, match func(fakeEntry) bool) []fakeEntry {
	t.Helper()
	deadline := time.Now().Add(d)
	for {
		got := fakeEntries(path, match)
		if len(got) >= want {
			return got
		}
		if time.Now().After(deadline) {
			data, _ := os.ReadFile(path)
			t.Fatalf("fakehermes logged %d matching entries, want %d; its log:\n%s", len(got), want, data)
		}
		time.Sleep(100 * time.Millisecond)
	}
}
