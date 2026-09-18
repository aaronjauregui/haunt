# The manifest

`ghost.toml` is the whole description of a ghost. Every path in it is
relative to the ghost directory and must stay inside it. Unknown
capability names are refused at load, so a typo fails early and an older
driver refuses a package built for a newer one.

```toml
[ghost]
name = "wisp"                   # required; lowercase letters, digits, hyphens
version = "0.1.0"
author = ""
capabilities = ["message", "llm", "window"]

[shell]                         # see shell.md
sprite = "shell.png"            # one image, or:
# [[shell.layer]] ... [shell.pose.happy] ... [[shell.clip]] ...

[persona]                       # a ghost on hermes; see persona-and-skills.md
system = """
You are Wisp, a wry little desktop spirit. Reply in one or two short sentences.
"""

[hermes]
bundled_skills = ["humanizer"]  # hermes's own skills the ghost may use

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
| `name` | Required. The ghost's identity: its library directory, its socket, its hermes profile (`haunt-<name>`). |
| `version`, `author` | Shown at install. |
| `capabilities` | What the ghost would like to be allowed. Declaring one only means it may ask; the person grants. |

The capabilities:

| Name | Allows | Needed for |
|---|---|---|
| `message` | text from outside: typing, `haunt say`, scripts | any ghost that is talked to |
| `llm` | thinking through hermes | every persona ghost; the manifest is refused without it |
| `imagine` | drawing with the person's image backend | the `draw` tool, the `imagine` intent |
| `window` | which application and window is focused | session facts, goals watched on screen |
| `screen` | a picture of the focused window, to a vision model | the `look` tool, the `look` intent |
| `tools` | acting through the declared tool servers | any `[[tool]]`; the manifest is refused without it |
| `tell` | messaging other resident ghosts | the `tell` tool or intent |
| `native-brain` | running `brain.command` as a process | `brain.command` |

Ask only for what the ghost uses. Every capability is a question put to
the person, and a ghost that asks for `screen` and never looks is a ghost
that looked suspicious for nothing.

## `[shell]`

Either `sprite`, one PNG with transparency, or a layer stack with poses
and clips. Omit both and the ghost wears a built-in placeholder. The
format is [The shell](shell.md).

## `[persona]`

`system` is the character's text. It leads the hermes profile's `SOUL.md`;
haunt appends its standing rules after it. Only meaningful with `llm`.
See [Persona and skills](persona-and-skills.md).

## `[hermes]`

`bundled_skills` names skills from hermes's own library to install in the
ghost's profile. Profiles opt out of that library, since nearly all of it
needs a shell, files or the web; only `humanizer`, `weekly-review-planning`,
`meeting-action-items` and `songwriting-and-ai-music` may be named, and
any other name is refused at load.

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
installed into its hermes profile. See
[Persona and skills](persona-and-skills.md).
