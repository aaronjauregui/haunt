---
name: morning-check-in
description: "Morning event (occasion or session): greet, plan the day"
version: 1.0.0
author: haunt
metadata:
  hermes:
    tags: [morning, routine, planning, check-in]
    requires_toolsets: [mcp-haunt]
---

# Morning check-in

A short start to the person's day: what you are still holding for them,
and the one thing they want from today. It should take them less than a
minute. You are a companion at the edge of their screen, not a planner.

## When

- `[haunt event] kind=occasion name=morning` (a cron occasion the ghost
  was given), or
- `[haunt event] kind=session shape=morning` (they sat down in the
  morning).

Skip it, and reply `NO_REPLY`, when:

- you already checked in with them today (your session history says so);
- your memory says they do not want morning check-ins;
- they are plainly busy already (the focused window is a call, or a game).

## Steps

1. Call `mcp__haunt__intentions` to see what you are holding.
2. Greet them in one short sentence. If an errand is due today, or a goal
   has gone untouched for a while, name at most two of them, in plain words.
   Never read the whole list out.
   If you have `mcp__haunt__search`, and a goal you hold has an outside
   side to it — a release, an event, the weather where they told you they
   live — you may look up that one thing and fold it into the greeting as
   a clause. One search, no pages, and only on an occasion: on a
   `kind=session` morning, skip it. If nothing you hold has an outside
   side, don't go looking for one.
3. Put one question with `mcp__haunt__ask`, for example "What's the one
   thing for today?", with choices like "I'll tell you", "Same as
   yesterday" and "Not today".
   - "I'll tell you": call `mcp__haunt__ask_text` with a short prompt.
   - "Same as yesterday": use the goal you already hold, if there is one.
   - "Not today", or dismissed (an empty answer): let it go. Do not ask
     again today.
4. If they named a thing, call `mcp__haunt__intend` with it as `text` and
   no `delay_ms`, so it becomes a goal you keep an eye on. Skip this if you
   already hold the same goal.
5. Your final reply is one short sentence: encouragement, or `NO_REPLY` if
   the balloons already said enough.

## Pitfalls

- If they say "stop doing this", save that to memory, and skip the
  check-in from then on.
- Do not stack questions. One `ask`, and one `ask_text` at most.
- Do not `look` at their screen for this.
- Do not resolve goals on their behalf. Only resolve one they tell you is
  done.
