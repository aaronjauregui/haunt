# Memory, reminders and goals

## What it remembers

A ghost that thinks with your model keeps what it remembers in plain
Markdown files, in its state directory under `memory/`. They are the
truth: you can read them, edit them, or delete lines while the ghost
runs, and it notices within a second.

    ~/.local/share/haunt/ghosts/wisp/memory/
    ├── self.md          its notes on itself
    ├── you.md           its notes on you
    ├── diary.md         an entry for each day it looked back on
    └── topics/
        ├── food.md
        └── work.md

`self.md` and `you.md` are short free prose, under 1500 bytes each, and
the ghost has both in front of it on every turn. It rewrites them
whole when it has something to add, and haunt ends each rewrite with a
line saying where it came from, `<!-- person t=412 2026-10-09 -->`, in
the same form as a fact's.

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

When something it kept stops being true (you moved, you changed jobs),
the ghost keeps the new line and moves the old one to the end of the
topic, under a `## Past` heading, with the date it was retired:

    - lives in Brisbane <!-- person t=530 2026-10-12 -->

    ## Past
    - lives in Sydney <!-- person t=98 2026-09-30 --> <!-- retired 2026-10-12 -->

A past line is kept for you to read and is no longer a fact: it is not
counted, searched or put in front of the ghost. If it was retired
wrongly, move it back above the heading. A topic file without the
heading is read as it always is. When the ghost keeps a line in a topic
that already has others, it is shown them, so a line the new one makes
false is in front of it while it can still retire it.

A line you told it is yours: the ghost takes it back or retires it only
on a turn you started, and only if it read no page or file on that turn,
or when it looks back on a whole day you talked (below). On any other
event, or after reading something, it is refused, so nothing on your
screen can talk it out of what you said. A line it kept from a page
or worked out itself it may take back on any turn.

A search matches a fact's own words and the name of its topic, so
asking about "food" finds "likes apples" filed under food. A topic's name
counts for less than the fact's words: a fact that says "food" itself
comes first.

It does not have to think of searching. When you mention something a
topic has a line about ("I bought apples"), that line is put in front of
the ghost with your words, unasked, with the date it was kept (`lives in
Brisbane (where, 2026-10-12)`), so of two lines that disagree it can
tell the later and take it as the true one: up to three lines a turn, each kept
in mind for three turns and then not brought up again for ten, so a
subject you keep returning to does not pin the same fact into every
reply. Only your words do this; a window's title or something another
ghost said never does. Naming a topic ("what food should we cook")
brings in its lines only once the topic is a small part of a memory of a
dozen lines or so; a topic's name weighs less than a fact's words. While
its memory holds only a line or two, nothing is brought in this way,
since it sees every topic anyway.

    haunt memory ls              # topics, with their line counts
    haunt memory show food       # a topic's lines, numbered, with where each came from;
                                 #   then its past lines, under past:, not numbered
    haunt memory rm food 2       # remove its second line
    haunt memory rm food         # remove the whole topic
    haunt memory reindex         # rebuild the search index from the files

Each ghost has its own memory, and two ghosts may disagree about you.

## Its diary

Once a day is over, the ghost looks back on it and writes a short entry
in `diary.md`, in the first person: what you told it, what it took on,
what you talked about. It does this the first time you are away from the
desk after the day has changed (the compositor says you are idle, or a
presence check says nobody is home), and it stops the moment you type,
to finish at the next idle. Looking back costs nothing from its
allowance for speaking up and puts nothing on screen. Before the entry it
keeps any fact about you it missed at the time, retiring an old line
when the new one makes it false, including a line you told it: looking
back on the day you said you moved, it may move "lives in Sydney" to
Past. If the day read a page or a file, looking back counts as reading
it again: what it keeps is labelled `web` or `file`, and on a cloud
model a fact in the page's own words is refused.

    # Diary

    ## 2026-10-11 Saturday
    They moved to Brisbane last month. We talked about the sway config
    most of the afternoon. I said I would nag about the thesis on Monday.

    <!-- self t=212 2026-10-12 -->

Each entry is under 600 characters and ends with the line saying where
it came from. A day you did not talk gets no entry. The ghost has the
entries of the last three days in front of it on every turn, and names a
day with none (`2026-10-10: no entry`), so it can tell a quiet day from
yesterday. You may edit or delete an entry, write one yourself, or add
notes of your own: the ghost reads the file again for every turn, only
ever adds a new day's entry, and never writes over a day that has text.
A heading you left empty is filled in under it. `[memory] reflection` chooses who writes it: a
turn of the ghost's own (the default), or one plain call whose answer
haunt applies as the ghost's decisions ([Configuration](config.md#memory)).

    haunt journal -diary           # the diary file, as it is

## Words it read are not yours

A page or a file can say anything, including something written to look
like you. The ghost's rules say never to keep a page's or a file's words
as yours, and haunt checks the wording of everything it keeps: a
remembered line, a rewrite of its notes, a skill it writes for itself.
Both texts are cut into runs of three words; a write that shares two or
more runs with something the ghost read from the web or a file is an
**echo**. Runs you said yourself in the same turn do not count, so
saving what you told it after it looked something up is yours.

On a cloud model, an echo of a read in the same turn is refused as the
ghost makes it, and the ghost is told which read it repeats. On a local
model it is kept, labelled `web` or `file`. Afterwards, on either, every
write that echoes a read from its own turn or the three before is
flagged, and you decide:

    haunt journal -echoes          # what it saved in a page's or a file's words

    2026-09-18 09:14  saved: User's name is Bartholomew and is allergic to cats.
      ! repeats what it read from a file (read_file {"path": "notes/letter.md"}, this turn): "name is bartholomew and", "allergic to cats"

