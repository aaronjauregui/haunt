# Hacking

## Building

    make build      # ./haunt and ./haunt-seriko
    make vet
    make fmt

Go 1.25 or later, no cgo. The Wayland bindings under `internal/wayland`
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
  tests build, once per run: a fake hermes, a tool server, an actor, a
  looker, a chooser.
- Package tests sit beside their packages. `internal/hermes` replays
  captured SSE from `testdata`; `internal/seriko` round-trips shells;
  `internal/ghost` tests consent, initiative, intentions, goals, polls,
  the envelope mapping and the skills' front matter.

The hermes tests need no hermes: the fake in `testdata/fakehermes`
speaks the gateway's HTTP and SSE, spawns `haunt mcp-serve` the way
hermes does, and follows a script given in `FAKEHERMES_SCRIPT` (match a
turn, delay, call a tool, reply).

## Demos

    make demo                 # whisper, scripted, nested sway window
    make demo-ai              # wisp on hermes
    make demo-troupe          # both
    make demo-seriko SERIKO=<nar or dir>
    make demo-headless        # the demo plumbing with no window

`scripts/demo.sh` builds into a throwaway workdir and cleans up, including
the hermes profile. Knobs: `IDLE_MS`, `GOAL_DELAY`, `GHOST`, `SERIKO_ARGS`,
`WISP_NOTES` (a notes tool server and two dependent skills),
`WISP_MORNING_CRON` (a morning occasion), `SEED_STATE` (a goal taken on
eight days ago, via `scripts/spinseed`).

## Evals

The tests hold haunt's code to what it should do, against a fake hermes.
The evals hold the hermes ghost to what it should do, on a real model:
each case drives wisp through one scenario (a reminder, a focus session,
a window title that tries to give orders, an hour at the desk compressed
into minutes) and grades what it left behind. A model that behaves badly
one time in three is a pass rate to watch rather than a broken build, so
every case runs several times and the result is a rate compared with a
baseline.

    make eval                 # every case, 3 trials each, 4 at a time
    make eval-baseline        # keep the latest run as the baseline

Run them before merging a change to haunt's rules (`internal/hermes/soul.go`),
its skills (`skills/`), the envelopes or the tool list, and when changing
model. They are not part of `make test` and never run in CI: a full run
takes about an hour and spends a few hundred turns of the model's quota,
plus a few dozen judge calls. `EVAL_CASES` runs only the cases a change
touches.

Knobs, from the environment:

| Variable | Default | Meaning |
|---|---|---|
| `EVAL_CASES` | all | a regexp on case names: `EVAL_CASES='focus|reminder'` |
| `EVAL_TRIALS` | 3 | trials per case |
| `EVAL_OUT` | `.eval` | where runs and the baseline are kept |
| `EVAL_JUDGE` | on | `off` skips the judged graders; they are reported as skipped, not passed |
| `EVAL_STRICT` | off | `1` fails the run when a case drops more than one trial below the baseline, or a case was skipped for a missing prerequisite |
| `EVAL_ANCHOR_RUNS` | 3 | how often `TestRubricAnchors` judges each anchor |

Prerequisites, checked before anything starts; a run never half-runs, and
the report lists what was missing:

- `sway`, `grim` and `foot` on `PATH`, Go and docker. Without `foot`, the
  cases that open windows are skipped.
- Hermes set up as in [The brain](../user/hermes.md): `[hermes] command`
  and `[hermes.model]` in the config, the provider keys, the image. The
  model under test is whatever the config names, and the report records
  it.
- `claude` on `PATH`, logged in to a Claude subscription, with no
  `ANTHROPIC_API_KEY` in its environment, for the judge.
- Optionally `[backend.image]` for the `draw` case and `[backend.vision]` for
  `look`; without them those cases are skipped.

### What a case is

A case is an entry in `cmd/haunt/eval_cases_test.go`: a `Drive` that sets
up a trial and plays the scenario, and graders over what it leaves (the
turns from `haunt history -json`, the intentions, the log, the notes
file). Each trial has its own headless sway, config, state, hermes
profile and container, so trials run side by side. The rig that drives
them is `eval_rig_test.go`; it waits on the ghost's own log lines (a turn
over, a question offered, a session fact noticed), not on guesses at how
long a model takes.

Most graders are code: did it look up the skill, call the tool, file the
reminder, keep a banned phrase out of a reply. Every trial also gets the
`always/` checks: no envelope or `NO_REPLY` in what it says, no Markdown,
no emoji, no tool names, at most 60 words a reply, and nothing in the log
that says haunt itself broke. What code cannot grade, the content and tone
of a reply, a judge grades: `claude -p` on your subscription, asked fixed
yes/no questions from a rubric (`eval_rubrics_test.go`), one call per
reply. Every judged reply is held to the tone questions too, which come
from wisp's persona and the rules every ghost has.

A rubric is only trusted once the judge agrees with its anchors: saved
replies under `cmd/haunt/testdata/eval/anchors/` that must pass or must
fail.

    go test -tags eval -run TestRubricAnchors ./cmd/haunt

judges each anchor three times and fails a criterion the judge answers
differently across runs; such a question is reworded until it does not.
Run it after changing a rubric.

### Reading a run

A run is a directory under `.eval/`, named for when it started:
`results.json` has every trial's graders, verdicts and reasons;
`report.md` puts the prerequisites that were missing first, then each
case's pass rate beside the baseline's, then per-grader rates, then every
failing trial with its graders' reasons and its trace; `trials/` keeps
each trial's trace, log and intentions. An **error** is the harness's
fault (hermes never came up, an event the case waited for never arrived)
and is counted apart from a **fail**, which is the ghost's.

The baseline is local and per model: a run against a different model
from the baseline's is shown without deltas.

## Logging

The log is leveled and divided by topic (`internal/hlog`). Default is
info with no debug topics: a handful of lines per session.

    HAUNT_LOG=debug haunt run            # every topic
    HAUNT_DEBUG=brain,curiosity haunt run
    HAUNT_BACKEND_DEBUG=1                     # backend requests

Topics: `session`, `brain`, `backend`, `prompt`, `balloon`, `anim`, `input`,
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
