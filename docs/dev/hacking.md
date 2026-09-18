# Hacking

## Building

    make build      # ./haunt and ./haunt-seriko
    make vet
    make fmt

Go 1.24 or later, no cgo. The Wayland bindings under `internal/wayland`
are generated from the XML in `protocol/` against the patched
`third_party/go-wayland`.

## Tests

    make test       # go test ./...

Tests run against a real compositor, never a mock: `internal/testenv`
starts sway with the wlroots headless backend, so nothing is drawn on
your session, and offers a virtual pointer and keyboard and extra
headless outputs for multi-monitor cases. `sway` and `grim` must be on
`PATH`. The window awareness, session and fullscreen tests also need
`foot`, a terminal to open and focus; without it they skip.

Where the tests are:

- `cmd/haunt/*_test.go` are end-to-end: a ghost directory is written to a
  temp dir, `haunt run` is driven in-process against the headless sway,
  and the test pokes, types, waits and reads the log. `helpers_test.go`
  has the rig. Fixtures in `cmd/haunt/testdata` are small programs the
  tests build: a fake hermes, a tool server, an actor, a looker, a
  chooser.
- Package tests sit beside their packages. `internal/hermes` replays
  captured SSE from `testdata`; `internal/seriko` round-trips shells;
  `internal/ghost` tests consent, initiative, intentions, goals, polls,
  the envelope mapping and the skills' front matter.

The hermes tests need no hermes: the fake in `testdata/fakehermes`
speaks the gateway's HTTP and SSE, spawns `haunt mcp-serve` the way
hermes does, and follows a script given in `FAKEHERMES_SCRIPT` (match a
turn, delay, call a tool, reply).

## Demos and spins

    make demo                 # whisper, scripted, nested sway window
    make demo-ai              # wisp on hermes
    make demo-troupe          # both
    make demo-seriko SERIKO=<nar or dir>
    make demo-headless        # the demo plumbing with no window
    make spin-ai              # wisp headless on a real model, scripted conversation, history with reasons

`scripts/demo.sh` builds into a throwaway workdir and cleans up, including
the hermes profile. Knobs: `IDLE_MS`, `GOAL_DELAY`, `GHOST`, `SERIKO_ARGS`,
`WISP_NOTES` (a notes tool server and two dependent skills),
`WISP_MORNING_CRON` (a morning occasion), `SEED_STATE` (a goal taken on
eight days ago, via `scripts/spinseed`). `scripts/hermes-spin.sh` says
what to look for in each turn; `scripts/spinkeys` is a virtual keyboard
on the demo's headless sway that answers the ghost's questions by reading
the debug log.

The spin needs hermes set up as in the user guide ([The brain](../user/hermes.md)),
takes about twenty minutes, and spends a few dozen turns of quota.

## Logging

The log is leveled and divided by topic (`internal/hlog`). Default is
info with no debug topics: a handful of lines per session.

    HAUNT_LOG=debug haunt run            # every topic
    HAUNT_DEBUG=brain,curiosity haunt run
    HAUNT_AI_DEBUG=1                     # backend requests

Topics: `session`, `brain`, `ai`, `prompt`, `balloon`, `anim`, `input`,
`screen`, `memory`, `store`, `wayland`, `place`, `curiosity`, `tool`,
`control`. The same settings live under `[log]` in the config; the
environment wins. A misspelled level or topic is reported at startup.
`brain` at debug carries a scripted brain's stdio and the hermes
gateway's output.

## Conventions

- The code is the record of what exists. A package comment says what the
  package is for and why it is shaped that way; a function comment says
  what is not obvious from the signature. Rationale that spans packages
  goes in [Decisions](decisions.md).
- There is no status log, changelog or backlog file, on purpose. Git
  history is the history.
- Anything that changes a running ghost goes through its control socket.
  Do not open a running ghost's `state.db` for writing.
- Every effect the model or a brain can cause goes through the gates in
  `internal/ghost` (capability, consent, allowance, quiet, fullscreen,
  budget) and, for tools, the visible marks. Do not add a path around
  them.
- Hermes facts are pinned to a commit ([Hermes integration](hermes.md)).
  Moving the pin means rechecking each named source location.
- User-facing behaviour changes need the matching page under `docs/`
  updated in the same change.
