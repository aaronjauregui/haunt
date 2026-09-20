# Memory, reminders and goals

## What it remembers

A ghost on hermes remembers in hermes memory, written by the model alone.
Tell it something and it is kept; tell it it has something wrong and it
edits that memory. There is no separate belief store and no correction
menu. What it holds is in its profile for you to read:
`~/.hermes/profiles/haunt-<ghost>/memories/MEMORY.md` and `USER.md`.

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
aside: still on the books, no longer watched.

## Routines

A ghost on hermes carries a few routines as **skills**, short procedures
the model follows when one fits: a morning check-in, a focus session, a
weekly review, keeping an eye on a goal, drawing on request, looking
something up. A ghost's
package can add its own, a manifest can ask for a few of hermes's, and the
ghost may write new ones as it learns how you like things done.

    haunt journal -skills          # all of them, and which it wrote itself

A morning check-in runs on a `morning` occasion if the ghost's manifest
schedules one, or when you sit down in the morning; a weekly review runs
on a `weekly` occasion or on request.

## Why did it say that?

    haunt history                  # the conversation, read back from hermes
    haunt history -why             # per reply: what woke it, what it saw, what it did
    haunt history -json            # the same turns as JSON, one object per line
    haunt journal                  # what it did: tools run, pictures drawn,
                                   #   searches made and pages read
    haunt journal -style           # the look it paints in, and when that changed
    haunt journal -style -reset    # back to the look its package came with

`-why` shows whether a line answered you or an event, what window was in
front of it, which tools it used (hermes's own memory and skill lookups
included), and the turns where it chose to say nothing. The reasons live
in the ghost's own store and only the last dozen turns keep them. The
conversation itself is hermes's, so `haunt history` needs the ghost's
gateway running.

`-json` is for a program reading the conversation back (the eval harness
does). Each line is one turn, oldest first, and `-n` counts turns
(`-n 0` is all of them):

```json
{"time":"2026-09-18T09:14:02+10:00","provoked_by":"person","input":"remind me to check the oven",
 "reply":"Done — in ten minutes.","silent":false,"self_prompt":false,
 "acted":[{"tool":"intend","args":"{\"text\":\"check the oven\",\"delay_ms\":600000}","result":"hermes/…"}],
 "looked":[{"tool":"session_search","args":"{\"query\":\"oven\"}","result":"…"}],
 "window":{"app":"foot","title":"~"}}
```

| Field | |
|---|---|
| `provoked_by` | `person` for your words, `event` for an event from the desktop |
| `input` | what provoked the turn; an event is given without its envelope prefix |
| `reply` | every line the ghost wrote in the turn, one per line; empty when it said nothing |
| `silent` | the ghost said nothing |
| `self_prompt` | the ghost was woken rather than spoken to |
| `acted` | the ghost's own tools, as hermes called them, with their arguments and results whole |
| `looked` | hermes's own tools: memory, session search, skills |
| `window` | what was in front of the ghost, when it could see; left out otherwise |

The words and the calls come from hermes's session, which keeps every
turn. `window` comes from the reasons, so it is missing on a turn older
than the last dozen, and `self_prompt` there is read from the input being
an event.

Most things that happen on the desktop reach the model as a one-line
event, and most deserve no reply; the model answers those with silence
and nothing is shown.
