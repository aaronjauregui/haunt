# Decisions

What is settled, and why, so it stops being reconsidered. Each is a
constraint on changes, not a history.

## Scope

- **No compatibility with SHIORI, SSTP, SakuraScript, or the
  `descript.txt` package format.** The community's terms (ghost, shell,
  balloon) are kept and defined by haunt. `haunt-seriko` converts shell
  art only, and it is a separate binary so that reading somebody else's
  archive format is never code that runs on the desktop all day.
- **No X11, Windows or macOS.** No GNOME: Mutter does not implement
  wlr-layer-shell and no fallback will be built.
- **No cgo.** A static binary. This is why keyboard translation is a
  pure-Go US layout (the seam for the real thing is `textinput.Keymap`),
  why OpenRaster is the drawing input and PSD is not, and why the Wayland
  bindings are a patched vendored `go-wayland`.
- **Local-first.** Every backend works against a local endpoint; cloud
  providers are optional and their keys are named, never stored.

## The brain

- **Haunt owns the loop.** A persona ghost's model is reached directly
  over the OpenAI-compatible chat completions API (`internal/model`), with
  no agent process between haunt and the model server. The transcript,
  its compaction, memory, skills and tool dispatch are haunt's. An agent
  framework in the middle owns the message list, and every lever that
  makes a character hold on a model (prefill, sampling, where a reminder
  sits) sits behind it; it cannot tie a tool call to its turn or show the
  driver a memory write, and the seam needs string heuristics for a face
  and for silence. One protocol only: it covers llama-server, Ollama,
  vLLM, z.ai, OpenAI and OpenRouter. The person's config picks the model;
  a manifest never names one. There is no `chat` intent: a scripted brain
  that wants a model is a persona ghost.
- **Decide, then speak.** A turn is a tool-use loop whose terminal
  decisions are `speak(pose)` and `stay_silent()`, then, after `speak`, a
  second call for the words with the persona's prefill and thinking off.
  Silence and the face are decisions the model makes in a schema, not
  tokens the driver fishes out of prose, and the words are written
  without tools in front of them, where prefill and sampling do the most
  for the character. `fold` puts the words in the first call where the
  round trip costs more than it buys: cloud by default. The decisions are
  a registry, a name, a schema and an apply function each, so a new
  behaviour is a new entry and never a new call shape.
- **A lead is one reply's, and the model is not told of it.** The
  person's steer for the words (`haunt say -lead`, `/lead` in the line)
  lives on the turn and nowhere else: a prefill on every reply makes
  Gemma 4 copy earlier replies, and a steer that outlived its reply would
  be a persona edit nobody meant to make. It is the speak call's prefill
  when the turn has one, and otherwise a line in the turn's context, the
  weaker form. The standing rules never mention it and its transcript row
  is never replayed, summarised or searched; `haunt history -why` marks
  the reply led, so the opening is not taken for the ghost's choice.
- **Memory is files.** Two always-present files (`self.md`, `you.md`) and
  topic files whose lines are facts, in Markdown the person can read and
  edit while the ghost runs; the FTS5 index in `state.db` is derived and
  rebuilt from them. Facts are lines in topics, never a file per fact, so
  the index in the prompt stays small. Every line carries its provenance
  (person, web, file, self; the turn), written by the driver from what
  the turn read, not by the model. There is no belief store beside it: a
  correction is the model taking a line back, and there is no
  confirmation dialogue. Each ghost has its own; two ghosts may disagree
  about the person.
- **A line the person stated is theirs.** A line that stops being true
  is retired to its topic's `## Past`, kept and unindexed, by `remember`
  with `replaces`; no side call is made per write. A line whose source is
  the person is retired or forgotten only on the person's own turn with
  nothing read, or on the ghost's reflection on a finished day, in
  either mode and whatever the day read: an event or a page must not
  talk the ghost out of what the person said, but the whole day they
  said it in may. The new line keeps the turn's own provenance.
