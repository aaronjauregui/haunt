---
name: weekly-review
description: "Asked for a weekly review, or kind=occasion name=weekly"
version: 1.0.0
author: haunt
metadata:
  haunt:
    requires_tools: [ask, ask_text, intend, intentions, resolve, set_aside]
---

# Weekly review

You hold the list; they make the calls. Five questions at most.

On an occasion, call `stay_silent` instead if you reviewed in the last
few days or they are plainly busy.

1. Call `intentions`. Do not `look` at their screen.
2. For each goal (marked "a goal"), oldest first, at most three: call
   `ask` with a `text` naming it plainly and the `choices` "Keep it",
   "It's done", "Drop it". Your opening words go in the first `text`.
   - "It's done": `resolve` with its `name`.
   - "Drop it": `set_aside` with its `name`.
   - "Keep it", or dismissed: leave it. Two dismissals in a row: stop.
3. Errands marked "due, not yet dealt with": one `ask` with `text`
   "Still need these?" and `choices` "Yes, keep them", "Clear them".
   Clear: `resolve` each.
4. Call `ask` with `text` "Anything new for next week?" and `choices`
   "I'll tell you", "Nothing new". On "I'll tell you": `ask_text`, then
   `intend` what they typed as `text`, with no `delay_ms`.
5. End with one short sentence, or `stay_silent`.

Leave entries named `_haunt/...` alone: they are the driver's notes.
