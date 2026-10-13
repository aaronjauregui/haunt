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

CI (`.github/workflows/test.yml`) runs `make build vet test` and a
`gofmt` check on every push and pull request, on Ubuntu with sway, grim
and foot installed, so none of the compositor tests skip there.

Where the tests are:

- `cmd/haunt/*_test.go` are end-to-end: a ghost directory is written to a
  temp dir, `haunt run` is driven in-process against the headless sway,
  and the test pokes, types, waits and reads the log. `helpers_test.go`
  has the rig. Fixtures in `cmd/haunt/testdata` are small programs the
  tests build, once per run: a tool server, an actor, a looker, a
  chooser, a caller, a teller.
- Package tests sit beside their packages. `internal/model` runs its
  client against the fake model server and keeps the request and reply
  bodies of a real Gemma 4 run on llama-server in `testdata`;
  `internal/memory` and `internal/skill` work on temporary directories;
  `internal/seriko` round-trips shells; `internal/ghost` tests consent,
  initiative, intentions, goals, polls, the envelope mapping, and the
  model brain's loop against the fake.

### The fake model server

Nothing in `make test` needs a model. `internal/model/fake` is a
scripted chat completions server, an `httptest.Server` that answers
`POST /v1/chat/completions` in the OpenAI SSE format, so the client and
everything above it are tested against the wire rather than a mock:

```go
srv := fake.NewT(t,
    fake.Rule{Match: "remind me", Calls: []fake.Call{{Name: "intend", Args: json.RawMessage(`{"text":"stretch","delay_ms":60000}`)}}},
    fake.Rule{Match: "remind me", AfterTool: "*", Calls: []fake.Call{{Name: "speak", Args: json.RawMessage(`{"pose":"happy"}`)}}},
    fake.Rule{Reply: "Will do."},
)
// [model] base_url = srv.BaseURL()
```

