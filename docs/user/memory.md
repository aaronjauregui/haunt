# Memory, reminders and goals

## What it remembers

A ghost on hermes remembers in hermes memory, written by the model alone.
Tell it something and it is kept; tell it it has something wrong and it
edits that memory. There is no separate belief store and no correction
menu. What it holds is in its profile for you to read:
`~/.hermes/profiles/haunt-<ghost>/memories/MEMORY.md` and `USER.md`.

A scripted ghost remembers in its own key-value store, which its brain
reads at boot and writes as it goes.

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
weekly review, keeping an eye on a goal, drawing on request. A ghost's
package can add its own, a manifest can ask for a few of hermes's, and the
ghost may write new ones as it learns how you like things done.

    haunt journal -skills          # all of them, and which it wrote itself

A morning check-in runs on a `morning` occasion if the ghost's manifest
schedules one, or when you sit down in the morning; a weekly review runs
on a `weekly` occasion or on request.

## Why did it say that?

    haunt history                  # the conversation, read back from hermes
    haunt history -why             # per reply: what woke it, what it saw, what it did
    haunt journal                  # what it did: tools run, pictures drawn

`-why` shows whether a line answered you or an event, what window was in
front of it, which tools it used (hermes's own memory and skill lookups
included), and the turns where it chose to say nothing. The reasons live
in the ghost's own store and only the last dozen turns keep them. The
conversation itself is hermes's, so `haunt history` needs the ghost's
gateway running.

Most things that happen on the desktop reach the model as a one-line
event, and most deserve no reply; the model answers those with silence
and nothing is shown.
