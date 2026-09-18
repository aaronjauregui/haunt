# Hermes integration

How a persona ghost thinks. `internal/hermes` is haunt's side of the seam;
`internal/ghost/hermesbrain.go` is the brain that uses it, and
`internal/ghost/hermestools.go` is the tool surface hermes calls back
into. Every hermes identifier, path and payload here is checked against
hermes-agent at commit **6005aa1**, which `scripts/hermes-docker` builds
its image from. When the pin moves, recheck them: the places that depend
on hermes internals are named below.

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
  `external_dirs` skills from a live turn (only the background review
  fork is refused, in `tools/skill_manager_guards.py`), and no config key
  makes them read-only. The demo and the test rig `chmod -R u+w` before
  removing a profile.
- **`config.yaml`**, merged rather than replaced. Haunt owns
  `platforms.api_server`, `platform_toolsets`, `agent.disabled_toolsets`,
  `tools.tool_search`, `mcp_servers.haunt`, its own entry in
  `skills.external_dirs`, and `_config_version` if absent; it defaults
  `memory.write_approval` to false and `skills.guard_agent_created` to
  true. Everything else, the model above all, stays as the person set it
  unless `[hermes.model]` in the haunt config says otherwise.
- **`.env`** gets `API_SERVER_KEY` (32 random bytes, hex),
  `API_SERVER_PORT` (a free loopback port), `API_SERVER_HOST=127.0.0.1`
  and `HAUNT_GHOST`, each chosen once and kept so the CLI can find a
  running gateway and a restart does not move it. Other lines are kept.
- **`.no-bundled-skills`**, only before hermes's first seed. Without it a
  new profile lists hermes's 58 bundled skills and the ghost claims to do
  mail and documents. The marker affects seeding only; the essential
  skill `hermes-agent` is still seeded.

Never touched: `memories/MEMORY.md`, `memories/USER.md`, `state.db`.

The toolset restriction:

```yaml
platform_toolsets:
  api_server: [memory, session_search, skills, haunt]
agent:
  disabled_toolsets: [terminal, file, browser, code_execution, delegation,
    web, cronjob, computer_use, todo, vision, image_gen, tts, kanban,
    connections, clarify]
tools:
  tool_search: {enabled: "off"}
```

The MCP toolset is named after its key under `mcp_servers`. Naming any
MCP server enables only those. `disabled_toolsets` is subtracted last, so
a later hermes that widens an explicit list still cannot hand a ghost a
shell. `clarify` is left out because the API server passes no clarify
callback and the tool only ever answers that it is unavailable; haunt's
`ask` is how a question is put. Tool search must be off: with it on, the
MCP tools and `session_search` hide behind a lookup a small model does not
think to use. The model receives exactly `memory`, `skill_manage`,
`skill_view`, `skills_list`, `session_search` and `mcp__haunt__…`.

## The gateway

`Gateway` runs `HERMES_HOME=<profile> hermes gateway run
--external-supervisor --replace` in its own process group, waits on
`GET /health`, restarts it with the troupe's backoff shape, and stops it
on shutdown. A gateway already answering on the profile's port is adopted.
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
kept in the store under `_haunt/hermes_session`. Compression may end a
session and continue in a child with a new id, so the id is taken from
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
any of `assistant.delta {delta}`, `tool.started {tool_name, preview,
args}`, `tool.completed {tool_name}`, `tool.failed`, `tool.progress`,
then `assistant.completed {content, completed, partial, interrupted}`,
`run.completed|run.failed|run.cancelled`, and `done`; an exception is
`error {message}` before `done`. `tool.progress` with `tool_name`
`_thinking` is reasoning and is ignored. Captured frames live in
`internal/hermes/testdata`.

On the driver's side: the first-token deadline (`[hermes]
first_token_timeout`, 30 s) is on the first sign of life, and a tool
event counts; the waiting indicator comes down on the first delta; the
talk animation runs while deltas arrive; tool events feed the why-trace
and suppress the lexicon face when the model wore one itself. One turn
in flight per ghost. The person's words queue behind it.

**Silence.** Hermes does not filter `NO_REPLY` on this path. A final
reply that is `NO_REPLY` alone, or at the start or end of a reply under
300 characters, is dropped: no balloon, and the why-trace records a
suppressed turn (`Silent`). Looser than hermes's exact-match rule on
purpose, for small models that decorate the token. While streaming,
deltas are held back as long as the text could still be the token
(`MaybeSilent`); an unprompted turn is not streamed at all.

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
| `draw` | `prompt`, `fresh?` | image path | `imagine`, image budget, fullscreen |
| `look` | `question?` | description | `screen`, vision backend, ignore list, fullscreen |
| `intend` | `text`, `delay_ms?`, `name?` | name | none |
| `resolve`, `set_aside` | `name` | ok | none |
| `intentions` | | list | none |
| `tell` | `ghost`, `text` | ok | `tell` |
| `<server>__<tool>` | the server's schema | the result, cut to 2000 chars | `tools`, per-tool consent, tool budget, marks |

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

## Memory

Hermes memory as it comes: `MEMORY.md`, `USER.md`, the `memory` tool,
`session_search`, the post-turn review, and `memory.write_approval`
defaulted off (a ghost that asks permission to remember your cat's name is
not a companion). Haunt keeps no belief store. A correction is the model
replacing a memory entry; hermes blocks removals only in its background
review fork, not in a live turn.

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
/api/sessions/{id}/messages?order=latest&limit=N` from the gateway, skips
tool traffic, and prints envelopes as `event:`; `-why` joins the trace
and also shows hermes's own tool calls (memory, session search, skills)
from the session's messages. The system prompt is not available: the API
server returns only `has_system_prompt`, and haunt does not read hermes's
database.

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

## Tests

`cmd/haunt/hermes_test.go` runs a real ghost in headless sway against a
fake `hermes` executable (`cmd/haunt/testdata/fakehermes`) that reads
the profile, spawns `haunt mcp-serve` with hermes's filtered environment,
and speaks the captured SSE format from a script (`FAKEHERMES_SCRIPT`:
match, delay, tool call, reply). `newHermesRig` is the shared rig. The
fake gateway also logs what it finds under `skills.external_dirs`.
`internal/hermes` tests replay captured SSE and cover the profile merge
and the skills install. `make spin-ai` is the live counterpart
([Hacking](hacking.md)).
