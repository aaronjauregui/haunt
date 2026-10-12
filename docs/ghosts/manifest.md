# The manifest

`ghost.toml` is the whole description of a ghost. Every path in it is
relative to the ghost directory and must stay inside it. Unknown
capability names and unknown top-level sections are refused at load, so a
typo fails early and an older driver refuses a package built for a newer
one.

```toml
[ghost]
name = "wisp"                   # required; lowercase letters, digits, hyphens
version = "0.1.0"
author = ""
capabilities = ["message", "llm", "window"]

[shell]                         # see shell.md
scale = 1.0                     # how large the art is drawn
sprite = "shell.png"            # one image, or:
# [[shell.layer]] ... [shell.pose.happy] ... [[shell.clip]] ... [shell.masks] ...

[persona]                       # a ghost on the person's model; see persona-and-skills.md
system = """
You are Wisp, a wry little desktop spirit. Reply in one or two short sentences.
"""
prefill = "Hm. "                # optional; every reply starts from it

[[persona.example]]             # optional; how it sounds
user = "hello"
ghost = "Oh. It's you. Good."

[persona.sampling]              # optional; laid over the person's [model] sampling
temperature = 0.9
min_p = 0.05

[imagine]                       # the look of its pictures; needs the imagine capability
style = "soft watercolour, muted palette, paper texture"
negative = "photorealistic"     # optional; ComfyUI only

# [brain]                       # a scripted ghost instead; see brains.md
# wasm = "brain.wasm"           # or
# command = ["./brain"]         # needs the native-brain capability

[[tool]]                        # see tools-and-triggers.md
name = "notes"
command = ["mcp-server-notes", "--dir", "/home/me/notes"]
env = ["NOTES_TOKEN"]
allow = ["append", "search"]

[[trigger]]
cron = "0 9 * * *"
name = "morning"

[[trigger]]
cron = "*/15 * * * *"
name = "calendar"
tool = "calendar.upcoming"
args = { within = "2h" }
```

## `[ghost]`

| Field | Meaning |
|---|---|
| `name` | Required. The ghost's identity: its library directory, its state directory, its socket, its `[model.ghost.<name>]` table in the person's config. |
| `version`, `author` | Shown at install. |
| `capabilities` | What the ghost would like to be allowed. Declaring one only means it may ask; the person grants. |

The capabilities:

| Name | Allows | Needed for |
|---|---|---|
| `message` | text from outside: typing, `haunt say`, scripts | any ghost that is talked to |
| `llm` | thinking through the person's model | every persona ghost; the manifest is refused without it |
| `imagine` | drawing with the person's image backend | the `draw` tool, the `imagine` intent |
| `window` | which application and window is focused | session facts, goals watched on screen |
| `screen` | a picture of the focused window, to a vision model | the `look` tool, the `look` intent |
| `tools` | acting through the declared tool servers | any `[[tool]]`; the manifest is refused without it |
| `tell` | messaging other resident ghosts | the `tell` tool or intent |
| `web` | searching the web and reading a page | the `search` and `read_page` tools |
| `files` | reading, never changing, the folders the person shares in their config | the `list_files`, `read_file` and `search_files` tools |
| `native-brain` | running `brain.command` as a process | `brain.command` |

Ask only for what the ghost uses. Every capability is a question put to
the person, and a ghost that asks for `screen` and never looks is a ghost
that looked suspicious for nothing.

## `[shell]`

Either `sprite`, one PNG with transparency, or a layer stack with poses
and clips. Omit both and the ghost wears a built-in placeholder. The
format is [The shell](shell.md).

| Field | Meaning |
|---|---|
| `sprite` | One PNG, the whole shell. |
| `layer`, `pose`, `clip`, `hit` | The layer stack, its poses, animations and hit rectangles. |
| `default_pose` | The pose worn at boot. |
| `scale` | How large the art is drawn: above 0, at most 4, default 1. Every coordinate in the shell is in the art's own pixels. |
| `masks` | Mask names to PNGs marking where each part may go; `silhouette` is required. |
| `note` | Optional paper the ghost's desktop notes are written on ([The shell](shell.md#note-paper)). |

Every file the shell names (the sprite, each layer's image and variants,
the masks, the note paper) is a path relative to the ghost directory that stays inside
it; the manifest is refused otherwise.

## `[persona]`

