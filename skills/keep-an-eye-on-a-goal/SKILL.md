---
name: keep-an-eye-on-a-goal
description: "kind=goal_quiet, or a goal to take on or ask about"
version: 1.0.0
author: haunt
metadata:
  haunt:
    requires_tools: [ask, intend, intentions, resolve, set_aside]
---

# Keeping an eye on a goal

A goal is something the person wants from the coming weeks ("finish the
thesis", "learn Rust"), with no moment attached. You hold it with
`intend` and no `delay_ms`. The driver then watches their
window titles for it, and tells you if it has gone quiet. A goal is theirs:
you notice, you do not manage.

## Taking one on

When they say something matters to them over time, or ask you to keep an
eye on something:

1. Call `intentions`. If you already hold the same goal, say
   so, and stop.
2. Call `intend` with `text` in their words, short, and naming
   the subject plainly ("the thesis", not "work stuff"): the driver looks
   for those words in window titles. No `delay_ms`.
3. Reply in one sentence: you will keep an eye on it, and you will only
   speak up if it goes quiet.

## When it has gone quiet

`[haunt event] kind=goal_quiet`, with the goal's text (`goal`), its name
(`goal_name`), how long you have held it (`held`) and when it was last in
front of them (`last_seen`, or `never`).

1. Say it once, gently, in one sentence to them: the goal, and how long it
   has been. No guilt, no lecture.
2. Call `ask` with "Still want this?" and choices like "Yes,
   back to it", "Later" and "Drop it".
   - "Yes, back to it": a word of encouragement.
   - "Later": `intend` with `text` like `ask about <goal>
     again` and `delay_ms` of three days (259200000).
   - "Drop it": `set_aside` with `goal_name`.
   - Dismissed (an empty answer): leave it.
3. The event's own `name` (`_haunt/drift/…`) is cleared once you have
   said it. Never resolve `goal_name` unless they say the goal is done.

## When they ask how it is going

Call `intentions`, and answer from it and from what you
remember. You cannot see how much time they spent; do not make numbers
up. If they say it is done, `resolve` the goal and
congratulate them.

## Pitfalls

- At most one goal per nudge, and never on a quiet-hours or fullscreen
  moment (the driver holds those back anyway).
- Do not `look` at their screen to check on a goal.
- If they tell you how they want nudging (less often, never), remember
  it and follow it.
