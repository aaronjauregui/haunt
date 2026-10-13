GO ?= go

.PHONY: build test vet fmt eval eval-baseline eval-test demo demo-ai demo-ember demo-troupe demo-seriko demo-headless demo-record install-user clean

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

# The evals: wisp on the real model in the config's [model], each case
# run EVAL_TRIALS times (default 5) four at a time, graded by code and by
# `claude -p` on a Claude subscription. Spends quota; not part of `test`.
# Results and report.md go to .eval/<timestamp>/ (EVAL_OUT moves .eval).
# EVAL_CASES=focus|reminder picks cases, EVAL_JUDGE=off skips the judge,
# EVAL_STRICT=1 fails when a case is more than one trial below the baseline's
# rate and that rate is above the run's 95% interval, or on a case skipped
# for a missing prerequisite. -count=1 because a
# cached eval would be no eval at all. The test binary gives every test
# an empty config unless HAUNT_CONFIG_DIR is set, and the evals run on the
# person's own, so the recipe sets it (to the person's, unless given).
EVAL_OUT ?= .eval
export EVAL_OUT

eval:
	HAUNT_CONFIG_DIR="$${HAUNT_CONFIG_DIR:-$(or $(XDG_CONFIG_HOME),$(HOME)/.config)/haunt}" \
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

# The eval harness's own tests: the grading, the report, the judge's call
# against a stub `claude`, and the rig on the fake model in a headless
# sway. No real model, no judge, nothing off the machine, so CI runs them.
# TestEval and TestRubricAnchors spend quota, and are left out.
EVAL_TEST_RUN = ^(TestEvalHarness.*|TestEvalFaceAndPace|TestTurnsOfKind|TestTraceRecordIsTraceTurn|TestJudge.*|TestRig.*|TestEvalRigMultiday|TestMultidayAnchors)$$

eval-test:
	$(GO) test -tags eval -count=1 -run '$(EVAL_TEST_RUN)' -timeout 10m ./cmd/haunt

# Opens a nested sway window in your current session with the whisper
# ghost inside it. Close the window (or Ctrl-C) to stop.
demo:
	./scripts/demo.sh

# The AI ghost: a persona and no brain at all, thinking with the model in
# [model] in ~/.config/haunt/config.toml (docs/user/model.md). Its memory
# and transcript live in the demo's workdir and go with it. Draws via your
# image backend ([backend.image]).
# IDLE_MS=10000 shortens the wait before it speaks unprompted.
demo-ai:
	GHOST=wisp ./scripts/demo.sh

# The second persona ghost, examples/ember: warm and talkative where wisp
# is dry and short, on the same [model]. The guide to building a character
# on a local model (docs/ghosts/local-models.md) is written against it.
demo-ember:
	GHOST=ember ./scripts/demo.sh

# Two ghosts at once, in one nested sway. Whisper is scripted and wisp
# thinks with your model, so they behave differently — which is the point.
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

# Record the README's GIF: runs the demo, captures the nested sway window
# and encodes it. Needs wf-recorder and ffmpeg. Recording starts and stops
# on Enter, so a persona ghost's slow wake stays out of the take.
#   make demo-record GHOST=wisp
# The capture is kept beside the GIF, so a take is recut without being
# performed again. CROP keeps the balloon's text at a readable size and
# SEGMENTS drops the dead time; scripts/record.sh says why for both. The
# GIF in the README is this take, whose shape is worth keeping: dragged,
# right-clicked and typed at, answered, asked again from the menu,
# answered again.
#   make demo-record RAW=docs/media/demo-raw.mp4 \
#       CROP=580:300:180:100 \
#       SEGMENTS=7.90-10.20,11.90-12.80,14.40-15.10,16.30-19.60,\
#                26.35-29.30,31.38-34.60,41.00-45.40
# OUT, GIF_WIDTH and GIF_FPS override where it lands and how big it is.
demo-record:
	./scripts/record.sh

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