The character a ghost with no `[brain]` plays on the person's model. Only
meaningful with `llm`. Which model, and where it runs, is the person's
choice in their config (`[model]`); a manifest never names one. Every
field here works on any provider. See
[Persona and skills](persona-and-skills.md).

| Field | Meaning |
|---|---|
| `system` | The character's text. It leads the system prompt; haunt's standing rules follow it. |
| `example` | Example exchanges, `[[persona.example]]` with `user` and `ghost`, shown to the model after the rules as how the character sounds, not as things that happened. Two or three are plenty. |
| `prefill` | Words every reply starts from, in the character's voice. They are the opening of what is said and shown with it. Optional. A person's lead replaces it for one reply ([Putting words in its mouth](../user/running.md#putting-words-in-its-mouth)). |
| `sampling` | `[persona.sampling]`: `temperature`, `top_p`, `min_p`, `repeat_penalty`, `dry_multiplier`, `xtc_probability`. Each one set is laid over the person's `[model]` sampling; a provider ignores a field it does not know. |

The model also sees, every turn, a two-line reminder after the turn's
input: the first sentence of `system`, and "Stay in character."

## `[imagine]`

The ghost's house style: the look its pictures have before the model
writes a word of prompt. It is the author's taste, and it is what makes a
ghost's pictures look like one ghost's rather than like whatever the model
felt like that turn.

| Field | Meaning |
|---|---|
| `style` | Added to every prompt the ghost draws with. At most 200 characters, on one line. |
| `negative` | What to keep out of the picture. ComfyUI only: the OpenAI image endpoint has nowhere to put it. Same limits. |

Write `style` the way the image model wants to be spoken to — tags for
Stable Diffusion, a phrase for Flux — and keep it about the look rather
than the subject. It rides ahead of whatever the picture is of, so a
paragraph of it would be the picture.

Only `style` is the ghost's to change. A person can tell their ghost to
draw in something else for good, which the driver keeps and `haunt journal
-style` shows; the package's `style` is the seed it started from, and
`haunt journal -style -reset` goes back to it. `negative` stays the
author's either way.

A `[imagine]` section without the `imagine` capability is a warning at
start, not a refusal: the ghost simply never draws.

## `[brain]`

Omit the section entirely for a persona ghost. Otherwise exactly one of:

| Field | Meaning |
|---|---|
| `wasm` | a WASI module, run sandboxed inside the driver |
| `command` | argv for a native process speaking the same protocol; `argv[0]` is relative to the ghost directory; needs `native-brain` |

See [Scripted brains](brains.md).

## `[[tool]]`

One MCP tool server the ghost would like to act through. The driver runs
it. Needs the `tools` capability, or the manifest is refused.

| Field | Meaning |
|---|---|
| `name` | The server's name and the first half of every tool's name (`notes` makes `notes.append`). No dots or spaces. It is what the person sees in the consent question, so say what the server is for. |
| `command` | The server process. `argv[0]` is looked up on `PATH` as well as relative to the ghost directory: a tool server is ordinarily something the user installed. |
| `env` | Environment variables to pass through from the driver's environment, by name. A manifest never carries a value. |
| `allow` | Optional. The tools this ghost may use at all, the manifest's own bound under the person's. Empty means everything the server offers. |

## `[[trigger]]`

A scheduled event.

| Field | Meaning |
|---|---|
| `cron` | Standard five-field cron, minute granularity. Required. |
| `name` | Identifies the trigger in the event. Required. |
| `tool` | Optional. `server.tool` to call on schedule; the server must be one of this manifest's `[[tool]]`s. |
| `args` | The argument object handed to the tool unchanged on every poll, inline: `args = { within = "2h" }`. Only with `tool`. |
| `quiet_when` | Makes the poll a presence check: when the answer contains this text, the ghost keeps quiet. Only with `tool`. |

Without `tool` the trigger is an **occasion**: a name and a time, and the
ghost finds the words. A persona ghost with an occasion named `morning`
runs its morning check-in on it, and one named `weekly` its weekly
review. With `tool` it is a **poll**. See
[Tools and triggers](tools-and-triggers.md).

## `skills/`

Not a manifest field. A `skills/` directory next to the manifest, one
subdirectory per skill holding a `SKILL.md`, is packed with the ghost and
listed to the model beside haunt's own skills; one with the name of one of
haunt's replaces it. See
[Persona and skills](persona-and-skills.md).
