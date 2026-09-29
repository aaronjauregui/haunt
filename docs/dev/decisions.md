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

- **Hermes owns the model.** Haunt has no chat client and no one-shot
  loop. Which provider, which key and which bill are hermes's. Haunt's
  own backend layer speaks the OpenAI API only for the driver's verbs
  (vision, images) and ComfyUI for images. A brain that wants a model runs
  on hermes; there is no `chat` intent.
- **One writer per store, one interpreter per utterance.** No parser in
  front of the model deciding whether typed text is a command. No belief
  store beside hermes memory: a correction is the model editing its
  memory, and there is no confirmation dialogue for it.
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
- **Skills a ghost ships are read-only.** Hermes would let the model
  patch them; haunt's are haunt's. What the model learns goes to the
  profile's own skills.

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
- **The web is the driver's to hold, not a toolset handed to hermes.**
  The keys, the consent question, the ration, the refusal texts and the
  taint all live where the rest of them live. Hermes's own `web` toolset
  stays off, so there is one way in and one place that says no.
- **Unprompted searching is bounded by the turn's event kind.** The
  driver cannot see which skill the model loaded, so it gates on what it
  can see: the kinds a skill acts on, none of which carry text from the
  person's windows. It is approximate on purpose — the ration and quiet
  hours bound what the approximation costs — and the point it does buy is
  that what is on their screen never becomes a query.
- **Web text taints the turn it entered.** After a page or a result has
  been read, the verbs that reach past the person are asked about again
  in the balloon, and refused outright on a turn the person did not
  start. Showing them something is not one of those verbs; telling
  somebody else is.
- **Files are the driver's to read, not a toolset handed to hermes.**
  The grant, the unprompted policy, the ration, the journal and the taint
  all need the driver to see the read. Hermes's file toolset cannot be
  split from its writing tools, native hermes would have no confinement,
  and a read through a bind mount is one the driver never sees. So the
  person names folders in the config, the model sees only their names,
  and the driver reads through an `os.Root` per folder.
- **Taint records its source.** A file is a stranger's words as much as
  a page is, and marks the turn the same way. What differs is what may
  leave afterwards: after a file, a search query or a page's address is
  asked about too, since either could carry the file's text off the
  machine; after the web alone they stay free, because they send nothing
  that was not already out there.

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
| Model loop | hermes-agent, over its API server and an MCP stdio server |
