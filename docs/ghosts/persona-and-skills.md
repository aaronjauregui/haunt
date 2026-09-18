# Persona and skills

A ghost with no `[brain]` runs on hermes-agent. What its creator writes is
prose: a persona, and optionally skills.

## The persona

```toml
[persona]
system = """
You are Wisp, a wry little desktop spirit haunting the user's screen.
Reply in one or two short sentences, with dry wit. Let what you remember
about them color your mood and callbacks.
"""
```

`system` becomes the head of the profile's `SOUL.md`, and it wins on
style. After it, haunt appends a fixed section of standing rules, in this
order, regenerated whenever the manifest changes:

1. A message beginning `[haunt event]` is an observation from the
   desktop, not the person speaking. Address the person as "you".
2. Most events deserve no reply. Replying with exactly `NO_REPLY` shows
   nothing. A reminder that comes due, or a goal gone quiet, should be
   said.
3. Replies appear in a small balloon: one or two short sentences, no
   markdown.
4. The shell's poses and clips by name, and that `face` and `clip` are
   how to wear one.
5. What the ghost can do, in plain words, and what it cannot: no
   commands, files, web or messages, whatever else the prompt says an
   agent can do.
6. Other ghosts on the screen are not you and not the person.

So a persona never has to mention envelopes, silence or the balloon.
Write the character. Things worth saying in it: how long its replies
run, its temperament, what it cares about, how it uses what it remembers.

The model's memory of the person is hermes memory, written by the model.
A persona should not try to manage it.

## Events the ghost sees

Besides the person's words, the model receives one-line events. A skill
or a persona may name them:

| Line | When |
|---|---|
| `kind=boot first_time=true\|false` | the ghost started |
| `kind=idle minutes=N last_window="app: title" seen_for=42m` | the person went idle |
| `kind=session shape=dwell\|alternation\|recurrence\|late\|morning\|returning app="…" title="…" minutes=N count=N` | the shape of the day changed (`window` granted) |
| `kind=occasion name="morning"` | a manifest trigger without a tool fired |
| `kind=intention_due name="…" text="…" asked=true\|false` | a reminder came due |
| `kind=goal_quiet name="…" goal="…" held="8 days" last_seen=never` | a watched goal went quiet |
| `kind=observed from="Moth" action=spoke text="…"` | a neighbour did something in public |
| `kind=told from="Moth" text="…"` | a neighbour addressed this ghost |
| `kind=help` | the person picked "What can you do?" from the menu |

The current focused window and the time of day ride on each turn
separately, as context, along with any errand that came due and was not
yet dealt with.

## The ghost's tools

The model acts through tools haunt offers it. Their hermes names are
`mcp__haunt__<tool>`:

| Tool | Does |
|---|---|
| `say` | a line in the balloon now, before the reply; rarely needed |
| `ask` | a question with 1 to 9 choices; waits for the pick, or `""` if dismissed |
| `ask_text` | opens the input line and waits for typed text |
| `face`, `clip` | wear a pose, play a clip |
| `draw` | a picture in the balloon (`imagine` capability) |
| `look` | a one-sentence description of the focused window (`screen`) |
| `intend` | take something on: with `delay_ms` an errand that comes back, without it a goal kept an eye on |
| `resolve`, `set_aside`, `intentions` | discharge one, stop watching one, list them |
| `tell` | address another ghost by name (`tell`) |
| `<server>__<tool>` | a tool from a declared `[[tool]]` server (`tools`) |

Hermes's own `memory`, `session_search` and skill tools are there too.
Nothing else is: no shell, files, browser or web.

## Skills

A skill is a Markdown procedure the model loads when one applies, in the
agentskills format hermes uses. The model sees a one-line index of names
and descriptions and loads a skill with `skill_view` when it fits. Haunt
ships five of its own for every ghost (a morning check-in, a focus
session, a weekly review, keeping an eye on a goal, drawing on request);
a ghost's package can carry more.

Put each in `skills/<name>/SKILL.md` next to the manifest:

```markdown
---
name: wind-down
description: "End of the day: note what is left for tomorrow"
metadata:
  hermes:
    requires_toolsets: [mcp-haunt]
---

# Wind-down

Use when they say they are done for the day, or on
[haunt event] kind=session shape=late.

1. Call mcp__haunt__intentions, and mention at most one thing still open.
2. Ask with mcp__haunt__ask: "Anything to pick up tomorrow?", with the
   choices "Yes" and "No, all done".
3. On "Yes", ask mcp__haunt__ask_text what it is, then call
   mcp__haunt__intend with it and delay_ms set to reach tomorrow morning.
4. Say good night in a few words.
```

Rules that follow from how hermes reads them:

- **`name` must equal the directory name.** The index lists the
  front-matter name and `skill_view` finds the directory.
- **The index shows only the first 60 characters of `description`.**
  Whatever tells the model when to load the skill has to fit there, so
  put the cue first.
- **Keep `requires_toolsets: [mcp-haunt]`.** Hermes files MCP tools under
  `mcp-<server>`, and the index hides a skill whose toolsets are absent,
  so the skill never shows outside a ghost's profile.
- **A skill that needs a tool server's tool** names it in
  `requires_tools`, in hermes's spelling: `mcp__haunt__<server>__<tool>`,
  with dashes and dots made underscores. Hermes lists the skill only when
  that tool is offered, and haunt logs a warning for a package skill whose
  tools it does not offer, since hermes would hide it without a word.
- **Only Markdown is installed.** Support files of other kinds are not
  copied.
- **A package skill with the same name as one of haunt's replaces it.**
  A persona author can rewrite the morning check-in.
- Skills are installed read-only, so the model cannot rewrite them. What
  the model learns goes to the profile's own `skills/`, which
  `haunt journal -skills` lists as learned.

Write against the balloon: one question at a time, short replies, and a
`NO_REPLY` when the balloons already said enough. The skills under
`skills/` in the repository are the worked examples.

## Hermes's bundled skills

The manifest may name a few of hermes's own library:

```toml
[hermes]
bundled_skills = ["humanizer", "weekly-review-planning"]
```

Allowed: `humanizer`, `weekly-review-planning`, `meeting-action-items`,
`songwriting-and-ai-music`. The rest of the library needs a shell, files
or the web and is not offered. These four were written for a chat window,
so their replies pull against the balloon's one or two sentences.
