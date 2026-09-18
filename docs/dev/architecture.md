# Architecture

Haunt is a ground-up, Linux-native, Wayland-first ukagaka ghost driver in
Go: one static binary, no cgo. This page is the map across packages. Each
package's own comment says why it is shaped as it is; read it with
`go doc ./internal/<name>`.

## The one rule

**The driver owns effects; the brain owns judgment.** The driver holds
everything the ghost cannot be trusted with: the screen, the sockets,
persistence, backend keys and budgets, consent, quiet hours, the
fullscreen rule and the allowance for acting unprompted. A brain is
untrusted logic that receives events and emits intents, and the driver
validates and executes them. What the driver must not own is the ghost's
cognition: what to remember, what an utterance is about, whether an event
is worth a word, what to say. For a persona ghost that cognition is
hermes-agent's, and haunt keeps no parser, memory or belief store of its
own beside it ([Hermes integration](hermes.md)).

## Packages

```
cmd/haunt            the CLI: run, dev, say, new, shell init, pack, install, perms, journal, history, mcp-serve
cmd/haunt-seriko     ukagaka shell art converter (separate binary on purpose)
brainkit             public SDK for brains in Go; re-exports proto
skills/              haunt's own hermes skills, embedded
third_party/         patched go-wayland; the four hermes skills a manifest may name
protocol/            Wayland protocol XML the client bindings are generated from

internal/
  proto              the schema: Event and Intent, shared by driver, bus and brains
  bus                the single channel every trigger flows through; records and replays
  ghost              runs one ghost: manifest, shell, brain, intents, consent, initiative, intentions, awareness, polls, the hermes brain and its tool surface
  troupe             runs the resident ghosts: supervision, restart, shared placement, shared budgets, the stage
  brain              hosts scripted brains: WASM (wazero) and native processes over stdio JSONL
  hermes             haunt's side of hermes-agent: profile, gateway, session, turns, envelope, skills install
  control            the per-ghost Unix socket: inbound messages, and tool_list/tool_call for mcp-serve
  mcp                a small MCP stdio client: start a server, list its tools, call one
  ai                 driver-owned backends for drawing and looking, and the hourly budgets
  shell              the shell format: layers, variants, poses, clips, transforms, hit regions
  sprite             decoded art: premultiplied buffers and alpha input regions
  artimport          OpenRaster drawing to shell
  seriko             SERIKO shell art to shell
  balloon            renders balloons: text, paging, choices, the waiting indicator
  textinput          keyboard scancodes to an editable line (US layout, pure Go)
  wlclient           the Wayland connection: registry, shm, layer-shell surfaces, input regions, drag
  wayland/           generated bindings: layer-shell, virtual keyboard and pointer
  desktop            compositor IPC: fullscreen state and the focused window (sway, Hyprland)
  session            the focused-window stream to the shape of the day: dwell, alternation, recurrence, late, morning, returning
  quiet              quiet hours, shush, presence
  store              one SQLite file per ghost, key-value
  library            installed ghosts, author-key pinning, grants, residency
  pack               the signed .haunt format
  config             the user's config file, read once
  hlog               the leveled, topic-divided log
  testenv            a headless sway for tests
```

## A ghost's lifetime

`cmd/haunt run` resolves each target (a directory, or an installed name
whose grants are watched) into a `troupe.Member` and hands them to
`troupe.Run`, which starts each in its own goroutine and supervises it.

`ghost.Run` then, in order: reads the config, loads the manifest, opens
the store, connects to Wayland, loads the shell, opens the control socket,
starts the brain, publishes `boot`, and runs the event loop until the
context is cancelled or the session fails.

The event loop is `bus`: input from the renderer, timers and cron,
control-socket messages, brain intents, backend results, awareness and
session facts, and what the other ghosts did in public all flow through
it with sequence numbers. A session can be recorded to JSONL and replayed
deterministically.

The three kinds of brain receive the same events:

1. **WASM** (`brain.Host`): the module runs in-process under wazero with
   no syscalls. The default for distributed ghosts.
2. **Process** (`brain.ProcessHost`): any language, stdio JSONL, gated by
   the `native-brain` capability.
3. **Hermes** (`ghost/hermesbrain.go`): a `brain.Runner` hosted in the
   driver for a ghost with no `[brain]`. It decides which events reach the
   model, sends them as one-line envelopes, streams the reply into the
   balloon, and offers the ghost's verbs to hermes as MCP tools rather
   than intents.

