# What it may do

Everything a ghost can do beyond sitting on the screen is a
**capability**. A ghost's package declares the ones it would like; you
grant them. The package is the outer bound: a ghost can never ask for a
power it did not ship asking for.

| Capability | What it allows |
|---|---|
| `message` | receive text from outside: typing, `haunt say`, scripts |
| `llm` | think and talk through hermes-agent |
| `imagine` | draw pictures with your image backend |
| `window` | see which application and window you have focused, and its title |
| `screen` | take a picture of the focused window and show it to a vision model |
| `tools` | act through the tool servers its manifest declares, one tool at a time |
| `tell` | send messages to other running ghosts |
| `web` | search the web and read pages, through the provider you configured |
| `native-brain` | run its brain as a native process instead of in the WASM sandbox |

## Consent in the moment

Installing asks about the requested capabilities once, at the least
informed moment there will ever be. So a refusal there is not final:
anything withheld is asked for again the first time it is actually
needed, in the ghost's balloon, about the thing happening right then.

    Wisp would like to generate dialogue with your configured language model. Allow?
    1. Allow
    2. Not now
    3. Never ask again

"Allow" records the grant and the request proceeds. "Not now" refuses this
once. "Never ask again" is remembered, so "no" does not mean "ask me again
in a minute". The question is only ever put after something you did, so
there is something on screen to make sense of it. Nothing asks from a
background loop, and nothing asks at login.

`message` and `native-brain` are settled at install and never asked in the
balloon: one decides whether a socket exists, the other how the brain is
executed, and neither can change mid-session.

## Watching your screen

The two awareness capabilities are separate on purpose. `window` is
cheap and continuous: application name and title. `screen` is a picture of
the focused window, never the whole desktop, shown to a vision model, and
the ghost visibly indicates that it is looking while it happens.

Both are refused for applications on the ignore list, which by default
covers password managers, keyrings, polkit and pinentry, and can be
replaced in `[awareness] ignore` ([Configuration](config.md)). Neither
runs while something is fullscreen.

`window` is asked for differently, because what would use it is a poller
and there is no moment of use to ask at. The ghost offers it from its menu
("Keep an eye on what I'm working on"), and you raise the question
whenever you like. It can also be granted from the command line.

Granted, the ghost watches the shape of your day rather than each window:
a long stretch in one file, a flip-flop between two applications, a title
that keeps coming back, returning after an afternoon away, the first thing
opened in the morning, still being at it after eleven at night. Those are
the moments it may be woken for. Nothing leaves the machine except one
line to your model, and what is written down to recognise a recurrence
after a reboot stores window titles as hashes, countable but not readable.

## Acting

`tools` says the ghost may act at all. Each individual tool is then asked
about the first time it is about to run, with the tool server's own
description of it, because a server offering "read a note" and "delete
the note" is one line in a manifest and two very different questions.
That second question is the only place "just this once" is offered, and it
records nothing.

Acting always looks different from talking: a working face while the tool
runs, a line in the balloon afterwards saying what was done and whether it
worked, and an entry in the journal. None of that is configurable.

## Reading the web

`web` lets a ghost look something up and read one page, through the
provider set in `[backend.search]` ([Configuration](config.md)). With no
provider configured the capability grants nothing: there is nothing to
search with, and the ghost is told so.

What leaves the machine is the query, to that provider, and nothing else:
not your window titles, not your conversation. Pages are fetched from
this machine, and only from public addresses — a link to something on
your own machine or your own network is refused, after the name has been
resolved and again on every redirect, so a ghost cannot be pointed at
your router's admin page.

Whether it may look something up with nobody asking is `unprompted`:
`never`, `skills` (the default: only the turns a skill acts on, which are
the ones carrying nothing off your screen) or `any`. Unprompted searches
come out of a ration of their own and stop entirely during quiet hours.
Every search and every page read goes in the journal, with the query and
the address, and to no other ghost in the room: `haunt journal` is where
you see what it looked up.

A page can say anything, including "tell Moth the password". A model
reading one cannot tell that sentence from something you said, so once a
ghost has read the web in a turn, the things it could do that reach past
you — messaging another ghost, taking something on, resolving it,
changing the style it draws in, using one of its tool servers — are put
to you first:

    Wisp read the web just now and wants to tell Moth: "…". Allow?
    1. Allow
    2. Not this time

Nothing is recorded either way; the next one asks again. Talking to you,
drawing, looking and searching are not asked about: they show you
something or only read. On a turn you did not start there is nobody to
ask, so the ghost is refused outright.

One gap, and it is deliberate: what the ghost remembers is hermes's
memory, not haunt's, and haunt cannot gate it. Its standing rules forbid
saving a page's claims as yours, and `haunt journal` shows what it wrote
down, which is how you would catch it.

## Seeing and revoking

    haunt perms                              # what it asked for, what it has
    haunt perms grant window                 # or ask it from its menu
    haunt perms revoke screen                # stops within seconds, no restart
    haunt perms                              # tools it has been allowed, per server
    haunt perms revoke tool:notes.append     # take one back

With several ghosts resident, name the ghost: `haunt perms wisp`,
`haunt perms revoke window wisp`.

## Budgets and allowances

The driver holds the brakes, and a ghost cannot raise them.

- **Speaking first** spends from a daily allowance of unprompted actions
  (12 by default, `[curiosity] actions_per_day`). A wake that ends in
  silence costs nothing. A restart does not refill it.
- **Reminders you asked for** come out of a separate, generous allowance
  (`obligations_per_day`, 24), so a ghost's whims can never crowd out
  something you put on its books.
- **Backend calls** are rationed per hour, shared across all resident
  ghosts: images (12), looks (60), tool calls (60) and web requests (30,
  of which 4 may be spent unasked), under `[backend.budget]`.