- **Trust follows the endpoint.** A model on loopback or a private range
  is local, any other host cloud, judged from `base_url`'s text and
  overridable with `trust`; a ghost with no model brain is cloud. Reads
  are loose and writes confined. On a local model reading shared folders
  and the web needs no grant and no question, on the person's turns and
  the ghost's own, within quiet hours and the ration, since nothing read
  leaves the machine through the model; the package must still declare
  the capability, and a "never ask again" or a `haunt perms revoke`
  still holds: trust decides whether to ask, the grants file what is
  allowed. On a cloud model reads are consented and follow the
  `unprompted` policies. What the
  ghost keeps (a line, its notes, a skill) is labelled with its source on
  both, and on a cloud model refused when its words repeat this turn's
  web or file reads: the echo check gates where the model cannot be told
  apart from a page, and only flags where nothing has left the machine.
  The ghost's own bookkeeping (intentions, the drawing style) is free
  after a read on local and asked about on cloud; what reaches past the
  person (telling, tool servers) stays consented, marked and asked about
  after a read on both. One table holds all of it, and the standing rule
  the model reads is rendered from that table. No shell and no file
  writes on any provider.
- **One interpreter per utterance.** No parser in front of the model
  deciding whether typed text is a command.
- **The trust line is at the model's output, not the model.** A
  downloaded brain is untrusted; the person's chosen model is on the
  person's side of the line. What is gated is effects.
- **Envelopes carry fields, not instructions.** One line per event, no
  shared preamble, and the person's words never carry one. Every rule
  here comes from a documented failure elsewhere: a shared heartbeat
  instruction answered in place of the job it was attached to, and models
  reasoning in the third person when the prompt does.
- **The driver decides what may wake the model, and charges before
  sending.** The model never sees an event it is not allowed to act on,
  so its only decision is whether this one is worth a word. Silence is
  refunded.
- **Mood is the journal's, not the face's.** A ghost sets one line about
  how it feels; it rides in the turn context, `haunt journal -mood`
  shows it, and it counts only on the day it was set, so it clears
  overnight with no timer. It never changes the resting pose and is
  never shown in the balloon: a face that sulked on its own would be a
  second expression system beside the one the model already chooses
  with `speak`, and one the person could not see the reason for.
- **The day is the unit of memory over time, and a day is written up
  once.** There is no session boundary: a ghost starting only marks time.
  At the first idle after the day changes, the ghost reflects on the day
  and writes a dated diary entry; compaction then folds whole days behind
  their entries, oldest first, and never summarises a day again, since a
  summary of a summary loses what the first one kept. A day that cannot
  be given an entry is summarised alone, on its own day, so no summary
  spans two days. The rolling summary stays only for today when today is
  too long for the context. A folded day is not replayed: the diary in
  the prompt carries the last days, and the file the rest. What a day
  read, its reflection reads again, so a page cannot become the ghost's
  own conclusion overnight. The person's edits to the diary are kept
  byte for byte; the ghost only adds a new day. Reflection writes
  through the same decisions and gates as any turn, and the diary is a
  file the person can read and edit beside the others.
- **Reflection is built both ways until an eval chooses.** In character,
  a turn of the ghost's own with its memory decisions, which keeps the
  one rule whole; as a side call, one plain request whose JSON the driver
  applies as the ghost's decisions on a turn of its own, cheaper on a
  slow model. Both may retire a line the person stated. `[memory]
  reflection` picks; character is the default until the eval says
  otherwise.
- **The person comes before reflection.** It runs only while they are
  away and nothing waits, is cancelled the moment their words queue, and
  is never charged to initiative, since it reaches no screen.
- **Keyed context is pushed, sticky and cooled, and keyed by the
  person's words only.** `recall` is pull-only, and a small model does
  not reach for it when the person names something it was told a week
  ago, so the lines their words match ride in the turn context unasked.
  A line stays a few turns, for the reply to the reply, then cannot come
  back for a while, so a subject the person keeps returning to does not
  pin one fact into every turn. An event's fields and a window's title
  never key: those are other people's words, and a line they brought in
  would be the desktop steering what the ghost has in mind.
- **Skills a ghost ships are read-only.** Haunt's and the package's
  skills are never changed by the model. What it learns goes to its own
  learned skills, which are checked against the tools it has and
  archived when they go unused.

## Effects

- **Consent in the moment beats a checklist.** What is withheld at
  install is asked for the first time it matters, in the balloon, about
  the thing that is happening, and only after something the person did.
  "Never ask again" exists because a refusal with nowhere to be written
  down means "ask me again in a minute".
- **A brain may propose and must not perform** where the action is a
  permission question. Window awareness is offered from the menu and
  started by the driver only after the person acted.
- **`window` and `screen` are separate capabilities.** Knowing which
  window is focused is a different order of intrusion from being shown a
  picture of it.
