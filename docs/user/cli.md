# Command reference

## haunt

```
haunt run [-x N] [-y N] [-output NAME] [-record FILE] [-state DIR] [-idle-ms N] [-goal-check-delay D] [ghost-dir|name ...]
haunt dev [-x N] [-y N] [-state DIR] <ghost-dir>
haunt say [-ghost NAME] <text>
haunt new [-brain] <name>
haunt shell init [-C dir] [-art subdir] [-write] <art.ora>
haunt keygen
haunt pack [-o FILE] <ghost-dir>
haunt install [-yes] <file.haunt>
haunt ls | rm <name>
haunt switch <name> | add <name> | drop <name>
haunt perms [name] | perms grant|revoke <capability> [name]
haunt shush [duration] | unshush
haunt journal [-intentions] [-skills] [name | -state DIR]
haunt history [-n N] [-why | -json] [name | -state DIR]
haunt mcp-serve -ghost NAME
```

| Command | What it does |
|---|---|
| `run` | Run the resident ghosts, or the ones named. A name is an installed ghost; a path is a directory. Each remembers where it was left, per screen; `-x`, `-y` and `-output` place a single ghost. `-record` writes the event session as JSONL. `-idle-ms` and `-goal-check-delay` are demo and debugging affordances. |
| `dev` | Run one ghost directory with hot reload: when its files change the ghost restarts in place, with its state intact. |
| `say` | Send text to a running ghost, as if typed. Without `-ghost`, the one you last interacted with. |
| `new` | Scaffold a ghost directory: a persona ghost by default, or with `-brain` a Go brain that builds to WASM. |
| `shell init` | Build a shell (cropped PNGs and the `[shell]` section) from a layered OpenRaster drawing. `-write` replaces the section in `ghost.toml`. |
| `keygen` | Create the author's Ed25519 signing key. |
| `pack` | Build a signed `.haunt` package from a ghost directory. |
| `install` | Verify, install, and ask about the requested capabilities. `-yes` grants them all. |
| `ls`, `rm` | List installed ghosts (`*` marks the resident ones); uninstall one, memory and grants included. |
| `switch`, `add`, `drop` | This ghost alone; this one as well; this one no longer. The set is read when `run` starts. |
| `perms` | What a ghost asked for and what it has. `grant` and `revoke` take a capability, or `tool:<server>.<tool>`, and apply to a running ghost within seconds. |
| `shush`, `unshush` | Quiet now, for an hour or the given duration; and back. |
| `journal` | What the ghost did. `-intentions` is what it is still meaning to do; `-skills` lists its routines by source, including the ones it wrote itself. |
| `history` | The conversation, read from the ghost's hermes gateway, which must be running. `-why` adds what woke it, what it saw and what it did before each reply; `-json` gives the turns as JSON with every tool call whole ([memory](memory.md#why-did-it-say-that)). |
| `mcp-serve` | The MCP stdio server hermes spawns to reach a running ghost. Not meant to be run by hand. |

## haunt-seriko

```
haunt-seriko [-o DIR] [-surface N] [-only IDS] [-name NAME] [-pose happy=3,sad=5] [-n] <shell-dir | ghost.nar>
```

Converts ukagaka shell art into a haunt shell. `-n` says what it would
write, including how many animation clips the shell has, without writing
it. See [Converting ukagaka shells](../ghosts/seriko.md).
