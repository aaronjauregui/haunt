# Command reference

## haunt

```
haunt run [-x N] [-y N] [-output NAME] [-record FILE] [-state DIR] [-idle-ms N] [-goal-check-delay D] [ghost-dir|name ...]
haunt dev [-x N] [-y N] [-state DIR] <ghost-dir>
haunt say [-ghost NAME] <text>
haunt new [-brain] <name>
haunt shell init [-C dir] [-art subdir] [-scale F] [-write] <art.ora>
haunt shell check [-C dir] [-factor F] [-render DIR]
haunt keygen
haunt pack [-o FILE] <ghost-dir>
haunt install [-yes] <file.haunt>
haunt ls | rm <name>
haunt switch <name> | add <name> | drop <name>
haunt perms [name] | perms grant|revoke <capability> [name]
haunt shush [duration] | unshush
haunt journal [-intentions] [-notes] [-mood] [-echoes] [-skills] [-style [-reset]] [name | -state DIR]
haunt history [-n N] [-why | -json] [name | -state DIR]
haunt memory ls|show|rm|reindex [-ghost NAME | -state DIR] [topic [line-number]]
```

| Command | What it does |
|---|---|
| `run` | Run the resident ghosts, or the ones named. A name is an installed ghost; a path is a directory. Each remembers where it was left, per screen; `-x`, `-y` and `-output` place a single ghost. `-record` writes the event session as JSONL. `-idle-ms` and `-goal-check-delay` are demo and debugging affordances. |
| `dev` | Run one ghost directory with hot reload: when its files change the ghost restarts in place, with its state intact. |
| `say` | Send text to a running ghost, as if typed. Without `-ghost`, the one you last interacted with. |
| `new` | Scaffold a ghost directory: a persona ghost by default, or with `-brain` a Go brain that builds to WASM. |
| `shell init` | Build a shell (cropped PNGs, masks from a `masks` group, and the `[shell]` section) from a layered OpenRaster drawing. `-scale` sets how large the art is drawn. `-write` replaces the section in `ghost.toml`, keeping the poses, clips and hits written there by hand ([The shell](../ghosts/shell.md#importing-again)). |
| `shell check` | Load a ghost directory's shell as the driver would, and report its scale, canvas, layers, poses, clips and masks; exits 1 if it does not load. `-factor` stands in for the `[appearance]` factor in your config; `-render` writes each pose, and with masks the base and the masks, as PNGs. |
| `keygen` | Create the author's Ed25519 signing key. |
| `pack` | Build a signed `.haunt` package from a ghost directory. |
| `install` | Verify, install, and ask about the requested capabilities. `-yes` grants them all. |
| `ls`, `rm` | List installed ghosts (`*` marks the resident ones); uninstall one, memory and grants included. |
| `switch`, `add`, `drop` | This ghost alone; this one as well; this one out. The set is read when `run` starts. |
| `perms` | What a ghost asked for and what it has. `grant` and `revoke` take a capability, or `tool:<server>.<tool>`, and apply to a running ghost within seconds. |
| `shush`, `unshush` | Quiet now, for an hour or the given duration; and back. |
| `journal` | What the ghost did. `-intentions` is what it is still meaning to do, the notes it left on the desktop included; `-notes` is those notes alone; `-mood` is how it said it is feeling today, and when; `-echoes` lists what it saved to memory in the words of a page or a file it read; `-skills` lists its routines by source, including the ones it wrote itself; `-style` is the style it draws its pictures in, and `-style -reset` puts it back to the one its package ships. |
| `history` | The conversation, read from the ghost's transcript (`transcript.db` in its state directory), running or not; a summary of older turns shows as `summary:`. `-why` adds what woke it, what it saw and what it did before each reply, with a `!` under a memory write that repeats a page or a file; `-json` gives the turns as JSON with every decision whole ([memory](memory.md#why-did-it-say-that)). |
| `memory` | The ghost's topic files, under `memory/topics/` in its state directory. `ls` lists the topics with their line counts; `show <topic>` prints a topic's lines, numbered, each with where it came from (person, web, file or self), the turn and the date; `rm <topic>` deletes a topic and `rm <topic> <n>` its nth line. These work on the files whether the ghost runs or not. `reindex` rebuilds the search index in `state.db`: a running ghost is asked over its control socket, and its answer is printed; with no ghost running, `state.db` is opened and rebuilt here. A running ghost also picks up edits to its memory files by itself, within a second. Flags go before the topic; without `-ghost` or `-state`, the resident ghost. |

## haunt-seriko

```
haunt-seriko [-o DIR] [-surface N] [-only IDS] [-name NAME] [-pose happy=3,sad=5] [-n] <shell-dir | ghost.nar>
```

Converts ukagaka shell art into a haunt shell. `-n` says what it would
write, including how many animation clips the shell has, without writing
it. See [Converting ukagaka shells](../ghosts/seriko.md).
