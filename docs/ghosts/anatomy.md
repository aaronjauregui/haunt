# Anatomy of a ghost

A ghost is a directory. It holds a manifest, `ghost.toml`, and whatever
the manifest points at: art, a persona, skills, a brain. Packaged, it is
one signed archive; installed, it is the same directory under the user's
library.

    mycompanion/
    ├── ghost.toml            # identity, capabilities, shell, persona or brain, tools, triggers
    ├── shell/                # the art: PNGs with transparency
    ├── skills/               # optional: routines, one SKILL.md per directory
    └── brain.wasm            # only for a scripted ghost

## Two kinds of ghost

**A persona ghost** declares no `[brain]` section. It runs on hermes-agent,
which speaks in the persona, remembers the person, takes on reminders,
draws when asked, uses whatever tool servers the manifest gives it, and
speaks first now and then. There is nothing to compile. What is left to
do is the art and the character. This is the default, and the kind most
ghosts should be.

**A scripted ghost** ships a brain: a program that receives events and
emits intents, compiled to WASM and run in a sandbox inside the driver,
or run as a native process when the user allows it. It has no model
unless it asks the driver to draw or look. This is for a ghost whose
logic is its own: reactions, commands, state machines, games.

Both kinds wear the same shells, obey the same rules, and are packaged
the same way.

## The scaffold

    haunt new mycompanion          # a persona ghost
    haunt new -brain mycompanion   # a scripted one, in Go
    cd mycompanion && haunt run .

The persona scaffold is a manifest, a persona, an example skill and a
README. The brain scaffold is a manifest, a Go module and a Makefile that
builds `brain.wasm`. Names are lowercase letters, digits and hyphens,
starting with a letter.

`haunt dev .` runs a directory with hot reload: edit, save, and the ghost
restarts in place with its state intact.

## What the driver does for every ghost

The division of labour is fixed. The **driver** owns everything the ghost
cannot be trusted with: the screen and its surfaces, input, the balloon
and its menus, persistence, consent, the AI backends and their keys,
budgets, quiet hours, the fullscreen rule, and the allowance for speaking
first. The **ghost**, whether a persona on hermes or a brain, owns the
judgment: what to say, what to remember, whether a moment is worth a
word.

So a ghost never draws to the screen, never opens a socket, never holds a
credential, and never decides when it may interrupt. It asks, and the
driver decides. Several things follow that a creator should know:

- A line's lifetime on screen belongs to the driver. A brain that wants a
  line to go away after a while says so on the line (`linger_ms`), and
  the driver hides it only if it is still the thing on screen.
- Expression follows the words. If a line is said without a face being
  set, the driver picks one from the words, using the pose names in
  [The shell](shell.md). A face the ghost sets itself always wins.
- Looking at the screen answers to the ghost, never to the balloon. A
  ghost that looks is told what it saw and decides what to say.
- Watching the screen continuously is a permission question the person
  raises from the ghost's menu. A ghost may offer it, and cannot start it.
- Acting through a tool always shows: a working face, a line about what
  was done, a journal entry.

## Pages in this guide

- [The manifest](manifest.md): every field of `ghost.toml`.
- [The shell](shell.md): the art and how it moves.
- [Persona and skills](persona-and-skills.md): the character and its
  routines.
- [Tools and triggers](tools-and-triggers.md): acting in the world.
- [Scripted brains](brains.md): writing logic.
- [Converting ukagaka shells](seriko.md).
- [Packaging](packaging.md): signing and shipping.
