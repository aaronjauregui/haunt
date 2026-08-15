GO ?= go

.PHONY: build test vet fmt demo demo-ai demo-headless install-user clean

build:
	$(GO) build ./...
	$(GO) build -o haunt ./cmd/haunt

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

# Install for daily use: the binary on your PATH and a systemd user unit,
# so the ghost is there when you log in. See dist/README.md.
install-user: build
	install -Dm755 haunt $(HOME)/.local/bin/haunt
	install -Dm644 dist/haunt.service $(HOME)/.config/systemd/user/haunt.service
	systemctl --user daemon-reload
	@echo
	@echo "installed. start it with:  systemctl --user enable --now haunt"

clean:
	rm -f haunt haunt-poc
	$(GO) clean
