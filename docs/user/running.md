# Running a ghost

## The demos

Each demo opens a nested sway window in your current session and runs a
ghost inside it. Close the window or press Ctrl-C to stop. Everything the
ghost writes goes to a throwaway directory, so nothing lands in your
library.

    make demo         # whisper: a scripted ghost with a WASM brain
    make demo-ai      # wisp: no brain of its own, runs on hermes-agent
    make demo-troupe  # both at once
    make demo-seriko SERIKO=<ghost.nar>   # somebody else's ukagaka shell art

`make demo-ai` needs hermes set up first ([The brain](hermes.md)).
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
  ghost on hermes answers from a short table, without waking the model.
- **Drag it** anywhere. It remembers where it was left, per screen.
- **Double-click** for its menu. A ghost on hermes offers "What can you
  do?", an offer to keep an eye on what you are working on, and nothing.
  A menu is answered with the mouse or with the digits, arrows, Enter and
  Escape.
- **Right-click** to type at it. An input line opens in its balloon; the
  keyboard is taken only while the line is open.
- **From a script**, `haunt say "text"`. With several ghosts resident and
  no `-ghost`, it goes to the one you last interacted with.

A ghost on hermes is talked to in plain language. Telling it something
about yourself is remembered; telling it it has something wrong corrects
that memory; asking it to remind you of something is taken on as an
intention ([Memory, reminders and goals](memory.md)).

## Quiet hours

The driver enforces quiet, and no ghost can override it. Recurring windows
go in the config ([Configuration](config.md)); for right now:

    haunt shush          # quiet for an hour
    haunt shush 30m
    haunt unshush

Nothing speaks over a fullscreen window either, and a ghost hides while
one has focus. A reminder that comes due during quiet is not lost: it is
held until it has actually been said.

## Reduced motion

`[animation] reduced_motion = true` stops idle animation (blinking,
breathing) and the face changing at rest. Animations a brain asks for as
part of saying something still play.

## Accessibility

Menus can be operated entirely from the keyboard. Poking, dragging and
opening the input line need a pointer, and nothing is exposed to a screen
reader.
