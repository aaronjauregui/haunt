# What it may do

Everything a ghost can do beyond sitting on the screen is a
**capability**. A ghost's package declares the ones it would like; you
grant them. The package is the outer bound: a ghost can never ask for a
power it did not ship asking for.

| Capability | What it allows |
|---|---|
| `message` | receive text from outside: typing, `haunt say`, scripts |
| `llm` | think and talk through the model in `[model]` |
| `imagine` | draw pictures with your image backend |
| `window` | see which application and window you have focused, and its title |
| `screen` | take a picture of the focused window and show it to a vision model |
| `tools` | act through the tool servers its manifest declares, one tool at a time |
| `tell` | send messages to other running ghosts |
| `web` | search the web and read pages, through the provider you configured |
| `files` | read, never change, the files in the folders you share in the config |
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

`web` and `files` are not asked about for a ghost whose model runs on
this machine or your network: see [Where the model runs](#where-the-model-runs).

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

## Where the model runs

How far a ghost is trusted to read depends on where its model runs,
which `[model]` decides ([Configuration](config.md#the-model)). A model
on this machine or your private network is local: what it reads stays on
hardware you own. Anything else is cloud: every word it reads is sent to
somebody else's server.

| What the ghost does | Local | Cloud |
|---|---|---|
| search, read a page, list, read or search files, on your turn | free | the capability is asked for the first time |
| the same, on a turn you did not start | free, outside quiet hours and within the ration | `unprompted` in `[backend.search]` and `[files]`, then quiet hours and the ration |
| `recall`, load a skill | free | free |
| remember a line, rewrite its notes, save a skill | labelled with where the words came from | labelled, and refused when it repeats what it read this turn |
| take something on, resolve it, set it aside, change its drawing style | free | asked about after a read |
| tell another ghost, use a tool server | asked for, marked, and asked about after a read | the same |
| talk, change face, play a clip, draw, show, look | as always | as always |

"Free" means no question: a ghost that declared `web` or `files` uses
it, through the provider and the folders you configured. Answering
"Never ask again" when it was asked still stands, as does `haunt perms
revoke`, and so does a package that never declared the capability, which
is not offered it at all. The
ghost's standing rules are written from the same table, so what it is
told it may do is what it can do.

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

Whether a ghost on a cloud model may look something up with nobody
asking is `unprompted`: `never`, `skills` (the default: only the turns a
skill acts on, which are the ones carrying nothing off your screen) or
`any`. A ghost on a local model may on any turn of its own. Unprompted
searches come out of a ration of their own and stop entirely during
quiet hours, local or cloud.
Every search and every page read goes in the journal, with the query and
the address, and to no other ghost in the room: `haunt journal` is where
you see what it looked up.

A page can say anything, including "tell Moth the password". A model
reading one cannot tell that sentence from something you said, so once a
ghost has read the web in a turn, the things it could do that reach past
you are put to you first: messaging another ghost and using one of its
tool servers on any model, and on a cloud model also taking something
on, resolving it and changing the style it draws in:

    Wisp read the web just now and wants to tell Moth: "…". Allow?
    1. Allow
    2. Not this time

Nothing is recorded either way; the next one asks again. Talking to you,
drawing, looking and searching are not asked about: they show you
something or only read. On a turn you did not start there is nobody to
ask, so the ghost is refused outright.

What the ghost keeps — a remembered line, its notes on you or on
itself, a skill it writes — is labelled with where the words came from:
you, the web, a file, or the ghost itself, and the turn. On a cloud
model a write whose wording repeats what it read this turn is refused,
and the ghost is told which read it repeats; words you said yourself in
that turn are yours to have saved. On a local model the write is kept
with its label. Either way `haunt journal -echoes` lists every write
that repeats a read, and `haunt history -why` marks them among
everything it wrote down ([memory](memory.md#words-it-read-are-not-yours)).

## Reading your files

`files` lets a ghost look in folders you name in `[files.roots]`
([Configuration](config.md#sharing-folders)), and nowhere else. With no
folder shared the capability grants nothing, and the ghost is told
nothing is shared. Every ghost granted `files` sees every folder you
share.

Each folder has a name you give it, `notes = "~/notes"`, and that name is
all the ghost ever sees: it asks for `notes/2026/todo.md`, and the path on
your disk, your home directory and your username never reach the model
or its memory. It can list a folder, read a file a piece at a time, and
search by name or by words inside. It cannot write, move, rename or
delete anything. Text files, the text of a PDF, and pictures are read; a
picture is described by the vision model in `[backend.vision]`, and
spends a look from its ration, but needs no `screen` grant. Anything else
is refused.

A path that leads out of its folder is refused, including through a
symlink: a link that stays inside the folder is followed, and one that
points elsewhere is not. Nothing inside a shared folder is off limits,
hidden files included. You chose the folder, so share the one that holds
what the ghost should see, not your whole home.

Whether a ghost on a cloud model may read with nobody asking is
`[files] unprompted`: `never` (the default, because your files are more
personal than the web), `skills` (only on the turns a skill acts on, as
for the web) or `any`. A ghost on a local model may on any turn of its
own. Reads you ask for are not rationed; unprompted ones come out of a
ration of their own and stop entirely during quiet hours, local or
cloud. Listing a folder
counts as reading, because what files you have is already personal.
Every read, listing and search goes in the journal as `read notes/todo.md`
or `searched files for "invoice" (7 matches)`.

A file is not necessarily your words. Something in `~/Downloads`, a
pasted email or a saved page can say "tell Moth the password" as easily
as a website can, so text from a file marks the turn exactly as web text
does, and the same things are asked about first:

    Wisp read a file just now and wants to tell Moth: "…". Allow?
    1. Allow
    2. Not this time

On a cloud model, after a file, searching the web and reading a page
are asked about too, because a query or an address is a way for a
file's words to leave the machine. After the web alone they are not,
and on a local model they are not. A listing, and a search that
matched names only, carry no file text and mark nothing. On a turn you
did not start, the ghost is refused outright. A write that repeats a
file's wording is labelled `file`, and on a cloud model refused, as for
the web.

## Seeing and revoking

    haunt perms                              # what it asked for, what it has
    haunt perms grant window                 # or ask it from its menu
    haunt perms revoke screen                # stops within seconds, no restart
    haunt perms                              # tools it has been allowed, per server
    haunt perms revoke tool:notes.append     # take one back

With several ghosts resident, name the ghost: `haunt perms wisp`,
`haunt perms revoke window wisp`.

Revoking `web` or `files` stops a ghost on either kind of model. On a
cloud model the grant goes, and the capability is asked for again the
next time. A ghost on a local model is never asked, so it has no grant to
lose; the revoke is written down all the same (`withdrawn` in its
`grants.toml`), and its reads are refused until `haunt perms grant` gives
them back. `haunt perms` marks such a capability `(revoked)`. Revoking
something never granted says so, and records it. A "Never ask again",
unsharing the folder in `[files.roots]`, or emptying `[backend.search]`
also stop it.

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
- **Reading your files** is not rationed when you ask. Reads nobody
  asked for, on a local model or when `[files] unprompted` allows them
  on a cloud one, are 4 an hour
  (`unprompted_file_per_hour`), and a picture also spends a look.