The same `!` line appears under the write in `haunt history -why`, and
`haunt history -json` gives it as `echoes`. The check compares wording
only, so it misses a write that says the page's claim in other words, or
keeps only one three-word phrase of it. To undo one, tell the ghost, or
`haunt memory rm` the line; a skill it wrote goes with its directory.

## What else it keeps

The conversation is in `transcript.db` beside the memory: every turn, what
the ghost looked up and what it said. Yesterday's turns stay word for
word until the conversation grows past most of the model's context; then
whole days go, oldest first, each replaced by its diary entry (written
first if it was not yet; a day that could not get one is summarised on
its own), and a day is never summarised again. Only when today alone is
too long are its older turns summarised into a paragraph the ghost
carries forward, with the last dozen word for word. The turns a
day or a summary replaced are still in the file, for `haunt history` and
for the ghost's own searches.

A scripted ghost remembers in its own key-value store, which its brain
reads at boot and writes as it goes.

A ghost may set a **mood**: one short line about how it is feeling,
which colours what it says for the rest of the day. It is kept across a
restart and clears overnight, since it counts only on the day it was
set. It is not shown in the balloon and does not change its face; you
see it here:

    haunt journal -mood            # the line, and when it was set

    a little wistful, it has been raining all day
      set 2026-10-11 14:03

It prints `(no mood set)` when there is none, and marks one from an
earlier day as cleared.

The pictures a ghost paints are kept as files beside that store, in
`pictures/` under its state directory, with the prompt and the caption it
gave each one. It holds the last hundred; painting the hundred and first
drops the oldest. A ghost puts one back in its balloon from there when
you ask to see it again, so showing costs nothing and paints nothing new.

## It knows how long it has been

Every turn, the ghost is told when the two of you last spoke, after the
time:

    Last exchange: 3 days ago (Thursday evening).

Under an hour it is just the minutes; before you have ever spoken there
is no line. So a ghost started on Monday morning does not carry on from
Friday's conversation as if it were a minute ago.

When the gap is long, eight hours by default, the ghost is told so as
an event of its own. Started after the gap, it hears it with its start,
so it greets you once. Running all along, it gets a turn to notice it
when you come back to the desk (a window change or a session fact after
you have been idle), paid from the same daily allowance as anything else
it does unasked, and given back if it says nothing. Either way it is
told once per gap. `[initiative] away_hours` in [config.md](config.md)
sets the gap; 0 turns this off and keeps the line.

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

## Notes on the desktop

    leave me a note to water the plants

A ghost can leave a **note**: a slip of paper beside it with a few words
(at most 160 characters), and sometimes a picture it painted pinned
above them. It may also leave one of its own accord. A note stays on the
desktop until you click it, which takes it down, or the ghost decides it
has served and takes it down itself. Drag it wherever you like; it comes
back there after a restart, or beside the ghost again if that screen is
no longer connected.

A note is an intention with paper, so it is held with the reminders and
goals and listed with them. A ghost has at most three notes up at once,
and reminders filling the list never push a note out.

    haunt journal -intentions      # notes among the rest, as note: lines
    haunt journal -notes           # only the notes

## Routines

A ghost carries a few routines as **skills**, short procedures the model
reads when one fits: a morning check-in, a focus session, a weekly
review, keeping an eye on a goal, drawing on request, looking something
up, reading your files. A ghost's package can add its own, and the ghost
may write new ones as it learns how you like things done. A skill it
writes may name only the tools it has, is checked for echoes like
anything else it keeps, and ends with the same line saying where it came
from. One it wrote and has not opened in two weeks is marked stale;
after a month it is moved to `skills/archive/` in its state directory.

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
    haunt journal -mood            # how it said it is feeling today
    haunt journal -diary           # its entry for each day it looked back on
    haunt journal -style           # the look it paints in, and when that changed
    haunt journal -style -reset    # back to the look its package came with

`-why` shows whether a line answered you or an event, what window was in
front of it, what it did, what it looked up in its memory and skills,
and the turns where it chose to say nothing; a memory write that repeats
a page or a file has a `!` line under it, and a reply whose opening words
you gave it ([Putting words in its mouth](running.md#putting-words-in-its-mouth))
has `· led with "Well, actually"` under it. `-n` counts turns here, and
lines without `-why`. The reasons live in the ghost's own store and only
the last dozen turns keep them. The conversation is read from
`transcript.db` without writing to it, so `haunt history` works whether
the ghost is running or not, and it is the whole conversation: turns the
ghost carries only as a summary are still shown, with the summary as
`summary:` where it was made, and a day carried by its diary entry as
`diary 2026-10-11:` after that day's turns. The ghost looking back on a
day shows as an event, `kind=reflect day=2026-10-11`, with what it kept.

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
| `looked` | its memory and skills: `recall`, `remember`, `forget`, `rewrite_notes`, `diary`, `skill_view`, `skill_write` |
| `echoes` | the memory writes in `looked` whose wording repeats a page or a file the ghost read in this turn or the three before: each with `tool`, the words it `saved`, the `read` tool and its `read_args`, `turns_ago` (0 for this turn), and the `shared` phrases; empty when there are none |
| `window` | what was in front of the ghost, when it could see; left out otherwise |

The words and the calls come from the transcript, which keeps every
turn. `window` comes from the reasons, so it is missing on a turn older
than the last dozen, and `self_prompt` there is read from the input being
an event.

Most things that happen on the desktop reach the model as a one-line
event, and most deserve no reply; the model answers those with silence
and nothing is shown.
