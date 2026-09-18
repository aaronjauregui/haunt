# More than one ghost

A desktop can hold several ghosts. `haunt add` and `haunt drop` decide who
lives there; `haunt run` and the service run everyone resident.

    haunt add moth      # moth moves in alongside whoever is there
    haunt ls            # * marks who is resident
    haunt drop moth     # moth moves out, keeping its memory and its grants

`make demo-troupe` puts two on screen in a nested sway to see it before
committing your own desktop to it.

## Each on its own

Each ghost is its own session: its own memory, its own brain, its own
compositor connection. One crashing is one ghost lost rather than the
room, and it comes back a couple of seconds later and greets you again,
which is how you find out. A ghost that dies over and over is eventually
left stopped, and haunt says so. One that never reached the screen is not
retried at all, because that is a manifest to fix rather than a ghost to
revive.

## What they share

What belongs to you rather than to any of them:

- **Placement.** Nobody opens on top of anybody else.
- **`haunt say`** with no `-ghost` goes to the one you last interacted
  with.
- **Hourly backend budgets** are one ration between them, not one each.
- **A ceiling on unprompted remarks** across all of them together. It
  grows sub-linearly: a second ghost adds half an allowance, so three
  ghosts are more present than one without being three times the
  interruptions or three times the bill. `[curiosity]
  actions_per_day_total` sets it outright.
- **Quiet hours, the fullscreen rule and reduced motion** are answers
  about the room, and every ghost obeys them.

## Seeing each other

What a ghost does in public reaches the others: a line in its balloon, a
picture, a flourish it was asked for, being poked. They may react without
being spoken to, which is where dynamics nobody wrote come from. Only
public things: a ghost can watch its neighbour and can never reach its
neighbour's memory. Reacting is unprompted, so it comes out of the same
allowance as speaking first, and a ghost on hermes is woken about its
neighbours at most once an hour on top of that. A ghost with the `tell`
capability can also address another by name.
