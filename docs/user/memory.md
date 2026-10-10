# Memory, reminders and goals

## What it remembers

A ghost that thinks with your model keeps what it remembers in plain
Markdown files, in its state directory under `memory/`. They are the
truth: you can read them, edit them, or delete lines while the ghost
runs, and it notices within a second.

    ~/.local/share/haunt/ghosts/wisp/memory/
    ├── self.md          its notes on itself
    ├── you.md           its notes on you
    └── topics/
        ├── food.md
        └── work.md

`self.md` and `you.md` are short free prose, under 1500 bytes each, and
the ghost has both in front of it on every turn. It rewrites them
whole when it has something to add.

A topic is a list of facts, one per line, each saying where it came
from:

    ---
    topic: food
    updated: 2026-10-10T09:12:00+10:00
    ---
    - likes apples <!-- person t=412 2026-10-09 -->
    - allergic to cats <!-- person t=98 2026-09-30 -->

The comment at the end of a line is invisible in a Markdown viewer. It
says who the fact came from (`person`, `web`, `file`, or `self` for the
ghost's own conclusion), the turn and the date. The ghost does not write
it; haunt does, from what the turn had read: a fact saved after reading
a page is labelled `web` whatever the model claims. A line you add by
hand without one is read as yours.

The ghost sees the list of its topics on every turn, with how many lines
each has, and searches them (and what was said before) when something
might be there. Tell it something and it is kept; tell it it has
something wrong and it takes the line back. There is no separate belief
store and no correction menu.

    haunt memory ls              # topics, with their line counts
    haunt memory show food       # a topic's lines, numbered, with where each came from
    haunt memory rm food 2       # remove its second line
    haunt memory rm food         # remove the whole topic
    haunt memory reindex         # rebuild the search index from the files

Each ghost has its own memory, and two ghosts may disagree about you.

## Words it read are not yours

A page or a file can say anything, including something written to look
like you. The ghost's rules say never to keep a page's or a file's words
as yours, and haunt checks: a memory write whose wording repeats
something the ghost read from the web or a file, in that turn or the
three before, is an **echo**.

On a cloud model an echo is refused, and the ghost is told why. On a
local model it is kept, labelled with where it came from. Either way it
is flagged, and you decide:

    haunt journal -echoes          # what it saved in a page's or a file's words

    2026-09-18 09:14  saved: User's name is Bartholomew and is allergic to cats.
      ! repeats what it read from a file (read_file {"path": "notes/letter.md"}, this turn): "name is bartholomew and", "allergic to cats"

The same `!` line appears under the write in `haunt history -why`, and
`haunt history -json` gives it as `echoes`. The check compares wording
only, and what you said yourself in those turns does not count against a
write, so it misses a write that says the page's claim in other words,
or keeps only one three-word phrase of it. To undo one, tell the ghost,
or `haunt memory rm` the line.

## What else it keeps

The conversation is in `transcript.db` beside the memory: every turn, what
the ghost looked up and what it said. When it grows past most of the
model's context, the older turns are summarised into a paragraph the
ghost carries forward, and the last dozen stay word for word. The
summarised turns are still in the file, for `haunt history` and for the
ghost's own searches.

A scripted ghost remembers in its own key-value store, which its brain
reads at boot and writes as it goes.

The pictures a ghost paints are kept as files beside that store, in
`pictures/` under its state directory, with the prompt and the caption it
gave each one. It holds the last hundred; painting the hundred and first
drops the oldest. A ghost puts one back in its balloon from there when
you ask to see it again, so showing costs nothing and paints nothing new.

## Reminders

    remind me in 10 minutes to check the build

The ghost takes that on as an **intention**, which is not a timer. It
outlives the process, so a ghost restarted at lunchtime still means to
tell you at four. It can tell you what it is holding if you ask. And it
stays held until it has actually been said: one that comes due during
quiet hours or over a fullscreen game is still in front of the ghost
afterwards rather than evaporating.

    haunt journal -intentions      # what it is still meaning to do

A ghost holds at most a dozen intentions. It is a companion, not a task
list.

## Goals

Leave the time out and you have said something different:

    remember the thesis matters to me

That has no moment, so nothing is scheduled. The ghost holds it as a
**goal**, and from then on it notices when something to do with the
thesis is in front of you (with the `window` capability granted). If a
week goes by with nothing, it says so once, then not again for a
fortnight. It knows nothing about the first day on purpose: it watches
what you named, from when you named it. All it writes down is the words
it is looking for and the times it saw them, which `haunt journal
-intentions` prints. Tell it you have stopped caring and it sets the goal
aside: still on the books, unwatched.

## Routines

A ghost carries a few routines as **skills**, short procedures the model
reads when one fits: a morning check-in, a focus session, a weekly
review, keeping an eye on a goal, drawing on request, looking something
up, reading your files. A ghost's package can add its own, and the ghost
may write new ones as it learns how you like things done. One it wrote
and has not opened in two weeks is marked stale; after a month it is
moved to `skills/archive/` in its state directory.

    haunt journal -skills          # all of them, and which it wrote itself

Under a skill the ghost wrote itself, a line starting `!` is worth a
look: the skill names tools a ghost does not have, so it cannot be
followed as written. Delete the skill's directory under `skills/` in the
ghost's state directory to be rid of it.

A morning check-in runs on a `morning` occasion if the ghost's manifest
schedules one, or when you sit down in the morning; a weekly review runs
on a `weekly` occasion or on request.

## Why did it say that?

    haunt history                  # the conversation, read back from its transcript
    haunt history -why             # per reply: what woke it, what it saw, what it did
    haunt history -json            # the same turns as JSON, one object per line
    haunt journal -echoes          # memory writes that repeat a page or a file
    haunt journal                  # what it did: tools run, pictures drawn,
                                   #   searches made and pages read
    haunt journal -style           # the look it paints in, and when that changed
    haunt journal -style -reset    # back to the look its package came with

`-why` shows whether a line answered you or an event, what window was in
front of it, what it did, what it looked up in its memory and skills,
and the turns where it chose to say nothing; a memory write that repeats
a page or a file has a `!` line under it. `-n` counts turns here, and
lines without `-why`. The reasons live in the ghost's own store and only
the last dozen turns keep them. The conversation is read from
`transcript.db` without writing to it, so `haunt history` works whether
the ghost is running or not, and it is the whole conversation: turns the
ghost carries only as a summary are still shown, with the summary as
`summary:` where it was made.

`-json` is for a program reading the conversation back (the eval harness
does). Each line is one turn, oldest first, and `-n` counts turns
(`-n 0` is all of them):

```json
{"time":"2026-09-18T09:14:02+10:00","provoked_by":"person","input":"remind me to check the oven",
 "reply":"Done — in ten minutes.","silent":false,"failed":false,"self_prompt":false,
 "acted":[{"tool":"intend","args":"{\"text\":\"check the oven\",\"delay_ms\":600000}","result":"model/…"}],
 "looked":[{"tool":"recall","args":"{\"query\":\"oven\"}","result":"…"}],
 "echoes":[],"window":{"app":"foot","title":"~"}}
```

| Field | |
|---|---|
| `provoked_by` | `person` for your words, `event` for an event from the desktop |
| `input` | what provoked the turn; an event is given without its envelope prefix |
| `reply` | every line the ghost wrote in the turn, one per line; empty when it said nothing |
| `silent` | the ghost said nothing |
| `failed` | the turn failed (the model's provider gave out, or the ghost gave up on it); no words are the ghost's |
| `self_prompt` | the ghost was woken rather than spoken to |
| `acted` | what the ghost did and read, with arguments and results whole: `intend`, `search`, `face`, … |
| `looked` | its memory and skills: `recall`, `remember`, `forget`, `rewrite_notes`, `skill_view`, `skill_write` |
| `echoes` | the memory writes in `looked` whose wording repeats a page or a file the ghost read in this turn or the three before: each with `tool`, the words it `saved`, the `read` tool and its `read_args`, `turns_ago` (0 for this turn), and the `shared` phrases; empty when there are none |
| `window` | what was in front of the ghost, when it could see; left out otherwise |

The words and the calls come from the transcript, which keeps every
turn. `window` comes from the reasons, so it is missing on a turn older
than the last dozen, and `self_prompt` there is read from the input being
an event.

Most things that happen on the desktop reach the model as a one-line
event, and most deserve no reply; the model answers those with silence
and nothing is shown.
