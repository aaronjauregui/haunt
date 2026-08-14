GO ?= go

.PHONY: build test vet fmt demo demo-ai demo-headless clean

build:
	$(GO) build ./...

test:
	$(GO) test ./...

vet:
	$(GO) vet ./...

fmt:
	gofmt -l -w .

# Opens a nested sway window in your current session with the whisper
# ghost inside it. Close the window (or Ctrl-C) to stop.
demo:
	./scripts/demo.sh

# The AI ghost: chats via your configured LLM and draws via your image
# backend ([ai.llm]/[ai.image] in ~/.config/haunt/config.toml).
# IDLE_MS=10000 shortens the wait before it speaks unprompted.
demo-ai:
	GHOST=muse ./scripts/demo.sh

# Same, but headless: no visible window, used to smoke-test the demo plumbing.
demo-headless:
	WLR_BACKENDS=headless ./scripts/demo.sh

clean:
	rm -f haunt-poc
	$(GO) clean