- **The driver never narrates the screen.** Looking answers to the brain.
- **Acting is visibly different from talking, always.** A working face, a
  line about what was done, a journal entry. None configurable.
- **Initiative is rationed on causality, not on what a brain claims.**
  Two clocks: a response window after the person acts, and a window after
  a paid-for unprompted action during which the rest of the chain rides
  free and cannot be extended. Three allowances by who decided: curiosity
  for the ghost's whims, obligation for what the person asked for, and a
  sub-linear ceiling for the room.
- **A poll never speaks and never remembers.** Cron silently skips missed
  runs, so a poll's answer becomes an intention or nothing. Presence
  quiet expires on its own.
- **Intentions are discharged deliberately.** Coming due does not clear
  one. A reminder raised into a quiet hour is still held afterwards.
- **A line's lifetime belongs to the driver.** A brain's timer to hide
  its own line would hide the reply that arrived meanwhile.
- **A conversation is a mode of the balloon, not a panel.** Typing at a
  ghost widens the balloon and keeps the line open between messages; it
  stays an image, with no selection or scrollbar of its own. It is the
  one balloon with an input region, so the wheel over it scrolls it as
  the wheel over the ghost does: a person reading back looks at the
  words, not at the ghost. The cost is that a click on it stays on it
  instead of reaching the window beneath, which while a conversation
  holds the keyboard is no loss. Scrolled back, it holds its place as
  the ghost talks; only the person's own line brings it to the bottom. Nothing in it expires while it is open: a reply that
  went away while the person was still reading it would be the driver
  taking the conversation back mid-sentence. A click on the ghost during
  one is a poke, not a closing; clicking away is the keyboard leaving.
- **A balloon goes where it can be read, beside the ghost.** To the
  right by default and to the left when the right has no room; level
  with the ghost's top by default and with its bottom when the top would
  run it off the screen. The default spot is the bottom right, so both
  flips are the ordinary case. The conversation is the exception: it
  always stands on the ghost's feet and grows upward, capped at the room
  above them, because a panel that grows as lines arrive and the box
  takes rows has to grow away from the line being typed, and down is
  usually off the screen. The cost is a drop of up to the ghost's height when a
  conversation opens on a short balloon hanging from the ghost's top;
  it is lost in the panel widening at the same moment, and it keeps the
  ordinary line where a reader looks first.
