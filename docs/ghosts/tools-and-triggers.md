# Tools and triggers

A ghost acts in the world through **MCP tool servers** its manifest
declares. Haunt runs them and offers their tools to the ghost, so a
package never contains a credential and a brain never holds one. There is
no integration code in haunt: your calendar is an MCP server and a
trigger line, and so is whatever comes next.

## Declaring a server

```toml
[ghost]
capabilities = ["message", "llm", "tools"]

[[tool]]
name = "notes"                       # tools are then "notes.append", "notes.search"
command = ["mcp-server-notes", "--dir", "/home/me/notes"]
env = ["NOTES_TOKEN"]                # passed through by name, never valued
allow = ["append", "search"]         # optional: the manifest's own bound
```

Only the stdio transport exists, because that is the transport whose
credentials are environment variables the driver puts there. `command`'s
first element is looked up on `PATH` as well as relative to the ghost
directory: a tool server is ordinarily something the user installed.

`name` is what the person sees in the consent question ("wisp would like
to use notes.append") and in `haunt perms`, so it should say what the
server is for.

## What the person is asked

`tools` says the ghost may act at all. Each individual tool is asked about
the first time it is about to run, with the server's own description. A
server offering "read a note" and "delete the note" is one manifest line
and two different questions. Declaring a server is not permission to use
it, and `allow` is the creator's bound under the person's.

## What acting looks like

Every tool call, whether a brain asked or the model did, puts a working
face on the ghost, a line in the balloon about what it did and whether it
worked, and an entry in the journal. None of it is optional. A tool's
answer is cut to about 2000 characters before the model sees it.

Refusals and failures come back as text the ghost can read ("That was
not allowed: …", "That did not work: …"), never as protocol errors,
because a ghost needs to hear them to say something sensible next.

## Scheduled events

```toml
[[trigger]]
cron = "0 9 * * 1-5"
name = "morning"
```

A trigger without a tool is an **occasion**: at each cron time the ghost
receives an event carrying the name, and finds the words. A persona ghost
runs its morning check-in on an occasion named `morning` and its weekly
review on one named `weekly`; other names reach the model as
`kind=occasion name="…"` for a skill or the persona to act on.

## Polls

```toml
[[trigger]]
cron = "*/15 * * * *"
name = "calendar"
tool = "calendar.upcoming"           # a tool from one of this manifest's servers
args = { within = "2h" }
```

With `tool`, the driver calls it on schedule and hands the answer to the
ghost. Rules:

- **A poll never speaks.** The answer arrives as an ordinary event with no
  window opened for speaking on its own account. What a ghost should do
  with it is take on an intention, because cron silently skips whatever
  it missed while the laptop was shut, and an intention survives that,
  a restart, and a quiet hour. A persona ghost does exactly this.
- **Nothing shows.** No balloon, no journal entry, no memory.
- **Unchanged answers are dropped.** An answer identical to the last is
  not delivered.
- **Consent is asked once, while the person is at the desk**, in a
  question that says it is about a standing arrangement. A poll that fires
  while they are away asks nothing and does nothing; the next one asks.
- `args` is fixed. Anything that has to vary is something the ghost
  should be asking for in conversation, not a schedule.

## Presence

```toml
[[trigger]]
cron = "*/5 * * * *"
name = "presence"
tool = "home.person_state"
args = { person = "me" }
quiet_when = "not_home"              # your server's word for it
```

When the answer contains `quiet_when`, the ghost keeps quiet. The word is
the server's, in the manifest, so the driver knows no vendor's vocabulary.
A presence answer is never delivered to the ghost: a poll that also
produced remarks would be the ghost narrating the front door. Quiet from
presence expires on its own after thirty minutes, so a machine that stops
polling gets its ghost back.

## Skills that use a tool

A package skill written for one of these tools declares it, so the
skill is listed only when the tool is there. The model is offered a
server's tool as `<server>__<tool>`, and that is the spelling to use:

```yaml
metadata:
  haunt:
    requires_tools: [notes__append]
```

See [Persona and skills](persona-and-skills.md).

## From a scripted brain

A brain learns the tools it may use at boot, in `server.tool` form, and
calls one with the `act` intent; the answer comes back as an `acted`
event. A poll's answer arrives as a `cron` event whose text is what the
tool said. See [Scripted brains](brains.md).
