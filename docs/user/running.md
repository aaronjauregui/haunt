# Running a ghost

## The demos

Each demo opens a nested sway window in your current session and runs a
ghost inside it. Close the window or press Ctrl-C to stop. Everything the
ghost writes goes to a throwaway directory, so nothing lands in your
library.

    make demo         # whisper: a scripted ghost with a WASM brain
    make demo-ai      # wisp: no brain of its own, thinks with your [model]
    make demo-troupe  # both at once
    make demo-seriko SERIKO=<ghost.nar>   # somebody else's ukagaka shell art

`make demo-ai` needs a model in `[model]` first ([The model](model.md)).
Wisp declares `screen`, so with `[backend.vision]` configured it can look
at the focused window. The keys named in `api_key_env` have to be in the
environment `make` runs in ([Configuration](config.md)).
`IDLE_MS=10000` shortens the wait before a demo ghost may speak first,
and `GOAL_DELAY=5s` shortens the wait before it checks on goals, so
initiative is watchable without waiting around.

## Installing one

A ghost arrives as a signed `.haunt` package, or as a directory you are
working on.

    haunt install some-ghost.haunt    # asks which capabilities to grant
    haunt ls                          # * marks who is resident
    haunt run                         # runs everyone resident
    haunt run ./my-ghost              # a directory, without installing

Installing asks about the capabilities the package requests. Anything you
withhold is asked for again the first time it is needed, in the balloon
([What it may do](permissions.md)). Every package is signed; the author's
key is pinned on first install, and a later package under the same name
must be signed with the same key or it is refused.

## Who is resident

The desktop can hold several ghosts ([More than one](troupe.md)). The
resident set is who `haunt run` and the service start.

    haunt add moth      # moth moves in alongside whoever is there
    haunt drop moth     # moth moves out, keeping its memory and its grants
    haunt switch moth   # moth alone
    haunt rm moth       # uninstall, memory and grants included

## Talking to it

- **Poke it** (single click). A scripted ghost answers from its brain; a
  persona ghost answers from a short table, without waking the model.
- **Drag it** anywhere. It remembers where it was left, per screen.
- **Double-click** for its menu. A persona ghost offers "What can you
  do?", an offer to keep an eye on what you are working on, and nothing.
  A menu is answered with the mouse or with the digits, arrows, Enter and
  Escape.
- **Right-click** to type at it. An input line opens in its balloon and
  a conversation begins ([Talking at length](#talking-at-length)); the
  keyboard is taken only while the line is open, and held keys repeat at
  the compositor's rate (sway's `repeat_rate` and `repeat_delay`).
  A line the ghost opens to ask you something (`ask_text`) closes on
  Enter, and anything it finishes saying meanwhile is shown when the line
  closes.
- **Scroll** over it to go back through what was said this session: its
  lines, the pictures it showed and what you typed, a page at a time,
  each marked with who said it, how long ago and where it sits. Scrolling
  down comes forward again, and past the newest line the balloon goes.
  A long reply on screen pages with the wheel as well as with clicks.
  Taking the pointer off the ghost, or clicking it, puts the past away;
  anything new the ghost says replaces it. The last 50 are kept; the
  whole conversation is `haunt history` ([memory](memory.md)). In a
  conversation the wheel scrolls the conversation instead.
- **From a script**, `haunt say "text"`. With several ghosts resident and
  no `-ghost`, it goes to the one you last interacted with.

A persona ghost is talked to in plain language. Telling it something
about yourself is remembered; telling it it has something wrong corrects
that memory; asking it to remind you of something is taken on as an
intention ([Memory, reminders and goals](memory.md)).

## Talking at length

Typing at a ghost opens a conversation. Enter sends the line and the line
opens again, empty, with the keyboard still held, so the next thing can
be typed straight away; a line sent while the ghost is still answering
waits its turn. A persona ghost is told it may write a few sentences
rather than a remark.

The balloon widens to 480 pixels and shows the conversation: what you
typed and what the ghost said, each marked `you` or with the ghost's
name, its pictures as thumbnails, newest at the bottom, with the line you
are typing beneath. Your line appears there as soon as you press Enter,
and the ghost's words stream in below it as they arrive. The balloon
grows until it is about 520 pixels tall; past that, the wheel over the
ghost scrolls back through the earlier lines, three at a step, and
anything new brings it back to the bottom. What it shows is the last 50
things said this session, the same ones the wheel goes back through
outside a conversation. Nothing in the balloon goes away while the
conversation lasts.

Clicking the ghost during a conversation pokes it and leaves the
conversation and the half-typed line alone. The conversation ends when
you press Escape, click away (the keyboard goes elsewhere), after three
minutes with nothing typed and nothing said, or when the ghost says
goodbye. A ghost ends one with
`end_conversation`, and its goodbye is the last thing shown;
`[conversation] ghost_may_end = false` takes that away from it
([Configuration](config.md)). A question with choices, the ghost's or a
consent question, ends it too. Once it has ended, the last words stay up
for twelve seconds, as a reply does.

## Quiet hours

The driver enforces quiet, and no ghost can override it. Recurring windows
go in the config ([Configuration](config.md)); for right now:

    haunt shush          # quiet for an hour
    haunt shush 30m
    haunt unshush

Nothing speaks over a fullscreen window either, and the idle animation
stops while one has focus. A reminder that comes due during quiet is not lost: it is
held until it has actually been said.

## Reduced motion

`[animation] reduced_motion = true` stops idle animation (blinking,
breathing) and the face changing at rest. Animations a brain asks for as
part of saying something still play.

## Accessibility

Menus can be operated entirely from the keyboard. Poking, dragging and
opening the input line need a pointer, and nothing is exposed to a screen
reader.
