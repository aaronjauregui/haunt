# Hermes integration

How a persona ghost thinks. `internal/hermes` is haunt's side of the seam;
`internal/ghost/hermesbrain.go` is the brain that uses it, and
`internal/ghost/hermestools.go` is the tool surface hermes calls back
into. Every hermes identifier, path and payload here is checked against
hermes-agent at commit **818c13b** (release v0.21.6), which `scripts/hermes-docker` builds
its image from. When the pin moves, recheck them: the places that depend
on hermes internals are named below, and `make hermes-check` checks the
toolset restriction against the new image ([Moving the
pin](#moving-the-pin)).

## Shape

```
┌─────────────────────────────┐   HTTP + SSE    ┌──────────────────────────┐
│ haunt driver (Go)           │ ──────────────▶ │ hermes gateway           │
│  screen, input, consent,    │  events, text   │  one profile per ghost   │
│  budgets, quiet hours,      │ ◀────────────── │  session, memory, skills,│
│  intentions                 │  deltas, tools  │  compression, review     │
│                             │                 │                          │
│  control socket ◀───────────┼── MCP stdio ────┤  mcp_servers: haunt      │
│  (say, face, draw, look,    │   tool calls    │  (spawned by hermes)     │
│   intend, ask, resolve …)   │                 │                          │
└─────────────────────────────┘                 └──────────────────────────┘
```

Two channels, neither needing Python code in haunt:

1. **Turns.** Haunt posts each turn to hermes's API server and reads the
   reply as server-sent events. The person's words go verbatim; every
   other event goes as a one-line envelope.
2. **Tools.** Hermes spawns `haunt mcp-serve --ghost <name>` as an MCP
   stdio server. Every ghost verb is a tool, which hermes names
   `mcp__haunt__<tool>`. The server forwards each call over the ghost's
   control socket to the running driver, which applies consent, budgets
   and the visible marks, performs the effect, and returns the result.

## The profile

`Config.EnsureProfile` runs on every ghost start. The profile is
`<root>/profiles/haunt-<ghost>/`, where the root is `[hermes] home`,
else `$HERMES_HOME`, else `~/.hermes`. The `haunt-` prefix keeps a ghost
off any profile the person made, and the name is folded to hermes's
`^[a-z0-9][a-z0-9_-]{0,63}$`. The profile directory is `HERMES_HOME` for
its gateway.

Rewritten every start (haunt's):

- **`SOUL.md`**: the manifest persona, then haunt's standing rules
  (`Soul`). The rules' order is fixed because the first two are what keep
  a companion from narrating every window change.
- **`haunt-skills/`**: the package's skills, haunt's, and the bundled
  hermes skills the manifest names, in that order, earlier source winning
  a name clash; Markdown only; files 0444 and directories 0555. Read-only
  on disk because at the pinned hermes `skill_manage` edits and deletes
  `external_dirs` skills from a live turn, and the background review fork
  may patch or rewrite any skill it has read, whoever owns it
  (`tools/skill_manager_guards.py` gates the fork's deletes alone). No
  config key makes them read-only, so the permissions are the only guard
  on haunt's skills. The demo and the test rig `chmod -R u+w` before
  removing a profile.
- **`config.yaml`**, merged rather than replaced. Haunt owns
  `platforms.api_server`, `gateway.standalone`, `platform_toolsets`,
  `agent.disabled_toolsets`, `agent.auto_recovery_cycles`,
  `auxiliary.title_generation.model_upgrade_enabled` (off: hermes keeps
  the session title it derives from the first message and makes no model
  call for a better one, which nothing of a ghost's shows),
  `tools.tool_search`, `skills.inline_shell`, `security.allow_lazy_installs`,
  `mcp_servers.haunt`, its own entry in
  `skills.external_dirs`, and `_config_version` if absent; it defaults
  `skills.guard_agent_created` to true. Everything else, the model above
  all, stays as the person set it unless `[hermes.model]` in the haunt
  config says otherwise. No `memory` key is haunt's: that section is the
  person's, set through `hermes memory setup`.
- **`.env`** gets `API_SERVER_KEY` (32 random bytes, hex),
  `API_SERVER_PORT` (a free loopback port), `API_SERVER_HOST=127.0.0.1`
  and `HAUNT_GHOST`, each chosen once and kept so the CLI can find a
  running gateway and a restart does not move it. Other lines are kept.
- **`.no-bundled-skills`**, only before hermes's first seed. Without it a
  new profile lists hermes's 58 bundled skills and the ghost claims to do
  mail and documents. The marker affects seeding only; the essential
  skill `hermes-agent` is still seeded.

Never touched: anything that holds what the ghost remembers or said —
`memories/MEMORY.md`, `memories/USER.md`, a memory provider's store
(holographic's `memory_store.db`, say) and `state.db`.

The toolset restriction:

```yaml
platform_toolsets:
  api_server: [memory, session_search, skills, haunt]
agent:
  disabled_toolsets: [terminal, file, browser, code_execution, delegation,
    web, cronjob, computer_use, todo, vision, image_gen, tts, kanban,
    connections, clarify, google_meet, x_search, video, video_gen,
    spotify, a2a, browser-cdp, browser-use, discord, discord_admin,
    feishu_doc, feishu_drive, yuanbao]
tools:
  tool_search: {enabled: "off"}
```

The MCP toolset is named after its key under `mcp_servers`. Naming any
MCP server enables only those. `disabled_toolsets` is subtracted last, so
a later hermes that widens an explicit list still cannot hand a ghost a
shell. The plugin toolsets are on that list because hermes enables a
plugin's toolset beside an explicit list once the person turns the plugin
on (`hermes_cli/tools_config.py` `_enabled_plugin_toolsets`), and each
hermes release brings more; `make hermes-check` finds the ones the list
misses. The messaging-platform toolsets (`discord`, `feishu_doc`,
`yuanbao` …) reach a ghost only if named; they are on the list so that
a widened list still has their tools subtracted. `clarify` is left out
because the API server passes no clarify callback and the tool only
ever answers that it is unavailable; haunt's `ask` is how a question is
put. Tool search must be off: with it on, the MCP tools and
`session_search` hide behind a lookup a small model does not think to
use. The model receives exactly `memory`, `skill_manage`,
`skill_view`, `skills_list`, `session_search` and `mcp__haunt__…`, plus
the tools of the memory provider the person chose, which hermes exposes
whenever the `memory` toolset is enabled
(`agent/memory_manager.py` `memory_provider_tools_enabled`). With
built-in memory turned off, `memory` itself is gone and the provider's
tools are the only memory the model has. A plugin context engine the
person chose (`context.engine`) brings its tools the same way: hermes
adds the `context_engine` toolset beside an explicit list whenever one
is set (`hermes_cli/tools_config.py` `_get_platform_tools`,
`_context_engine_active`), and the agent hands the model the engine's
tools (`agent/agent_init.py` `_inject_context_engine_tools`). Haunt
leaves it on; which engine, if any, is the person's choice.

## The gateway

`Gateway` runs `HERMES_HOME=<profile> hermes gateway run
--external-supervisor --replace` in its own process group, waits on
`GET /health`, restarts it with the troupe's backoff shape, and stops it
on shutdown. A gateway already answering on the profile's port is adopted.
Hermes multiplexes profiles by default: one gateway serves every profile
under the root, and a named profile with no installed service is refused
a gateway of its own. `gateway.standalone: true` in the profile is what
lets haunt start one, and it also keeps a gateway the person runs from
serving the ghost's profile outside haunt. Hermes calls the key a
temporary shim, so a later pin may need another way.
It is started only once the control socket is listening, because hermes
spawns `mcp-serve` immediately and that asks for the tool list; turns
sent before it is up wait for it, up to two minutes, without counting
against the first-token deadline. The gateway's output goes to the log
under the `brain` topic at debug.

Hermes gives MCP servers a filtered environment (`PATH HOME USER LANG
LC_ALL TERM SHELL TMPDIR` and `XDG_*` survive; `WAYLAND_DISPLAY` and
`HAUNT_*` do not), so `HAUNT_RUNTIME_DIR`, when set, is written into the
server entry's `env`. The command is the running haunt binary's absolute
path. `mcp-serve` connects lazily and retries dial failures for 20
seconds. Its per-call timeout is `mcp_servers.haunt.timeout`, written
from `[hermes] tool_timeout` (600 s), which bounds how long `ask` may
wait.

## The session

One session per ghost, created with `POST /api/sessions` and
`{"id":"haunt-<ghost>"}`; a 409 `session_exists` is adopted. The id is
kept in the store under `_haunt/hermes_session`. Hermes compresses in
place by default (`compression.in_place: true`: same id, the older turns
archived as `compacted`); with it off, compression ends the session and
continues in a child with a new id, so the id is taken from
`assistant.completed` and `run.*` payloads and stored when it changes; a
404 `session_not_found` makes a new one. `X-Hermes-Session-Key: <ghost>`
goes on every request: it scopes long-term memory, the context engine and
the prompt cache, and does not pick the session on the `/api/sessions`
routes. Every request carries `Authorization: Bearer <API_SERVER_KEY>`;
the API server refuses to start without a key even on loopback. A ghost's
session is its life; nothing starts a new one deliberately.

## A turn

`Client.Turn` posts `POST /api/sessions/{id}/chat/stream` with
`{"input": …, "system_message": …}`. `system_message` is appended to the
end of hermes's system prompt for that turn only and is not persisted;
it carries the per-turn context: one line with the time of day and the
focused window (when `window` is granted), and a second line naming
errands that came due and were not dealt with, so a reminder whose
`intention_due` turn was dropped, silent or held back by quiet is still
in front of the model until it resolves it.

SSE frames are `event: <name>\ndata: <json>\n\n` with `: keepalive`
comments every 10 s. The order is `run.started`, `message.started`, then
any of `assistant.delta {delta}`, `assistant.commentary {text,
already_streamed}`, `tool.started {tool_name, preview, args}`,
`tool.completed {tool_name}`, `tool.failed` (a tool that returned an
error), `tool.progress`,
then `assistant.completed {content, completed, partial, interrupted}`,
`run.completed|run.failed|run.cancelled`, and `done`; an exception is
`error {message}` before `done`. `approval.request` is ignored.
`tool.progress` with `tool_name` `_thinking` is reasoning and is
ignored; hermes also sends one after each model call with the text that
call wrote. The frames in `internal/hermes/testdata` are captured from a
v0.21.6 gateway: `lookup.sse` (interim words, a lookup, the reply),
`lookup-interim-off.sse` (the same with interim messages off),
`lookup-silent.sse` (interim words, a lookup, `NO_REPLY`), `failed.sse`
and `history-failed.json` (a turn whose provider never answered, and the
session read back after it). `history-interrupted.json` is built by hand
in the shape of `history-failed.json`, with the row hermes closes a turn
interrupted after a tool with.

**Interim words.** Text the model writes alongside a tool call ("let me
look…") streams as `assistant.delta` like any other, and once that model
call is over hermes sends it again whole as `assistant.commentary`,
before the call's `tool.started`; `already_streamed` is true when the
deltas carried all of it. The deltas also carry the blank line the model
writes before its call, which the commentary's text leaves out. It is not part of `assistant.completed`'s
`content`, which is the turn's last part only, and the first delta after
a tool round opens with `\n\n`. Hermes sends commentary while
`display.interim_assistant_messages` is on (the default), also for a
would-be reply hermes sends back to the model to carry on (its stall and
verification guards); on the Codex app-server runtime every message the
model writes is sent as commentary, the last included. The client
treats commentary as the end of a part (`TurnRequest.OnInterim`, with the
text), and so is a `tool.started` with words streamed since the last
mark, which is the only mark a profile with interim messages off gets.
A stream that ends without `assistant.completed` answers with
the last part alone. The driver shows each part as it arrives, in place
of the one before: interim words stay up while the tools run, as the
ghost saying what it is doing, and the reply replaces them, so the
balloon ends on what the finished turn shows. Commentary that did not
stream is shown from its text. Each part is held and read on its own
for a pose tag and for `NO_REPLY`, and a part's pose goes on with its
first words. The talk animation stops at the end of each part. The
why-trace records the reply alone; the session keeps the interim words
on the message that made the call, and `history` shows them as lines of
the turn.

On the driver's side: two deadlines bound silence, not the turn
(`turnDeadline`, fed every event's name by `TurnRequest.OnEvent`). The
first-token deadline (`[hermes] first_token_timeout`, 30 s) runs to the
first sign of life, which is any event past `run.started` and
`message.started`: words, a tool event, reasoning. The stall deadline
(`[hermes] stall_timeout`, 120 s) then runs from each event to the next;
keepalive comments are not events. It stops while one of the ghost's own
tools runs (`mcp__haunt__*`, from `tool.started` to `tool.completed` or
`tool.failed`), since `draw` and `ask` take minutes and the MCP timeout
bounds them; hermes's own tools are local reads and writes that ask no
model and do not stop it. A turn given up on fails as any other does, with
the reason in the balloon, and the client stops its run before `Turn`
returns (`Client.stopRun`). Closing the stream alone would stop it only
at the API server's next write, a keepalive up to 10 s later, which
fails and calls `agent.interrupt("SSE client disconnected")`
(`gateway/platforms/api_server.py`, `_handle_session_chat_stream`,
`_drain_session_stream_task_on_disconnect`), and hermes does not refuse
a second turn on a session whose first is still running, so the
person's queued words could start one beside it. The client records the
`run_id` every event carries and, when its context is cancelled before
a `run.*` event has ended the run, sends `POST /v1/runs/{run_id}/stop`
(`gateway/platforms/api_server_runs.py`, `_handle_stop_run`) on a fresh
context with a 5 s timeout. The stop is an explicit hard interrupt
(`request_hard_interrupt`), and it is not synchronous: it answers
`{"status": "stopping"}` at once, and the agent's thread winds down on
its own. A recovery wait checks for the interrupt every 200 ms
(`agent/turn_recovery.py`, `interruptible_backoff_sleep`) and ends the
turn through `abort_turn_on_interrupt`, which keeps what the turn had
done in the session; a model call ends when its request is cut. So the
client then polls `GET /v1/runs/{run_id}` every 200 ms, for at most
10 s, until the status is one of hermes's terminal ones (`completed`,
`failed`, `cancelled`, `interrupted`;
`api_server_run_idempotency.py` `TERMINAL_STATUSES`), which the session
stream sets once the agent has returned and the turn's rows are stored.
A 404 `run_not_found` is a run already forgotten, and a 409
`run_not_active` one with no agent to interrupt yet, which the closed
stream ends; neither is waited on. A stop or a wait that fails is logged
and does not change the error the person sees. The run's ownership is
the API key's and the profile's, so the same bearer key that started it
may stop it. The waiting indicator comes down when the first words are
put on screen, not on a tool event, since a `face` call followed by
another model round trip is seconds with nothing to show; the talk
animation runs while deltas arrive; tool events feed the why-trace and
suppress the lexicon face when the model wore one itself. One turn in
flight per ghost. The person's words queue behind it, and so does
everything before the gateway is up (`lateTurner`, two minutes at most).

**A provider outage.** Once hermes has spent its retries
(`agent.api_max_retries`) on a provider that is down, overloaded or
timing out, its auto-recovery ladder (`agent/turn_recovery_autorecover.py`)
waits and runs the retries again, `agent.auto_recovery_cycles` times, 5 by
default, at 15, 30, 60, 60 and 60 s with up to a fifth more as jitter, or
the provider's `Retry-After` up to 120 s. Its notices go to the status
and thinking callbacks, which the session stream does not wire, so a
waiting turn sends nothing but keepalives. The profile sets the cycles to
2: the waits of 15 and 30 s ride out a provider's overload, which on z.ai
comes as bursts of 429s, and the later ones only make a dead provider
slower to report. With no `Retry-After`, the three attempts of each
round (about 2 and 4 s apart) and the two waits come to about 85 s of
silence before hermes fails the turn itself, measured against a provider
refusing connections. A `Retry-After` stretches a wait to as much as
120 s, and a retry honours one of up to 600 s (`RETRY_AFTER_CAP_S` in
`agent/retry_utils.py`), so the cycles alone do not bound a turn. The
driver's deadlines do: before the first sign of life the first-token
deadline, after it the stall deadline, whose 120 s leaves room for both
short waits, their retries and jitter, and cuts anything longer.

A turn hermes fails ends its stream with `assistant.completed` carrying
hermes's notice for the person ("custom didn't respond in time on any of
3 attempts…") and `completed: false`, then `run.failed`, with no
`turn_exit_reason`; the client reads it as a failure and the notice is
not the ghost's words. The notice ends with the provider's error on a
line of its own, `Provider said: Connection error.`
(`agent/turn_failure_copy.py`), sometimes followed by a paragraph of
advice; the client takes that line alone into the error, "hermes: the
turn failed: Connection error", and leaves the rest, which tells a
hermes chat user to send `/retry` or switch with `/model`. A notice
without the line, for a failure that is not the provider's, fails as
"hermes: the turn failed". Hermes closes the turn in the session with an
assistant row of its own, "Your request was not processed. Send it again
if you still want me to carry it out." (or "This turn did not
complete…" once a tool ran), marked `display_kind: "failed_turn"`, which
`run.failed`'s `messages` also carries. A turn the driver gave up on
after a tool ran is closed instead with the interrupt's own text as a
plain assistant row, no `display_kind`
(`close_interrupted_tool_sequence` in `agent/message_sanitization.py`):
"Operation interrupted: waiting for the provider to recover (cycle
1/2)." during a recovery wait, and other "Operation interrupted…"
wordings elsewhere (`interruptNotices` in `internal/hermes/client.go`
lists them with their files). The stop and the closed stream write the
same rows; the stop's being an explicit one changes only the reason
hermes gives in the result of a tool it cancels. `history` reads such a
row as a failed turn (see [Why-trace and history](#why-trace-and-history)).
Hermes's own context for the model does not: the row goes to the
model on the next turn as an assistant message, the ghost's own words as
far as the model can tell. That is hermes's to change, and haunt leaves
it.

**Waking.** The boot event starts the waking indicator ("waking up…")
the moment the ghost is on screen, and the boot turn carries it
(`hermesTurn.waiting`), so it stays through the gateway's start and the
turn and comes down just before the greeting is shown. A boot turn that
is never sent (no `llm` grant yet, no allowance) takes it down once the
gateway is up instead (`hermesBrain.drop`, `whenUp`); one that fails
shows why, as a person's turn does. A first start measures roughly: the
container 2 s, the gateway 4 s, then the boot turn, most of it the
model. Words that queue show the indicator from the moment they are
queued ("waking up…" until the gateway is up, "thinking…" after), unless
the waking indicator is up, which they wait under.

**Pose tags.** A reply may open with the pose to wear as it is said,
`[happy] Oh, it's you.` (`hermes.CutPose`). Calling the `face` tool
first costs a model round trip before the words, and a model left to
the tool reaches for it before nearly every greeting; the tag rides on
the words instead. The stream is held while its opening could still be a tag
(`MaybePose`), as it is while it could be `NO_REPLY`, and the opening is
that of the part arriving, so a reply after interim words opens with its
own tag; the pose goes on with the first words shown, or, for a turn not
streamed, with the finished reply. Any short bracketed opening is taken off, a pose the
shell has or not: told to open with a bracket, a model also opens with
labels for what it did (`[memory add]`) or stage directions, and none of
those is for the balloon. One that names no pose falls back to the
lexicon. A little punctuation may come before the bracket, `… [surprised]
First words`, where the model reads the tag as part of the speech; the
punctuation is the ghost's and stays in the words. The `say`
tool reads a tag the same way. Hermes keeps the tag in the session, so
the client takes it off assistant messages as it reads them, and
`history` and the evals see the words alone; the evals fail a trial
with a tag anywhere else in a reply (`always/no-pose-tag`). The `face`
tool stays, for a face with no words.

**One indicator.** Waits nest (a turn, a tool it calls, the image
backend under that), and only the newest live one draws (`g.waiters`).
An indicator gives up the balloon to anything that replaces it, such as
the greeting or a `say`, and comes back after.

**Typing.** A line finished while the input line is open is held and
shown when it closes (`holdLine`), the latest one only. A reply held
this way has reached the screen when it is shown, so a reminder it
carries is resolved then, not before.

**Silence.** Hermes does not filter `NO_REPLY` on this path. A final
reply that is `NO_REPLY` alone, or at the start or end of a reply under
300 characters, is dropped: no balloon, and the why-trace records a
suppressed turn (`Silent`). Looser than hermes's exact-match rule on
purpose, for small models that decorate the token. While streaming,
deltas are held back as long as the part arriving could still be the
token (`MaybeSilent`); an unprompted turn is not streamed at all. A
silent reply after interim words takes those words down with it.

**Pictures.** Words that follow a `draw` call wait until the picture has
been up for its twenty seconds rather than painting over it.

## Events and the envelope

```
[haunt event] kind=<kind> key=value key="quoted value" …
```

One line, user role, no prose. Rules, each from a documented failure
elsewhere: one envelope per kind and no shared preamble (a shared
heartbeat instruction gets answered in place of the job); structured
fields rather than "say one short thing" (the model reasons in the third
person when the prompt does); the person's words never carry an
envelope; and the driver decides whether to send at all, so the model's
only decision is whether this event is worth a word.

| Source | Sent as |
|---|---|
| typed text, `haunt say` | the turn input, verbatim |
| poke | answered by the driver from a table; not sent |
| double-click | the driver's menu; not sent |
| menu "What can you do?" | `kind=help` |
| boot | `kind=boot first_time=…` inside the 2 s boot window |
| idle | `kind=idle minutes=N last_window="app: title" seen_for=42m` (`seen_for` only when watching) |
| session fact | `kind=session shape=… app=… title=… minutes=N count=N` |
| occasion | `kind=occasion name="morning"` |
| poll answer | the driver files the intention; nothing is sent until it comes due |
| intention due | `kind=intention_due name=… text=… asked=true\|false` |
| goal gone quiet | `kind=goal_quiet name=… goal=… goal_name=… held="8 days" last_seen=never` |
| neighbour spoke, drew, was poked | `kind=observed from=… action=… text=…` |
| neighbour `tell` | `kind=told from=… text=…` |
| window changed | not sent per change; rides on the next turn's context |
| look result | returned synchronously from the `look` tool |

`goal_quiet` is its own kind rather than an `intention_due` with prose,
because a finding sent as prose made the model skip the skill, resolve
the finding itself, and answer in the third person.

Compression keeps envelope lines verbatim: they are real user messages
under the compressor's user-message budget, and nothing in the API marks
them low-value.

## Gating

`allowInitiative` in `curiosity.go` decides whether an unprompted event
may reach the model, from causality and the allowances, **before the
event is sent**. The `llm` capability gates too: without it no unprompted
event is sent, and the person's words ask for it in the balloon first.
Idle, session, occasion, observed and told pass through curiosity;
intention-due events pass through the obligation path. **Attention is
charged when the ghost speaks, not when it wakes**: a `NO_REPLY` refunds
the spend. The bill is bounded separately by the event sources' own
cooldowns (the session model's, and one wake an hour about neighbours)
and hermes's rate limits.

Quiet hours, the fullscreen rule and reduced motion are checked at the
effect, so a turn that runs into a quiet hour is suppressed at the screen
and the intention stays on the books.

**Concurrency.** One turn per ghost. An unprompted event arriving during
a turn waits for it if it is still worth raising afterwards (boot, told,
session, occasion, intention_due): it is charged on arrival, a newer one
about the same subject replaces it, at most four wait, and one that has
waited more than ten minutes, or would now land on a fullscreen window,
is dropped and refunded. One sent a minute or more late carries
`waited=Nm`. Idle and observed are only true in the moment and are
dropped.

## The tool surface

Every tool goes through the gate `act.go` applies to intents: capability
active, per-tool consent in the balloon on first use, the hourly tool
budget, and the three visible marks for tools that change the world.
Tools that only speak or read are not marked.

| Tool | Args | Returns | Gate |
|---|---|---|---|
| `say` | `text`, `linger_ms?` | ok | none |
| `ask` | `text`, `choices[1..9]` | the choice, `""` if dismissed | none; blocks |
| `ask_text` | `prompt` | typed text | none; blocks |
| `face`, `clip` | `pose` / `name` | ok | reduced motion, fullscreen |
| `draw` | `prompt`, `style?`, `see?`, `like?` | the picture's name and prompt | `imagine`, image budget, fullscreen |
| `show` | `id` | the picture's name and prompt | `imagine` |
| `pictures` | `count?` | the gallery, newest first | `imagine` |
| `style` | `set?`, `reset?` | the style it draws in | a set or reset only on the person's turn; taint |
| `search` | `query`, `count?` | results, in the untrusted-content envelope | `web`, declared, search budget, the unprompted policy; taint after a file |
| `read_page` | `url`, `from?` | a piece of the page, in the same envelope | `web`, declared, search budget, the unprompted policy; taint after a file |
| `list_files` | `path?` | the shared folders, or a folder's entries, or a file's line | `files`, declared, a folder shared, the unprompted policy |
| `read_file` | `path`, `from?`, `question?` | a piece of the file, or a picture's description, in the file envelope | `files`, declared, a folder shared, the unprompted policy; a picture also the vision backend and budget |
| `search_files` | `text`, `path?`, `names_only?` | matches, each with a `from`, in the file envelope | `files`, declared, a folder shared, the unprompted policy |
| `look` | `question?` | description | `screen`, vision backend, ignore list, fullscreen |
| `intend` | `text`, `delay_ms?`, `name?` | name | taint |
| `resolve`, `set_aside` | `name` | ok | taint |
| `intentions` | | list | none |
| `tell` | `ghost`, `text` | ok | `tell`, taint |
| `<server>__<tool>` | the server's schema | the result, cut to 2000 chars | `tools`, per-tool consent, tool budget, marks, taint |

Every verb is listed whatever the manifest declared, and refused when it
is used, with one exception: a tool with `declared` set in the table
(`search`, `read_page`, `list_files`, `read_file`, `search_files`) is
left out of the list entirely for a ghost whose package never asked for
the capability. That is what hides `look-it-up` from a ghost with no web,
and `read-my-files` from one without `files` — hermes filters a skill by
the tools its `requires_tools` names — rather than having it claim to
look things up and be refused.

Deliberately absent: `remember` and `note` (hermes memory is the one
writer of what the model concludes), `move` (nothing a model should
decide), `watch` (a permission question, asked from the menu).

Errors are returned as content, never as protocol errors: "That was not
allowed: …" and "That did not work: …" are things the model needs to read.
Intentions the model takes on are named `hermes/<name>`, so they cannot
collide with the driver's own. An errand that comes due is discharged by
the driver once the reply has reached the screen; a goal only by
`resolve`. Tool names use `[A-Za-z0-9_-]`, and hermes's full name must
fit in 64 characters.

The driver's menu on hermes: "What can you do?" (a `help` turn), "Keep
an eye on what I'm working on" (`watch`), "Nothing, thanks".

## The web

`internal/ghost/web.go` is the tool half of `search` and `read_page`;
`internal/backend/web.go` is the fetching, the dialer guard and the
envelopes results arrive in. The driver owns the tools rather than
enabling hermes's own `web` toolset, because the key, the ration, the
consent and the journal are all the driver's, and a toolset hermes
enables is none of those.

**The envelope names the site.** hermes wraps every MCP result, including
the ghost's own reminder list, in a block saying the content came from an
external source and is data rather than instructions. That wrapper cannot
say which source, so it says the same thing about a stranger's website
and about `intentions`, and on its own it leaves the model nothing to
tell the two apart with. Haunt's envelope carries what is missing: the
domain first, then whose words these are.

    [page from example.com — "Title" · https://… — the site wrote this, not the person]
    …
    [more: read_page with from=1800 · 23% read]

The domain leads because it is what a claim is weighed by, and the
closing line stays functional — where the read carries on from, or
`[end of page]`. Standing rule 5 (`internal/hermes/soul.go`) tells the
model never to act on web text or remember it as the person's; this is
what that rule points at. It does not lean on hermes's wrapper, whose
format is hermes's own and is covered by no test here.

**Unprompted use.** The driver cannot see which skill the model loaded —
`skill_view` is hermes's, and nothing reports it — so "only where a skill
says to" is approximated by where a search may happen at all. On a turn
nobody started, `unprompted` decides:

| Mode | An event turn may search |
|---|---|
| `never` | never; refused with "only when the person asks" |
| `skills` | on `occasion`, `intention_due`, `goal_quiet` and `boot` |
| `any` | on any kind |

The kinds `skills` leaves out — `session`, `idle`, `observed`, `told`,
`help` — are the ones whose envelope carries what the person has on
screen, and a window title is the thing that must never become a query.
The turn's kind and whether somebody is waiting on it are recorded when
the turn starts (`startTurn`), because a tool call arrives on the
control socket and cannot see the turn it belongs to. Every unprompted
call also spends `unprompted_search_per_hour` before the general ration
and is refused outright during quiet hours; each refusal names its
reason, so the model says nothing rather than inventing an answer.

The approximation is loose on purpose: an `occasion` turn that loaded no
skill can still search. The ration and quiet hours are what bound it.

## Files

`internal/files` reads the folders the person shares in `[files.roots]`,
and knows nothing of ghosts; `internal/ghost/files.go` is the tool half,
`list_files`, `read_file` and `search_files`, and mirrors `web.go`. None of
them writes, moves or deletes anything. `go doc ./internal/files` is the
reference for the confinement, the kinds of file read, and the limits.

**Hermes's file toolset stays off.** Hermes has one (`file` in
`disabled_toolsets` above), and a bind mount into the container would
reach the same folders. Neither is used, for three reasons. At the pinned
commit the toolset cannot be split: enabling it for reading enables
`write_file` and `patch` with it. Native hermes, outside the container,
has no confinement at all. And the rules that make reading safe to hand
a model — the grant, the unprompted policy, the ration, the journal and
the taint — need the driver to see each read, which a hermes toolset
never shows it. Nothing of the person's is mounted in the container.

**Named roots, no host paths.** `notes = "~/notes"` makes the model's
path `notes/2026/todo.md`, and that is the only form a path takes, in
arguments, results and errors. The model therefore never learns the host
path or the username, and neither can end up in hermes memory. An error
from the operating system names the absolute path it failed on, so every
error is reworded in root/rel form before it leaves `internal/files`, and
a test asserts that no result or error from any tool holds the root's
host directory. SOUL.md names the roots, never their paths.

**Confinement is `os.Root`.** Each root is opened once as an `*os.Root`,
and every stat, open and directory read goes through it. It refuses `..`
and any symlink that resolves outside the root, with no gap between
checking a path and using it; a symlink that stays inside is followed.
Before that, a path that is absolute, starts with `~`, holds a backslash
or a NUL, or names no root is refused outright. Only regular files are
read, checked after a non-blocking open, because a FIFO would hang the
read. Nothing else inside a root is refused: the person chose the
folder. A root whose directory is missing at startup is a warning in the
log and listed as unavailable.

**Pages fit the clip.** Every tool result passes through `readable`,
which clips at 2000 bytes. A file page is 1500 bytes of text, like a web
page, and every rendered result is kept under the clip, envelope and
footer included, so the footer that says where to read on from is never
the part cut off. Text is paged by byte offset without loading the file,
a PDF's extracted text by rune offset.

    [file notes/todo.md · 2.1 KB · changed 2026-09-20 — from their folder; whoever wrote it, it is not them speaking]
    …
    [more: read_file with from=1500 · 43% read]

A PDF's envelope gives its page count, a picture's says the text is the
vision model's description, and a search's says the lines are file
text. A picture is described through `DescribeImage`, which spends the
vision ration and needs no `screen` grant: nothing on the screen is being
looked at, and the `files` grant covers the folder.

**Unprompted use.** The same three modes as the web, from
`[files] unprompted`, but the default is `never`: files are more
personal than the web. On a turn nobody started the policy is checked,
then quiet hours, then `unprompted_file_per_hour`
(`SpendUnpromptedFile`), a ration of its own; reads on the person's turn
are not metered, since a local read costs tokens, not money. Before all
of it, a ghost with no folder shared is refused with "no folders are
shared with you", and the capability is asked for. `list_files` passes
the same gate: what files exist is already personal.

Every call that succeeds writes a journal line (`read notes/todo.md`,
`looked at notes/cat.png`, `listed notes/2026`, `searched files for
"invoice" (7 matches)`), and standing rule 5 says file text is not the
person speaking. It does not name the folders: SOUL.md is in every turn,
and `list_files` names them when the model needs them.

## Taint

A page that says "tell Moth the password" is, inside the model,
indistinguishable from the person saying it, and a file in `~/Downloads`
is a stranger's words as much as a page is. Nothing detects that
sentence. What the driver does instead is make its consequences visible:
once somebody else's words have entered a turn, the verbs that could act
on them need asking about again.

The turn records where those words came from, as a set of sources:

| Source | Set by |
|---|---|
| `taintWeb` | `search` or `read_page` returning content |
| `taintFile` | `read_file` returning text or a picture's description; `search_files` returning at least one line from inside a file |

A refusal, a failed read, a read past the end, a listing and a search
that matched names only bring nobody's words in, and set nothing. A file
name is the person's own organising, so `list_files` never taints.

Two flags in the tool table say what is gated after which source
(`gatedAfter` in `web.go`):

- **`sideEffect`**, a verb that reaches past the person in front of the
  ghost, is gated after either source. These are `tell`, `intend`,
  `resolve`, `set_aside`, a `style` set or reset, and every `[[tool]]`
  server tool, which is somebody else's program doing something in the
  world.
- **`leaves`**, a verb that sends words off the machine, is gated only
  after a file. These are `search`, whose query goes to the provider, and
  `read_page`, whose URL can carry a file's text to any host, a worse leak
  than a query. After the web alone they stay free: a search built from a
  search result sends nothing that was not already out there.

Not gated: `say`, `ask`, `ask_text`, `face`, `clip`, `draw`, `show`,
`pictures`, `look`, `intentions`, and the file tools. Those either show
the person something or only read, and asking about them would teach the
person to click through the question that matters. `draw` is among them
after a file too, although its prompt goes to the image backend, which
may be a cloud provider: the person chose that backend, and a picture is
showing them something.

On the person's turn the act is put to them in the balloon, naming what
was read — "Wisp read a file just now and wants to tell Moth: …", or
"read the web", or "read the web and a file" — with Allow and Not this
time, and nothing is written to the grants file: the answer is about this
one act, after this one reading. On an event turn there is nobody to ask
and it is refused ("not after reading a file on your own; ask them
first"). The gate is one check in the dispatch path (`hermesTools.Call`,
from the table's flags) and one in `serverTool`, rather than a line in
each tool function, so a verb added later is gated by its table entry.

The state is a mutex-guarded `turnState` on the ghost rather than a field
on `hermesTurn`: a tool call arrives on the control socket and is served
on a connection of its own, so two calls in one turn can overlap if hermes
ever issues them in parallel, and a security decision that depends on a
client's issuing discipline is not one. The same state holds who started
the turn and its kind, which the web and file gates both read.

Hermes memory is not haunt's to gate, whichever store it is, so nothing
stops the model writing what a page or a file said into it. The SOUL
clause forbids it, and that is the whole of the prevention. What haunt
adds is detection: `haunt history` and `haunt journal -echoes` flag a
memory write whose words repeat a web or file read's (the echo check,
under [Why-trace and history](#why-trace-and-history)). Haunt flags such
a write and never gates it. A provider that records every turn on its
own keeps the page or file text whatever the model does, and the check
does not see that.

## Memory

Memory is hermes's, as the person set it up: by default `MEMORY.md`,
`USER.md` and the `memory` tool; or an external provider beside them or
alone, chosen with `hermes -p haunt-<ghost> memory setup`. Haunt writes
no memory config, reads no store and offers no memory verb of its own;
it keeps `memory` among the platform toolsets because that is what
exposes a provider's tools too. SOUL.md and the skills speak of what the
ghost remembers, never of a tool, so they hold for any store. Haunt
keeps no belief store. A correction is the model editing what it
remembers; for built-in memory, hermes blocks removals only in its
background review fork, not in a live turn.

## Skills

Hermes skills are `SKILL.md` procedures in the agentskills format. The
model sees a one-line index and loads one with `skill_view`; it writes
its own with `skill_manage` into the profile's `skills/`, which the
curator marks stale after 14 days and archives after 30.

What the pinned hermes source says, and haunt depends on:

- The index (`build_skills_system_prompt`, `agent/prompt_builder.py`)
  lists the local skills then every external dir, skipping a name already
  listed, so a learned skill shadows one of haunt's with the same name.
- Each skill is filtered by `_skill_should_show` against the toolsets the
  agent's tools belong to. MCP tools register under `mcp-<server>`
  (`tools/mcp_tool_registration.py`); the alias `haunt` is not consulted.
  So haunt's skills say `requires_toolsets: [mcp-haunt]`. `skill_view`
  does not apply the gate.
- A skill's category is the path between its dir and the root it was
  found under. Haunt installs into `haunt-skills/ghost/`, `haunt-skills/
  haunt/` and `haunt-skills/hermes/`, so the index groups them.
- Only 60 characters of `description` are shown
  (`SKILL_PROMPT_DESC_LIMIT`, `agent/skill_utils.py`); `name` is at most
  64 and `description` 1024. The index lists the front-matter name and
  `skill_view` finds the directory, so the two must agree.
  `internal/ghost/skills_test.go` enforces the limit and that every tool
  named in haunt's skills exists.
- `requires_tools` checks entries against the agent's valid tool names.
  An MCP tool is `mcp__<server>__<tool>` with both parts folded to
  `[A-Za-z0-9_]` (`sanitize_mcp_name_component`); `MCPToolName` does the
  same fold, and `Unmet` reports which of a package's skills hermes will
  hide. The driver logs a warning for each when it answers a tool list.
- `skills.auto_load` would render named skills in full on every turn;
  haunt does not set it. The index header already says to load a
  relevant skill, and live runs load the right one from the index alone.
- `skills.guard_agent_created` scans what `skill_manage` writes for
  dangerous shell patterns. It does not check which tools a skill
  assumes; `haunt journal -skills` flags a learned skill naming a tool
  from a disabled toolset.
- `skill_view` installs the packages a skill's front matter names in
  `deps:` (one name or a list) through hermes's package manager when the
  skill is loaded (`tools/skills_tool.py`), unless installs on demand are
  off (`security.allow_lazy_installs: false`, `pm/install.py`). The model
  writes skills, so the profile pins them off: the skill still loads,
  with a note that its packages could not be installed, and `haunt
  journal -skills` flags a learned skill that declares any. The same
  setting stops hermes fetching a plugin the config names at start; a
  ghost runs on what the image has.
- A skill's ``!`cmd` `` snippets run in a shell when it is viewed if
  `skills.inline_shell` is on (`agent/skill_preprocessing.py`), outside
  any toolset. Hermes defaults it off; the profile pins it off.
- Seeding one bundled skill into an opted-out profile is not possible
  (`hermes skills reset --restore` seeds only essentials; `hermes skills
  install` fetches the live repo), which is why the four allowed bundled
  skills are vendored in `third_party/hermes-skills`.
- Nothing in a skill directory is imported by hermes, but `skill_view`
  lists support files for the model to read; haunt copies only `.md`.

The index costs about 1,100 characters of fixed header once any skill is
visible, plus about 75 per skill.

## Why-trace and history

Each hermes turn records its input, reply or silence, tools used, and
the window in front of it, in the ghost's store (`why.go`); the last
dozen are kept. `haunt history` reads `GET
/api/sessions/{id}/messages?order=latest&limit=N&include_compacted=true`
from the gateway, skips tool traffic, and prints envelopes as `event:`.

That read is the whole conversation across compression. Hermes resolves
the id to the live tip and returns the verified compression lineage, root
to tip, as one list (`session_id` on each row says which segment it came
from); `include_compacted=true` adds the rows an in-place compaction
archived. Rows the compression carried forward are deduplicated, and the
list is in conversation order. `order=latest&limit=N` takes the newest N
of that merged list (the server caps N at 500), and the handoff rows
count towards N. Nothing on a row says it was compacted: `active` and
`compacted` are not in the response. A compaction's handoff comes back as
a `role: user` row with `content: ""` and `display_kind: "hidden"`,
placed where the compaction happened, which can be inside a turn between
a lookup and its reply; a handoff merged into a real message comes back
as that message's own content. Every `hidden` row is skipped, before it
can end a turn. A `failed_turn` row is hermes's notice closing a turn
that failed, and so is a plain assistant row straight after a tool
result whose text opens with one of hermes's interrupt notices
("Operation interrupted: " or "Operation interrupted during retry (",
`interruptNotices`), which closes a turn interrupted after a tool. Either
is read as a failed reply with no words
(`Message.Failed`), which takes the turn's calls, `-json` gives it as
`"failed": true` and `-why` as `(the turn failed)`, and the evals record
the trial as an error rather than grade it. `-why` joins the trace
and also shows hermes's own tool calls (memory, session search, skills)
from the session's messages. The system prompt is not available: the API
server returns only `has_system_prompt`, and haunt does not read hermes's
database.

`-why` and `-json` read every call whole (`Client.Transcript`), `-n`
counting turns; `-why` cuts each call to a line only to print it
(`Lookup.Short`). They need the whole result for the echo check
(`cmd/haunt/echo.go`), which flags a memory write whose words repeat a
read of somebody else's:

| | |
|---|---|
| A write | a call `hermes.MemoryWrite` recognises with new text in it: `memory` (any action; the text is `content` or `new_text`, every operation's in a batch) or holographic's `fact_store` add and update. A removal saves no words and is never flagged. |
| A read | one of the ghost's verbs that taint a turn (`ghost.UntrustedRead`): `search`, `read_page`, `read_file`, `search_files` |
| Window | a read in the write's own turn or the `echoTurns` (3) before it, event turns included |
| Overlap | both texts lowercased and cut into words (letters and digits); every `echoWords` (3) consecutive words is a run; the write echoes the read when they share at least `echoShared` (2) distinct runs that the person's own words in the window do not also have |

Three-word runs keep a fact's own phrases ("name is bartholomew",
"allergic to cats") when the model rewrites the sentence around them;
one shared run is what a page about the same subject shares by chance,
and two is a fact carried over. The person's words are subtracted so that
saving what they said after looking it up is theirs, whatever the page
also said. Within a turn the session does not keep the order of hermes's
calls against haunt's, so a read later in the same turn counts too. The
read that shares most runs is the one named, the latest on a tie.

`-json` gives each flagged write as an entry of `echoes`, `-why` marks it
with a `!` line under the memory call, and `haunt journal -echoes` lists
every one in the session (it reads the whole session, so it too needs the
gateway). The check flags and never gates. It sees only what the session
records and only wording: not hermes's post-turn background review, which
writes memory outside the session; not a provider that records every turn
on its own; not a paraphrase that keeps no three-word run, or keeps only
one; and not a read older than the turns read back.

## The container

`scripts/hermes-docker` runs the image with: the profile read-write; a
private stand-in for the hermes root (hermes writes `shared-state.db`
there, and the real root holds the other ghosts' profiles); the haunt
binary read-only, because hermes spawns `mcp-serve` from the path in
`config.yaml`; the control-socket directory; and the keys from
`HAUNT_HERMES_ENV` as environment. Host networking, with
`API_SERVER_HOST=127.0.0.1` in the profile's `.env` (hermes lets `.env`
override the environment). It sets `HERMES_GATEWAY_NO_SUPERVISE=1` (or
the image's s6 supervisor and boot reconciler start gateways that race
haunt's), `HERMES_DASHBOARD=0`, and `HERMES_UID`/`HERMES_GID` instead of
`--user`, which the image refuses. Before starting, it removes the
profile's leftover container and the stopped gateway's
`gateway_state.json` and `gateway.pid`: a gateway stopped by a signal
leaves its state saying it should be running, and the boot reconciler
would start a second gateway from it on the next start.

## Moving the pin

The image tag is in `scripts/hermes-docker`,
`scripts/hermes-numpy.Dockerfile`, the `HERMES_IMAGE` default in the
`Makefile` and [The brain: hermes-agent](../user/hermes.md); the commit
is at the top of this page. After building the new image, `make
hermes-check` runs `TestHermesToolsets`
(`internal/hermes/hermescheck_test.go`, build tag `hermescheck`). It
reads `apiServerToolsets` and `disabledToolsets` from the Go code and
pipes `internal/hermes/testdata/toolsets.py` into the image's own Python
(`/opt/hermes/.venv`), with no network and a throwaway `HERMES_HOME`.
The script enables every plugin it finds in that home's config, imports
`model_tools` (which imports every `tools/*.py` and loads the plugins),
and prints what hermes's own tables hold: the static `TOOLSETS`
(`toolsets.py`), the toolsets registered into `tools/registry.py`,
`get_plugin_toolsets` (`hermes_cli/plugins.py`), the platform-gated
toolsets (`TOOLSET_SESSION_PLATFORMS`,
`hermes_cli/toolset_scope.py` `_TOOLSET_PLATFORM_RESTRICTIONS`) and
`CLIENT_SURFACE_TOOLSETS`. MCP toolsets are not among them: they come
from the profile's `mcp_servers`, which haunt writes.

Every toolset gets a verdict. It passes when the profile names it,
allowed or disabled, or as the person's (`personsToolsets`: the
`context_engine` a chosen context engine brings); when hermes keeps it
to another platform or to a
desktop client; when it has no tools in the image; or when every tool it
has is one a ghost is given or one `disabled_toolsets` subtracts (hermes
subtracts by tool, so `disabled: [web]` covers `search`). A plugin
toolset has to be named outright, since hermes turns it on beside an
explicit list. Anything else fails, naming the tools nothing would take
away. Warnings, which do not fail the check: a `disabledToolsets` entry
the image does not know, a plugin whose declared tools did not register,
and a subtracted tool `absentTools` (`skills.go`) does not match. The
check also fails when `ghostTools` (`skills.go`) is not exactly the
tools of the allowed toolsets; a memory provider's or a context
engine's tools are the person's and not among them.

Some of what the client reads is hermes's wording, which no check
covers, and is read again in the new source by hand:

- `interruptNotices` (`internal/hermes/client.go`): the texts hermes
  closes an interrupted turn with. Search the source for "Operation
  interrupted" and for callers of `close_interrupted_tool_sequence`;
  the comment lists every place the current pin writes one. A new
  wording reads in `history` as the ghost's words.
- `providerSaidMark`: the "Provider said:" line of a failed turn's
  notice (`agent/turn_failure_copy.py`). Without it a failed turn's
  error loses its reason.
- `Client.stopRun`: `POST /v1/runs/{run_id}/stop` answering before the
  run is over, and the terminal statuses `GET /v1/runs/{run_id}` reports
  (`gateway/platforms/api_server_runs.py`,
  `api_server_run_idempotency.py`).

## Tests

`cmd/haunt/hermes_test.go` runs a real ghost in headless sway against a
fake `hermes` executable (`cmd/haunt/testdata/fakehermes`) that reads
the profile, spawns `haunt mcp-serve` with hermes's filtered environment,
and speaks the captured SSE format from a script (`FAKEHERMES_SCRIPT`:
match, delay, interim words, tool call, reply). Its runs outlive their
requests as hermes's do: a run whose client has gone ends only when it
is stopped (`POST /v1/runs/{run_id}/stop`, which answers "stopping" and
ends the run a moment later) or a few seconds on, when hermes would have
noticed, and `GET /v1/runs/{run_id}` reports its status.
`newHermesRig` is the shared rig. The fake gateway also logs what it finds under
`skills.external_dirs`.
`internal/hermes` tests replay captured SSE and cover the profile merge
and the skills install. The evals are the live counterpart, on a real
model ([Hacking](hacking.md#evals)).