The first rule that matches answers. `Match` is looked for in the last
user message; `AfterTool` says whether a rule answers a fresh turn (empty),
any round after a tool result (`*`), or one after a result containing
some text, so one script carries a whole decide loop. A rule can also
reason first, delay or stall between frames, split a call's arguments
over frames, answer with an HTTP status and `Retry-After`, cut the
stream mid-answer, or echo the prefill as llama-server does.
`Bodies`, `Last` and `Received` read back what was sent. The end-to-end
rig in `cmd/haunt/brain_test.go` (`newBrainRig`) writes a config whose
`[model]` points at one ([The model brain](brain.md#tests)).

## Demos

    make demo                 # whisper, scripted, nested sway window
    make demo-ai              # wisp on your [model]
    make demo-troupe          # both
    make demo-ember           # ember, the warm one, on your [model]
    make demo-seriko SERIKO=<nar or dir>
    make demo-headless        # the demo plumbing with no window

Wisp declares every capability the demo can show, so what it can actually
do follows the backends in your config: drawing needs `[backend.image]`,
looking needs `[backend.vision]`, and searching needs `[backend.search]`.
Without one the tool is still offered and refuses with the reason, which
is the behaviour worth seeing.

`scripts/demo.sh` builds into a throwaway workdir and cleans up, including
the ghost's memory and transcript; the config is your own, for `[model]`
and the backends, and the script warns when it has no `[model]`. Knobs: `IDLE_MS`, `GOAL_DELAY`, `GHOST`, `SERIKO_ARGS`,
`WISP_NOTES` (a notes tool server and two dependent skills),
`WISP_MORNING_CRON` (a morning occasion), `SEED_STATE` (a goal taken on
eight days ago, via `scripts/spinseed`).

## Evals

The tests hold haunt's code to what it should do, against the fake model
server. The evals hold a persona ghost to what it should do, on a real
model: each case drives wisp through one scenario (a reminder, a focus
session, a window title that tries to give orders, an hour at the desk
compressed into minutes) and grades what it left behind. A model that
behaves badly one time in three is a pass rate to watch rather than a
broken build, so every case runs several times and the result is a rate
compared with a baseline.

    make eval                 # every case, 5 trials each, 4 at a time
    make eval-baseline        # keep the latest run as the baseline

Run them before merging a change to haunt's standing rules
(`internal/ghost/prompt.go`), its skills (`skills/`), the envelopes or
the decisions, and when changing model. They are not part of `make test`
and never run in CI: a full run takes well over an hour and spends several
hundred turns of the model's quota, plus a few dozen judge calls.
`EVAL_CASES` runs only the cases a change touches. The harness's own
tests do run in CI: `make eval-test` checks the grading, the report,
the judge's call against a stub `claude` and the rig on the fake model,
and spends nothing.

Knobs, from the environment:

| Variable | Default | Meaning |
|---|---|---|
| `EVAL_CASES` | all | a regexp on case names: `EVAL_CASES='focus|reminder'` |
| `EVAL_TRIALS` | 5 | trials per case |
| `EVAL_OUT` | `.eval` | where runs and the baseline are kept |
| `EVAL_JUDGE` | on | `off` skips the judged graders; they are reported as skipped, not passed |
| `EVAL_STRICT` | off | `1` fails the run when a case is more than one trial below its baseline rate and that rate is above the 95% interval of its rate in this run, when a case has no trial graded, or when a case was skipped for a missing prerequisite (see [Reading a run](#reading-a-run)) |
| `EVAL_ANCHOR_RUNS` | 3 | how often `TestRubricAnchors` judges each anchor |
| `HAUNT_CONFIG_DIR` | `~/.config/haunt` (the recipe sets it) | the config whose `[model]` and `[backend.*]` the trials use |

Prerequisites are checked before anything starts; a run never
half-runs, and the report lists what was missing. The model under test
is the one wisp would think with: `[model]` with any `[model.ghost.wisp]`
laid over it, needing `base_url` and `name` and, when `api_key_env` names
a variable, that variable set. The report records the model's `name`
and the host of its `base_url`; the key stays in the environment.
The judge is `claude -p` on `PATH`, logged in to a Claude subscription,
with no `ANTHROPIC_API_KEY` in its environment. `[backend.image]` and
`[backend.vision]` are optional; the cases that need them are skipped
without them. The web cases run a canned SearXNG of their own, and the
files cases share a folder each trial seeds for itself.

### What a case is

A case is an entry in `cmd/haunt/eval_cases_test.go`: a `Drive` that sets
up a trial and plays the scenario, and graders over what it leaves (the
turns from `haunt history -json`, the intentions, the log, the notes
file). Each trial has its own headless sway, config and state directory, which
holds wisp's transcript, memory and the skills it writes, so trials run
side by side and none sees another's memory. The rig that drives them is `eval_rig_test.go`; it
waits on the ghost's own log lines (a turn over, a question offered, a
session fact noticed), not on guesses at how long a model takes.

Most graders are code: did it read the skill, call the tool, file the
reminder, keep a banned phrase out of a reply. Every trial also gets the
`always/` checks: no envelope in what it says, no Markdown, no emoji, no
tool names, no bracketed label (a pose or a stage direction) and no
`stay_silent` in the words, at most 60 words a reply, and nothing in the
log that says haunt itself broke.

Memory is the ghost's own files, written through decisions. The graders
count `remember`, `forget`, `rewrite_notes` and `diary` as memory writes
(`isMemoryWrite`: it remembered a fact) and `recall` (over the topics
and the transcript) as a read (`memoryWrites`, `memoryReads` in
`eval_cases_test.go`). A call persists (`persists`) when it is a memory
write or a `skill_write`; a test holds that list to `ghost.MemoryWrite`,
which `haunt history`'s echo check uses. The `memory` case asks for a
fact, so it counts memory writes; `files-injection` fails on the
injected words kept by any call that persists. Any other call `history`
files under `looked` beside `skill_view` and `skill_write` makes the
trial an error (`always/memory-known`): a write the graders cannot see
would let "remembered none of it" pass. A case that must know what was
kept also snapshots the trial's `memory/` directory, and
`files-injection` its `skills/` directory too.

What takes days is seeded before the ghost starts rather than waited
for: a goal taken on a week ago (`ghost.SeedGoal`), turns from earlier
days with their own dates (`ghost.SeedTranscript`, ordinary transcript
rows the ghost cannot tell from its own), and memory files written as
though kept long ago. The `multiday` case seeds two evenings of talk (a
move from Sydney to Brisbane, a cat named Pickle, a thesis deadline with
a date) and a `where` topic that still says Sydney, then lets the person
go idle until reflection has looked back on both days. It runs once per
`[memory] reflection` mode, as `multiday-character` and `multiday-side`,
and grades a diary entry per day under the 600-character cap, the
names and the date kept in the entries, Brisbane kept live (in any
topic, and in `where`), and
Sydney retired under Past in both modes, since a reflection turn may
retire a line the person stated. The judge (`diary-faithful`) reads everything
in `memory/` against the seeded talk for anything invented or mixed up.
Comparing the two sub-cases' pass rates is how the default mode is
chosen. The same case runs on the fake model as `TestEvalRigMultiday`,
which needs no real model.

What code cannot grade, the content and tone of a reply, a judge grades:
`claude -p`, asked fixed yes/no questions from a rubric
(`eval_rubrics_test.go`), one call per reply. Every judged reply is held
to the tone questions too, which come from wisp's persona and the rules
every ghost has.

A rubric's anchors are hand-written replies under
`cmd/haunt/testdata/eval/anchors/`, each with a decided verdict: it must
pass, or must fail and fail the criteria it names.

    go test -tags eval -run TestRubricAnchors ./cmd/haunt

judges each anchor three times and fails on a hand-written anchor that
comes out otherwise, or whose criteria the judge answers differently
across runs; such a question is reworded until it does not. What the anchors prove
is that the judge is consistent on known replies: it gives the verdict
decided for each, every time. They do not show that it grades unseen
replies as a person would. The agreement rates on labelled anchors are
the accuracy measure: how often the judge agrees with human labels on
real replies, given separately for replies labelled pass and labelled
fail.

Each rubric whose hand-written anchors all held is recorded in
`cmd/haunt/testdata/eval/anchors/verified.json` with its hash, the same
hash a run's `results.json` records, and the test rewrites only those
entries, even when other rubrics' anchors failed. The file is committed,
so a rubric's change and its re-verification travel together. A run
whose rubric's hash differs from the recorded one warns of it in the
report's header (see [Reading a run](#reading-a-run)); a change to the
shared tone questions or the prompt's template changes every rubric's
hash. Run `TestRubricAnchors` after changing a rubric, and commit
`verified.json` with the change.

Labelled anchors are real replies from eval runs, labelled pass or fail
by a person, in files named `<rubric>-label-NN.json` beside the
hand-written ones. Each carries `"source": "labelled"`, the judge's
verdict when the reply was sampled (`judged`) and where it came from
(`origin`, `<results.json>:<case>/<trial>/<turn>`). They are measured,
not enforced: `TestRubricAnchors` judges them as often as the others,
but they never fail the test or affect `verified.json`. For each rubric
that has them it logs a line such as
`memory: labelled-pass 9/10, labelled-fail 8/10`: of 10 replies a person
labelled pass, the judge passed 9 on every run, and of 10 labelled fail,
it failed 8 on every run. An anchor agrees only when every run's verdict
on the whole rubric matches its label, and a run that errs counts
against it. Each anchor that does not agree is listed beneath its line,
with what the judge said on each run. A low labelled-pass rate means the
judge is too strict for that rubric; a low labelled-fail rate means it
lets bad replies through.

To label, use the tool in `.eval-label/`. It is its own Go module,
outside the tree (`.gitignore` covers it), and reads one or more runs:

    cd .eval-label && go run . ../.eval/<run>/results.json…

It samples up to 10 judged-pass and 10 judged-fail replies for
`files-injection`, `memory` and `humanizer`, shows each with its
context, and takes `p` for pass, `f` followed by the failing criteria
IDs, `s` to skip, or `q` to quit. It saves after every answer, so
quitting loses nothing, and at the end prints each rubric's agreement
from the saved verdicts, which spends no quota. It writes the anchors
straight into `cmd/haunt/testdata/eval/anchors/`; only those files are
committed.

### Reading a run

A run is a directory under `.eval/`, named for when it started:
`results.json` has every trial's graders, verdicts and reasons;
`report.md` puts the prerequisites that were missing first, then each
case's pass rate beside the baseline's, then the run's face and pace,
then per-grader rates, then every failing trial with its graders'
reasons and its trace; `trials/` keeps each trial's trace, log and
intentions. An **error** is the harness's fault (the model never
answered, an event the case waited for never arrived) and is counted
apart from a **fail**, which is the ghost's.

The report's header has two lines. The first names the model and its
host, the revision, k, whether the judge was on, and how long the run
took. The second is what the run cost:

    judge claude-opus-5-5 (claude 2.1.296 (Claude Code)) · tokens 412000/380000/9100 · judge $1.84 (as the CLI reports it)

that is, the model the judge ran on and what `claude --version`
printed, the model's tokens over every trial as prompt/cached/out, and
the judge's cost in dollars as the CLI reports it, which on a
subscription is notional. Below them the header lists a warning for
each rubric the run used that has anchors but whose hash is not the one
in `verified.json`: `warning: rubric memory changed since its anchors
last passed`, or `never verified against its anchors` when it has no
entry there. Its verdicts in this run come from wording the anchors
have not been checked against. A rubric with no anchors is never
warned about. The tokens are read from the brain's
`decide:`, `speak:` and `reflect:` lines in each trial's log. The case
table's last column is a case's mean tokens per trial, over the trials
that logged any. A value that is absent, from a trial whose log has no
token line, a run with the judge off, or a `results.json` older than
these fields, shows as `–`, never `0`. `results.json` also keeps the
tokens and judge cost per trial and per case, the judge's own tokens,
and `rubrics`: each rubric the selected cases use, with the first 12
hex digits of the sha256 of its prompt and schema, so two runs show
whether a rubric's wording changed between them.

**Face and pace** is read from the line that ends every turn in the
ghost's log (`turn over: person took=4.2s face=happy by=speak`), and sits
beside the baseline's (`eval_face_test.go`). It splits the spoken turns
by how the face went with the words: chosen by the model as it decided
to speak, by the `face` tool, guessed by the driver from the words, or
not changed at all. It also counts the distinct poses worn per trial,
and the median boot and person turn. A pass rate says whether the ghost
did the right thing; these say whether it kept the person waiting, and
whether it has gone wooden: fewer faces changed, or fewer poses per
trial.

Beside each case's rate the case table gives its **95% CI**, the
Wilson score interval over its graded trials (`23–88%`), and
**pass^k**, ✓ when every one of the k trials passed. The rate counts
only passes and fails, so a case with errors can show 100%; pass^k
counts an error or a skip against it. The interval says how much the
rate could move by chance: at k=5, 3 of 5 is anywhere from 23% to 88%,
so two runs whose intervals overlap widely have not shown a
difference. A case with no trial graded has no interval and shows `–`.

The Δ column compares the rate with the baseline's on two levels. Both
start from a drop of more than one trial below the baseline's rate,
scaled to this run's graded trials. When the baseline's rate is also
above this run's interval, the drop is more than chance would give; Δ
is marked `✗` and `EVAL_STRICT=1` fails the run, naming the baseline's
rate and the interval. When the baseline is still inside the interval,
Δ is marked `↓`, which is worth a look but does not fail. A drop of one
trial is neither: the interval of any rate below 100% stops short of
100%, so the trial count is what keeps 4 of 5 against a baseline of
100% from failing, while 3 of 5 against it fails.

The baseline is local and per model: a run against a different model
from the baseline's is shown without deltas.

Two full runs are kept in `docs/dev/eval-reports/`, each a run's
`report.md` with its local endpoints replaced by placeholders:
[GLM 5.3 on z.ai's API](eval-reports/2026-10-11-glm-5.3.md) and
[Gemma 4 31B (QAT, Q4) on a local llama.cpp server](eval-reports/2026-10-11-gemma-4.md).
The draw and change-picture cases fail in both because the image
backend was not running.

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
`brain` at debug carries a scripted brain's stdio and, for a persona
ghost, each model call's token counts (with how many the server had
cached) and the line that ends every turn.

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
- User-facing behaviour changes need the matching page under `docs/`
  updated in the same change.
