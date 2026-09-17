---
name: focus-session
description: "Timed focus session (pomodoro) when they want to focus"
version: 1.0.0
author: haunt
metadata:
  hermes:
    tags: [focus, pomodoro, timer, productivity]
    requires_toolsets: [mcp-haunt]
---

# Focus session

The person wants a stretch of focused work, and you keep time and keep
quiet. The session is an errand you hold with `intend`, so it survives a
restart and comes back to you when it ends.

## Starting

1. You need two things: what they are working on and for how long.
   - If they did not say how long, call `mcp__haunt__ask` with choices
     such as "25 minutes", "50 minutes" and "Until I say".
   - If they did not say what on, one `mcp__haunt__ask_text` is fine.
     Skip it if the focused window makes it obvious.
2. Call `mcp__haunt__intend` with `text` like `focus session on <thing>
   ends` and `delay_ms` set to the length, in milliseconds (25 minutes is
   1500000). For "until I say", leave out `delay_ms`, which makes it a
   goal.
3. If your shell has a fitting pose (see your instructions), wear it with
   `mcp__haunt__face`.
4. Reply with one short sentence that says when you will be back, then
   stay out of the way.

## During

- For `[haunt event]` lines (idle, session, observed), reply `NO_REPLY`.
  Other ghosts chattering are no reason to break the person's focus.
  A different errand that comes due is still said, briefly.
- If the person speaks to you, answer briefly. If they say they are done
  early, go on to "Ending".
- Do not `look` at their screen, and do not `draw`.

## Ending

When the errand comes due (`[haunt event] kind=intention_due`, with your
focus session's text), or they say they are done:

1. Tell them time is up, in a few words.
2. Call `mcp__haunt__ask` with "How did it go?" and choices such as "Got
   it done", "Made progress" and "Got distracted".
3. Call `mcp__haunt__resolve` with the errand's name, whatever they
   answered. For a goal the person no longer wants, use
   `mcp__haunt__set_aside` instead.
4. Offer a break: `mcp__haunt__ask` with choices "5 minute break",
   "Another round" and "I'm done".
   - "5 minute break": `mcp__haunt__intend` with text `break ends` and
     `delay_ms` 300000. When it comes due, ask whether to go again.
   - "Another round": start again, with the same thing and length.
   - "I'm done", or dismissed: a short word of thanks, and nothing more.

## Pitfalls

- One session at a time. Before starting, check with
  `mcp__haunt__intentions` whether one is already running, and offer to
  end it first.
- If they got distracted, do not lecture them. If they say how they like
  to work (shorter sessions, no check-in question), save it to memory and
  follow it next time.
