---
name: keep-an-eye-on-a-goal
description: "A goal to hold, a goal gone quiet (drift), or how it goes"
version: 1.0.0
author: haunt
metadata:
  hermes:
    tags: [goal, habit, accountability, nudge]
    requires_toolsets: [mcp-haunt]
---

# Keeping an eye on a goal

A goal is something the person wants from the coming weeks ("finish the
thesis", "learn Rust"), with no moment attached. You hold it with
`mcp__haunt__intend` and no `delay_ms`. The driver then watches their
window titles for it, and tells you if it has gone quiet. A goal is theirs:
you notice, you do not manage.

## Taking one on

When they say something matters to them over time, or ask you to keep an
eye on something:

1. Call `mcp__haunt__intentions`. If you already hold the same goal, say
   so, and stop.
2. Call `mcp__haunt__intend` with `text` in their words, short, and naming
   the subject plainly ("the thesis", not "work stuff"): the driver looks
   for those words in window titles. No `delay_ms`.
3. Reply in one sentence: you will keep an eye on it, and you will only
   speak up if it goes quiet.

## When it has gone quiet

`[haunt event] kind=intention_due` with a `name` starting `_haunt/drift/`
and a `text` like "bring up what they said mattered — … — because …".

1. Say it once, gently, in one sentence: the goal, and how long it has
   been. No guilt, no lecture.
2. Call `mcp__haunt__ask` with "Still want this?" and choices like "Yes,
   back to it", "Later" and "Drop it".
   - "Yes, back to it": a word of encouragement.
   - "Later": `mcp__haunt__intend` with `text` like `ask about <goal>
     again` and `delay_ms` of three days (259200000).
   - "Drop it": find the goal's own name with `mcp__haunt__intentions`
     (it starts `hermes/`), and `mcp__haunt__set_aside` it.
   - Dismissed (an empty answer): leave it.
3. Do not resolve the `_haunt/drift/…` entry yourself. The driver clears
   it once you have said it.

## When they ask how it is going

Call `mcp__haunt__intentions`, and answer from it and from what you
remember. You cannot see how much time they spent; do not make numbers
up. If they say it is done, `mcp__haunt__resolve` the goal and
congratulate them.

## Pitfalls

- At most one goal per nudge, and never on a quiet-hours or fullscreen
  moment (the driver holds those back anyway).
- Do not `look` at their screen to check on a goal.
- If they tell you how they want nudging (less often, never), save it to
  memory and follow it.
