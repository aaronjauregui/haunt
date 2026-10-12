# Haunt documentation

Three guides, for three readers. Each page stands on its own; read in
order the first time.

## User guide

For someone who wants a ghost on their desktop.

- [Installing](user/install.md): requirements, the systemd user unit, the
  compositors it runs on.
- [Running a ghost](user/running.md): demos, installing a package, who is
  resident, talking to it, its menu.
- [The model](user/model.md): choosing a model, a local one on
  llama-server, keys, and what trust follows from where it runs.
- [What it may do](user/permissions.md): capabilities, consent in the
  moment, watching your screen, revoking.
- [Memory, reminders and goals](user/memory.md): its memory files, what
  it is holding for you, and how to see why it said something.
- [Doing things](user/tools.md): tool servers, scheduled polls, presence.
- [More than one ghost](user/troupe.md): a household of ghosts and what
  they share.
- [Configuration](user/config.md): every section of `config.toml`.
- [Command reference](user/cli.md): `haunt` and `haunt-seriko`.

## Ghost creator guide

For someone making a ghost: art, a persona, skills, tools, or a brain.

- [Anatomy of a ghost](ghosts/anatomy.md): the directory, the two kinds of
  ghost, the scaffold.
- [The manifest](ghosts/manifest.md): every field of `ghost.toml`.
- [The shell](ghosts/shell.md): layers, variants, poses, clips, hit
  regions, and building one from a layered drawing.
- [Persona and skills](ghosts/persona-and-skills.md): the character and
  its examples, prefill and sampling, the standing rules haunt adds, the
  ghost's tools, and routines as skills.
- [A character on a local model](ghosts/local-models.md): building a
  persona on llama-server, lever by lever, with the example ghost ember;
  prefill, sampling, thinking, the cache, chat templates, and the knobs
  haunt does not expose.
- [Tools and triggers](ghosts/tools-and-triggers.md): MCP servers,
  scheduled polls and presence checks.
- [Scripted brains](ghosts/brains.md): events, intents, brainkit, WASM and
  process brains.
- [Converting ukagaka shells](ghosts/seriko.md): `haunt-seriko`.
- [Packaging](ghosts/packaging.md): signing, `pack`, `install`, `dev`.

## Developer guide

For someone changing haunt. Package and function comments are the record
of what exists and why; these pages are the map across packages.

- [Architecture](dev/architecture.md): the one rule, the packages, a
  ghost's lifetime, what a troupe shares, where state lives.
- [Decisions](dev/decisions.md): what is settled and why, and the
  non-goals.
- [The model brain](dev/brain.md): a turn, the prompt, deciding then
  speaking, the registry of decisions, the envelope, gating, taint,
  trust, memory, skills, the transcript, and the fake model server.
- [Hacking](dev/hacking.md): building, the headless-sway tests, the fake
  model server, demos, the evals on a real model, logging.

Reference for a package is `go doc ./internal/<name>`, or the module on
pkg.go.dev.
