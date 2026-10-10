---
name: weekly-review
description: "Weekly review: when asked, or occasion name=weekly"
version: 1.0.0
author: haunt
metadata:
  haunt:
    requires_tools: [ask, ask_text, intend, intentions, resolve, set_aside]
---

# Weekly review

A few minutes at the end of the week: what you are holding for the person,
what is done, what to let go, and what to take on next. You hold the list;
they make the calls. Keep it under five minutes and five questions.

## When

- They ask for a review, or ask how their week went.
- `[haunt event] kind=occasion name=weekly` (a cron occasion the ghost was
  given). Call `stay_silent` instead if you already reviewed with them in
  the last few days, or they are plainly busy (a call, a game, a
  fullscreen window).

## Steps

1. Call `intentions`, and draw on what you remember of their week. Do
   not `look` at their screen.
2. Open with one short sentence: how many things you are holding, and
   anything that was dealt with this week, if your history shows it.
3. Go through the goals (entries marked "a goal"), oldest first, at most
   three. For each, one `ask` naming it in plain words, with
   choices like "Keep it", "It's done" and "Drop it".
   - "It's done": `resolve` with its name, and a word of
     congratulation.
   - "Drop it": `set_aside` with its name. No guilt.
   - "Keep it", or dismissed (an empty answer): leave it as it is.
   - If they dismiss two questions in a row, stop the review there.
4. Errands marked "due, not yet dealt with": name them together in one
   `ask` ("Still need these?") with "Yes, keep them" and
   "Clear them". Clear means `resolve` on each.
5. Ask once what is next: `ask` with "Anything new for next
   week?" and choices "I'll tell you" and "Nothing new".
   - "I'll tell you": `ask_text`, then `intend`
     with what they said and no `delay_ms`, so it becomes a goal.
6. Finish with one short sentence. Or call `stay_silent`, if the balloons
   already said it.

## Pitfalls

- Never read the whole list out in one balloon.
- Never resolve or set aside anything they did not decide on.
- An entry named `_haunt/drift/…` is the driver's note that a goal went
  quiet, not a goal of theirs: leave it alone here.
- If they say how they like their reviews (shorter, another day, not at
  all), remember it and follow it next time.
