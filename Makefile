GO ?= go

.PHONY: build test vet fmt eval eval-baseline demo demo-ai demo-troupe demo-seriko demo-headless install-user clean

build:
	$(GO) build ./...
	$(GO) build -o haunt ./cmd/haunt
	$(GO) build -o haunt-seriko ./cmd/haunt-seriko

test:
	$(GO) test ./...

vet:
	$(GO) vet ./...

fmt:
	gofmt -l -w .

# The evals: wisp on the real model in [hermes] / [hermes.model], each case
# run EVAL_TRIALS times (default 3) four at a time, graded by code and by
# `claude -p` on a Claude subscription. Spends quota; not part of `test`.
# Results and report.md go to .eval/<timestamp>/ (EVAL_OUT moves .eval).
# EVAL_CASES=focus|reminder picks cases, EVAL_JUDGE=off skips the judge,
# EVAL_STRICT=1 fails on a drop of more than one trial below the baseline
# or on a case skipped for a missing prerequisite. -count=1 because a
# cached eval would be no eval at all.
EVAL_OUT ?= .eval
export EVAL_OUT

eval:
	$(GO) test -tags eval -count=1 -run '^TestEval$$' -parallel 4 -timeout 120m ./cmd/haunt; \
	status=$$?; \
	latest=$$(ls -1 "$(EVAL_OUT)" 2>/dev/null | grep '^[0-9]' | sort | tail -n 1); \
	if [ -n "$$latest" ]; then echo "report: $(EVAL_OUT)/$$latest/report.md"; fi; \
	exit $$status

# Keep the newest run's results as the baseline later runs are compared
# with. A baseline is per model, and stays local.
eval-baseline:
	@latest=$$(ls -1 "$(EVAL_OUT)" 2>/dev/null | grep '^[0-9]' | sort | tail -n 1); \
	if [ -z "$$latest" ]; then echo "no run under $(EVAL_OUT)/: make eval first"; exit 2; fi; \
	cp "$(EVAL_OUT)/$$latest/results.json" "$(EVAL_OUT)/baseline.json"; \
	echo "baseline: $(EVAL_OUT)/$$latest/results.json"

# Opens a nested sway window in your current session with the whisper
# ghost inside it. Close the window (or Ctrl-C) to stop.
demo:
	./scripts/demo.sh

# The AI ghost: a persona and no brain at all, run by hermes-agent
# (docs/user/hermes.md: hermes in its container
# via scripts/hermes-docker, a model in [hermes.model]). Its hermes profile
# lives in the demo's workdir and goes with it. Draws via your image backend
# ([ai.image] in ~/.config/haunt/config.toml).
# IDLE_MS=10000 shortens the wait before it speaks unprompted.
demo-ai:
	GHOST=wisp ./scripts/demo.sh

# Two ghosts at once, in one nested sway. Whisper is scripted and wisp runs
# on hermes, so they behave differently — which is the point.
# Watch for: the second one opens beside the first rather than on top of it,
# and from another terminal `haunt say "hello"` with no -ghost goes to
# whichever you last clicked. `haunt say -ghost wisp ...` addresses one.
demo-troupe:
	GHOST="whisper wisp" ./scripts/demo.sh

# Try a ukagaka shell without installing anything. SERIKO takes a path or a
# URL to a .nar or a shell directory; it is fetched, converted and run
# inside a throwaway directory, and the ghost's memory goes there too, so
# nothing lands in your library.
#   make demo-seriko SERIKO=~/Downloads/some_ghost.nar
#   make demo-seriko SERIKO=https://example.org/some_ghost.nar
# SERIKO_ARGS passes flags through, e.g. SERIKO_ARGS="-only 0,1,2 -surface 0".
demo-seriko:
	@test -n "$(SERIKO)" || { echo "set SERIKO=<path or URL to a .nar or shell dir>"; exit 2; }
	SERIKO="$(SERIKO)" SERIKO_ARGS="$(SERIKO_ARGS)" ./scripts/demo.sh

# Same, but headless: no visible window, used to smoke-test the demo plumbing.
demo-headless:
	WLR_BACKENDS=headless ./scripts/demo.sh

# Install for daily use: the binary on your PATH and a systemd user unit,
# so the ghost is there when you log in. See dist/README.md.
install-user: build
	install -Dm755 haunt $(HOME)/.local/bin/haunt
	install -Dm755 haunt-seriko $(HOME)/.local/bin/haunt-seriko
	install -Dm644 dist/haunt.service $(HOME)/.config/systemd/user/haunt.service
	systemctl --user daemon-reload
	@echo
	@echo "installed. start it with:  systemctl --user enable --now haunt"

clean:
	rm -f haunt haunt-seriko
	$(GO) clean
