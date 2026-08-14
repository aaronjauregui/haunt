package ghost

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"image/png"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/aaronjauregui/haunt/internal/ai"
	"github.com/aaronjauregui/haunt/internal/proto"
)

func bytesReader(b []byte) io.Reader { return bytes.NewReader(b) }

// internalKeyPrefix marks driver-owned store keys hidden from brains.
const internalKeyPrefix = "_haunt/"

// visibleKV strips driver-internal keys from the boot snapshot.
func visibleKV(all map[string]string) map[string]string {
	out := map[string]string{}
	for k, v := range all {
		if !strings.HasPrefix(k, internalKeyPrefix) {
			out[k] = v
		}
	}
	return out
}

const historyKey = "_haunt/chat_history"

// execChat handles a chat intent: persona + history + the new utterance go
// to the LLM; the reply streams into the balloon and lands as an ai_reply
// event. Runs on its own goroutine so a slow backend never blocks intents.
func (g *Ghost) execChat(text string) {
	if !g.capabilityActive("llm") {
		g.publishAIError("llm capability not granted")
		return
	}
	if g.aiSvc == nil || !g.aiSvc.LLMEnabled() {
		g.publishAIError("no LLM backend configured in the haunt config")
		return
	}

	history := g.loadHistory()
	msgs := make([]ai.Message, 0, len(history)+2)
	system := g.manifest.Persona.System
	if system == "" {
		system = "You are " + g.manifest.Ghost.Name + ", a small desktop companion. Reply in one or two short sentences."
	}
	msgs = append(msgs, ai.Message{Role: "system", Content: system})
	msgs = append(msgs, history...)
	msgs = append(msgs, ai.Message{Role: "user", Content: text})

	// Stream into the balloon, re-rendering at a gentle pace.
	var acc string
	last := time.Now()
	full, err := g.aiSvc.Chat(context.Background(), msgs, func(delta string) {
		acc += delta
		if time.Since(last) >= 120*time.Millisecond {
			last = time.Now()
			g.showBalloon(acc)
		}
	})
	if err != nil {
		g.opts.Log(fmt.Sprintf("haunt: chat: %v", err))
		g.publishAIError(err.Error())
		return
	}
	g.showBalloon(full)

	turns := g.manifest.Persona.HistoryTurns
	if turns <= 0 {
		turns = 12
	}
	history = append(history,
		ai.Message{Role: "user", Content: text},
		ai.Message{Role: "assistant", Content: full})
	if max := turns * 2; len(history) > max {
		history = history[len(history)-max:]
	}
	g.saveHistory(history)

	g.publish(proto.Event{Type: proto.EventAIReply, Text: full})
}

// execImagine handles an imagine intent: generate (or reuse from cache) an
// image and show it as a balloon.
func (g *Ghost) execImagine(prompt string) {
	if !g.capabilityActive("imagine") {
		g.publishAIError("imagine capability not granted")
		return
	}
	if g.aiSvc == nil || !g.aiSvc.ImageEnabled() {
		g.publishAIError("no image backend configured in the haunt config")
		return
	}

	// Content-addressed cache: same backend/model/prompt = same file.
	key := sha256.Sum256([]byte(g.aiSvc.ImageCacheKey() + "\x00" + prompt))
	cachePath := filepath.Join(g.cacheDir, hex.EncodeToString(key[:8])+".png")
	data, err := os.ReadFile(cachePath)
	if err != nil {
		if data, err = g.aiSvc.GenerateImage(context.Background(), prompt); err != nil {
			g.opts.Log(fmt.Sprintf("haunt: imagine: %v", err))
			g.publishAIError(err.Error())
			return
		}
		if err := os.MkdirAll(g.cacheDir, 0o755); err == nil {
			os.WriteFile(cachePath, data, 0o644)
		}
	}

	img, err := png.Decode(bytesReader(data))
	if err != nil {
		g.publishAIError("image backend returned invalid PNG: " + err.Error())
		return
	}
	g.showImageBalloon(img)
	g.publish(proto.Event{Type: proto.EventAIImage, Name: cachePath})
}

func (g *Ghost) publishAIError(msg string) {
	g.publish(proto.Event{Type: proto.EventAIError, Text: msg})
}

func (g *Ghost) loadHistory() []ai.Message {
	raw, ok, err := storeGet(g.store, historyKey)
	if err != nil || !ok {
		return nil
	}
	var h []ai.Message
	if json.Unmarshal([]byte(raw), &h) != nil {
		return nil
	}
	return h
}

func (g *Ghost) saveHistory(h []ai.Message) {
	b, err := json.Marshal(h)
	if err != nil {
		return
	}
	if err := g.store.Set(historyKey, string(b)); err != nil {
		g.opts.Log(fmt.Sprintf("haunt: saving chat history: %v", err))
	}
}

// storeGet adapts the narrow Store interface (which lacks Get) by scanning
// All; history is one key so this stays cheap.
func storeGet(s Store, key string) (string, bool, error) {
	all, err := s.All()
	if err != nil {
		return "", false, err
	}
	v, ok := all[key]
	return v, ok, nil
}