Intents come back through one dispatcher in `ghost.go`, which validates
each against the schema, the capability grants, consent, quiet, the
fullscreen rule and the allowances, and performs it against the screen,
the store or a backend.

## Consent, initiative, intentions

Three mechanisms in `internal/ghost` carry most of the design's weight
and each has a long comment at the top of its file:

- **`consent.go`**: a declared but ungranted capability is asked for in
  the balloon the first time it is needed, after something the person
  did. "Never ask again" is recorded. Nothing asks from a background loop.
- **`curiosity.go`**: what counts as unprompted is decided by causality,
  not by asking the brain. Within a minute of the person acting, a ghost
  is responding and pays nothing. Otherwise it spends from a persisted
  daily allowance; a second allowance covers things the person put on its
  books; and the troupe holds a ceiling for the room. On hermes the charge
  is made before an event is sent and refunded if the reply is silence.
- **`intention.go`**: a purpose the ghost carries. In the store, not a
  timer wheel; listable; discharged by `resolve`, not by coming due. A
  goal is an intention with no moment, watched for its words in window
  titles by `goal.go`.

## Awareness

`awareness.go` polls the focused window over compositor IPC when the
`window` capability is active, applies the ignore list first, and feeds
`internal/session`, which turns the stream into facts about the day.
Titles that outlive the hour are stored as truncated hashes. `screen` is
separate: `look.go` captures the focused window with `grim` and shows it
to the vision backend, visibly.

## More than one ghost

`internal/troupe` runs a household. Each ghost keeps its own Wayland
connection, bus, store, brain and surfaces, so one crash is one ghost. A
ghost that died after reaching the screen is restarted with backoff and
given up on after enough failures; one that never got there is not
retried. Shared, because they are properties of the room: placement (the
`Room`), the ceiling on unprompted action (`Budget`), the hourly backend
rations, the log destination, and the `Stage`, which fans out each
ghost's public actions to the others as `observed` events.

## Where state lives

| What | Where | Writer |
|---|---|---|
| A ghost's memory, intentions, allowances, why-trace, placement, session snapshot | `ghosts/<name>/state.db`, keys under `_haunt/` hidden from brains | the running ghost only |
| Grants and the pinned author key | `ghosts/<name>/grants.toml` | the CLI; watched by the running ghost |
| Who is resident | `resident` | the CLI |
| The room's spend log | `room-curiosity.json` | the troupe |
| What the model remembers | the hermes profile's `memories/` | hermes only |
| The conversation | the hermes profile's session database | hermes only |
| Control sockets | `XDG_RUNTIME_DIR` (`HAUNT_RUNTIME_DIR`) | |
| Generated images | the ghost's state directory, content-addressed | |

**While a ghost runs it is the sole writer of its store.** Everything
that changes a running ghost goes through its control socket, which is
why the model's tool calls reach the driver over the socket rather than
touching the database, and why `haunt perms` writes a file the ghost
watches rather than the store.

## The control socket

`internal/control` listens on a per-ghost Unix socket. It accepts a
`message` event (`haunt say`, scripts, hooks) and refuses every other
event type. It also answers two request verbs, `tool_list` and
`tool_call`, which is how `haunt mcp-serve` hands hermes's tool calls to
the running ghost; a request makes its connection single-use, and a call
may block for as long as the tool does. A `recent` marker records which
ghost was last interacted with, for `haunt say` without `-ghost`.

## Rendering

Shells are layer-shell overlay surfaces with no decorations and no
taskbar presence; balloons are separate surfaces. Input regions come from
the sprite's alpha, so clicks on transparent pixels pass through.
Per-output scale, fractional scaling and viewporter are handled, outputs
hotplug, and positions are remembered per output. Animation schedules a
wake-up per keyframe rather than running a frame loop; clips that
interpolate are resampled at 15 fps while running. Damage tracking
repaints the bounds of what changed, which is why art is cropped to what
it covers.

The balloon streams text, pages long replies, draws its own waiting
indicator on its own delay, renders choices answerable by mouse or
keyboard, and hosts the input line. A line's lifetime is the driver's
(`linger_ms`), because a brain cannot know how long a model will take.
