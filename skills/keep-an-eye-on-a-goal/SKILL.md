---
name: keep-an-eye-on-a-goal
description: "kind=goal_quiet, or a goal to take on, drop or finish"
version: 1.0.0
author: haunt
metadata:
  haunt:
    requires_tools: [ask, intend, intentions, resolve, set_aside]
---

# Keeping an eye on a goal

A goal is something they want from the coming weeks ("finish the
thesis"), with no moment. You notice; you do not manage.

## Taking one on

1. Call `intentions`. If you already hold it, say so and stop.
2. Call `intend` with `text` in their words, naming the subject plainly
   ("the thesis", not "work stuff"), and no `delay_ms`.
3. Say in one sentence that you will speak up only if it goes quiet.

## kind=goal_quiet

The event carries `goal`, `goal_name`, `held` and `last_seen`.

1. Call `ask`. Its `text` is one gentle sentence naming the goal and
   how long it has been, then "Still want this?". Its `choices`: "Yes,
   back to it", "Later", "Drop it". No guilt.
2. On the answer:
   - "Yes, back to it": a word of encouragement.
   - "Later": `intend` with `text` `ask about <goal> again` and
     `delay_ms` 259200000 (three days).
   - "Drop it": `set_aside` with `name` set to the event's `goal_name`.
   - Dismissed: leave it.
3. End with a few words, or `stay_silent`.

## They drop it or finish it

- "I don't care about X anymore", "forget the marathon": `set_aside`
  with its `name` from your turn context, then a short word, no guilt.
- "I finished X": `resolve` with its `name`, then congratulate them.

## They ask how it is going

Answer from `intentions` and what you remember. You cannot see how
long they spent; never invent numbers. Do not `look` at their screen.
