---
name: morning-check-in
description: "kind=occasion name=morning, or kind=session shape=morning"
version: 1.0.0
author: haunt
metadata:
  haunt:
    requires_tools: [ask, ask_text, intend, intentions]
---

# Morning check-in

A start to their day in under a minute: what you hold for them, and the
one thing they want from today.

Call `stay_silent` instead if you already checked in today, they told
you they do not want check-ins, or they are plainly busy (a call, a
game).

1. Call `intentions`.
2. If you have `search`, and on an occasion (not a session) a goal you
   hold has an outside side to it (a release, an event, the weather where
   they live), you may search that one thing once. No pages.
3. Call `ask`. Its `text` is a one-sentence greeting, naming at most
   two things you hold, then "What's the one thing for today?". Its
   `choices`: "I'll tell you", "Same as yesterday", "Not today".
   - "I'll tell you": `ask_text` with a short `prompt`, then `intend`
     what they typed as `text`, with no `delay_ms`, unless you already
     hold it.
   - "Same as yesterday": keep the goal you hold.
   - "Not today", or dismissed: let it go.
4. End with a few words of encouragement, or `stay_silent`.

One `ask` and at most one `ask_text`. Do not `look` at their screen.
