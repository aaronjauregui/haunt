GO ?= go

.PHONY: build test vet fmt demo demo-headless clean

build:
	$(GO) build ./...

test:
	$(GO) test ./...

vet:
	$(GO) vet ./...

fmt:
	gofmt -l -w .

# Opens a nested sway window in your current session with the PoC ghost
# inside it. Close the window (or Ctrl-C) to stop.
demo:
	./scripts/demo.sh

# Same, but headless: no visible window, used to smoke-test the demo plumbing.
demo-headless:
	WLR_BACKENDS=headless ./scripts/demo.sh

clean:
	rm -f haunt-poc
	$(GO) clean
