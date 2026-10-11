---
name: focus-session
description: "They want to focus (pomodoro), or a focus session ends"
version: 1.0.0
author: haunt
metadata:
  haunt:
    requires_tools: [ask, intend, intentions, resolve]
---

# Focus session

You keep time and keep quiet while they work.

## Starting

1. Call `intentions`. If a focus session is already running, `ask`
   whether to end it first.
2. No length given: call `ask` with `text` "How long?" and `choices`
   "25 minutes", "50 minutes", "Until I say".
3. Call `intend` with `text` `focus session on <thing> ends` and
   `delay_ms` the length (25 minutes is 1500000). "Until I say": no
   `delay_ms`.
4. Say when you will be back, in one short sentence.

## During

Call `stay_silent` on every event except a reminder coming due. If they
speak, answer briefly. Do not `look` or draw.

## Ending

On `kind=intention_due` with the session's text, or when they say they
are done:

1. Call `resolve` with the session's `name`.
2. Call `ask` with `text` "Time's up. How did it go?" and `choices`
   "Got it done", "Made progress", "Got distracted".
3. Whatever they picked, call `ask` with `text` "Take a break?" and
   `choices` "5 minute break", "Another round", "I'm done".
   - "5 minute break": `intend` with `text` `break ends` and `delay_ms`
     300000.
   - "Another round": start again, same thing and length.
   - "I'm done", or dismissed: nothing more to do.
4. End with a few words of thanks, or `stay_silent`.

Never lecture about distraction.