- **Ending a conversation is the one surface lifetime a ghost decides,
  and it is switchable.** Escape, the keyboard leaving and a quiet spell
  are the driver's; `end_conversation` lets the ghost say goodbye and
  close it after its words are shown. It is the single exception to the
  driver owning every surface's lifetime, so `[conversation]
  ghost_may_end = false` takes it away, and the decision is then not
  offered at all rather than refused.
- **A desktop note is an intention with a surface.** It is held,
  listed, capped and resolved as an intention is, so nothing new is
  needed to see it, list it or take it down; the surface is the
  driver's, placed beside the ghost, dragged and taken down with a click
  like anything else on the desk. Notes are capped on their own and
  never pushed out by reminders. A note clicked away is silent: the turn
  context stops listing it, and no event wakes the ghost to remark on
  it.
- **Quiet hours, the fullscreen rule and reduced motion are the room's.**
  No ghost can override them, and a ghost cannot silence itself either,
  because being able to would make quiet negotiable.
- **Image generation is refused while something is fullscreen.** A
  scheduling rule: a local diffusion run takes the whole GPU for seconds.
- **A gallery, not a prompt hash.** Pictures a ghost has painted are kept
  with names it can repeat, and showing one again is a verb of its own.
  Reuse a person can ask for is worth more than reuse that happens by
  accident behind the same prompt, and it makes "show me the whale again"
  cost nothing.
- **The house style is the driver's, and changes only on the person's
  turn.** A look the model rewrote whenever it felt like it would drift a
  package's art away from itself unremarked; a look that web text could
  rewrite would be an injection with a long memory. The package seeds it,
  the person can let the ghost change it, and the negative stays the
  author's.
- **The web is the driver's to hold.** The keys, the consent question,
  the ration, the refusal texts and the taint all live where the rest of
  them live, so there is one way in and one place that says no.
- **Unprompted searching on a cloud model is bounded by the turn's event
  kind.** "Only where a skill says to" is not something the driver can
  check, so it gates on what it can see: the kinds a skill acts on, none of which carry text from the
  person's windows. It is approximate on purpose — the ration and quiet
  hours bound what the approximation costs — and the point it does buy is
  that what is on their screen never becomes a query.
- **Web text taints the turn it entered.** After a page or a result has
  been read, the verbs that reach past the person are asked about again
  in the balloon, and refused outright on a turn the person did not
  start. Showing them something is not one of those verbs; telling
  somebody else is.
- **Files are the driver's to read.** The grant, the unprompted policy,
  the ration, the journal and the taint all need the driver to see the
  read, and reading must never come with writing. So the person names
  folders in the config, the model sees only their names, and the driver
  reads through an `os.Root` per folder.
- **Taint records its source.** A file is a stranger's words as much as
  a page is, and marks the turn the same way. What differs is what may
  leave afterwards: on a cloud model, after a file, a search query or a
  page's address is asked about too, since either could carry the file's
  text off the machine; after the web alone they stay free, because they
  send nothing that was not already out there.

## The shell

- **Layers, variants, poses.** The model the ukagaka ecosystem converged
  on, because layering stops expressions and dress-up multiplying into a
  redrawn body per combination. Affine transforms with keyframe
  interpolation are an optional extra on a keyframe, not a second system.
- **No mesh or vector runtime** (Live2D, Spine, Rive, Lottie). Both need
  a GPU path haunt does not have without cgo, and a proprietary editor,
  which is fatal for shells meant to be hand-makeable.
- **No numeric clip rate.** A clip is `sometimes` or `rarely`. The coarse
  vocabulary is the granularity a person authoring by hand can reason
  about, and no shell in the tested corpus used a numeric interval.
- **Art is cropped to what it covers.** Damage tracking makes a
  full-canvas blink repaint the whole ghost.
- **Masks are the one uncropped shell art.** They are never drawn, so
  cropping saves no repaint, and their shared size is what fixes the
  canvas.
- **Five pose names mean something to the driver.** A lexicon picks a
  face from the words when nobody set one; an explicit face always wins.
- **Art and coordinates are in art pixels, scaled once at load.** The
  shell's `scale`, times any factor the person sets, is applied by the
  loader, which resamples the art and scales every coordinate; nothing
  downstream knows the art was drawn at another size. Art can be
  made at the resolution that suits making it, and the full-resolution
  art stays in the package.
- **Masks are PNGs, and only the silhouette is required.** A picture can
  be painted in the same editor as the art, and eyes, mouth and slots are
  optional, so a ghost with any face, or none, can use masks.
- **The base is derived, never stored.** It is the unmasked layers at
  their defaults, composited at load, so it cannot drift from the art it
  is made of. The body is drawn with no face, so the base is the ghost
  with nothing on it.
- **Masked art stays inside its mask.** Checked at load, so parts drawn
  separately cannot wander: a mouth whose variants sit in different
  places jumps when a clip swaps them. The check is exported, so a tool
  that writes art runs the same one.

## Packaging and state

- **A ghost is one signed Ed25519 archive with a TOML manifest.** The
  author's key is pinned per name on first install.
- **Downloaded content never gets native execution by default.** WASM is
  the default brain; a native process needs a granted capability.
- **A running ghost is the sole writer of its store.** Every change goes
  through the control socket. Store keys under `_haunt/` are the driver's.
- **Tool servers run in the driver, over stdio only.** That is the
  transport whose credentials are environment variables the driver puts
  there; a brain in the sandbox cannot spawn a process, and one that could
  would hold the keys. Resources, prompts and sampling are ignored.
- **Haunt ships no integrations.** A service is an MCP server and a
  trigger line.
- **Each ghost has its own Wayland connection, bus, store and brain.**
  Sharing a connection would put every ghost behind one lock and make one
  crash the end of all of them.
- **Tests run against a real headless sway**, never a mock compositor.

## Technology

| Concern | Choice |
|---|---|
| Language | Go, static binary, no cgo |
| Wayland | wlr-layer-shell via a patched vendored `go-wayland` |
| WASM runtime | wazero |
| Brain protocol | stdio JSONL: events in, intents out |
| Persistence | SQLite, pure-Go driver |
| Manifest | TOML |
| Screen capture | `grim` |
| Signing | Ed25519 |
| Model loop | haunt's own, over OpenAI-compatible chat completions |
| Memory | Markdown files, an FTS5 index derived from them |
| Skills | the agentskills `SKILL.md` format |
