# Haunt

A desktop companion for Wayland. A **ghost** — a character with a
personality and its own logic — lives on your screen: it has a **shell**
(layered art that blinks, breathes and changes expression) and speaks in a
**balloon**. It remembers you across sessions, it can do things for you
through the tool servers you give it, and it will occasionally speak
first.

The terms are the ukagaka community's, kept deliberately. Nothing else is:
there is no compatibility with SHIORI, SSTP or SakuraScript, and this runs
on wlroots-based Wayland compositors (sway, Hyprland) and others that
implement `zwlr_layer_shell_v1`. See `DESIGN.md` for the whole design and
its reasoning, and `BACKLOG.md` for what is still open.

## Try it

    make demo         # a scripted ghost, in a nested sway window
    make demo-ai      # one that talks to your LLM
    make demo-troupe  # two of them at once, sharing a screen
    make demo-seriko SERIKO=<ghost.nar>   # somebody else's ukagaka shell
    make demo-sleep   # the first, with memory consolidation sped up to watch

Poke it, drag it, double-click for a menu, right-click to type at it. A
menu can be answered with the mouse or with the number keys. `make demo-ai` needs an
OpenAI-compatible endpoint in `~/.config/haunt/config.toml`:

```toml
[ai.llm]
base_url = "http://localhost:11434/v1"   # Ollama, llama.cpp, anything
model = "qwen2.5:7b"

[ai.embed]                                # optional: long-term memory
model = "nomic-embed-text"                # with this, it also sleeps: see below

[ai.image]                                # optional: it can draw
backend = "comfyui"
base_url = "http://localhost:8188"
model = "your-checkpoint.safetensors"

[ai.vision]                               # optional: it can look at a window
model = "qwen2.5vl:7b"
```

Any OpenAI-compatible endpoint works, which is nearly all of them; for a
vendor that speaks its own format, point `base_url` at a router like
LiteLLM. Cloud keys are named, not stored — set `api_key_env = "..."` and
the driver reads that variable from its own environment.

With an embedding model configured, the ghost remembers past conversations
and pulls the relevant ones back into context months later. It also
*sleeps*: while you are away it takes a cluster of old exchanges, works out
what they amounted to, and writes that down — in its journal, where you can
read it — along with what it now knows about the people and projects in
them. The detail fades afterwards on how much it mattered rather than on
how old it is. That is a model call you did not ask for, so it is rationed:
`[memory] consolidations_per_day` (four by default, zero to switch it off).

Everything AI is opt-in twice — the ghost asks for a capability, you grant
it — and the driver holds the keys and the budgets, never the ghost.
Anything you withhold at install is asked for again the first time it is
actually needed, in the ghost's balloon, about the thing happening right
then; "never ask again" is an answer it remembers. That
matters most for the two awareness capabilities: `window` lets a ghost see
which application you have focused and its title, and `screen` lets it
capture that window and show it to a vision model. Both are refused unless
granted, never fire for apps on `[awareness] ignore` (password managers and
the like, by default), and never run while something is fullscreen.

`window` is the one it asks for differently. What would use it is a poller,
so there is no moment of use to ask at, and a permission popup at login
about nothing in particular is the thing this design avoids — so instead
the ghost offers it from its menu ("Keep an eye on what I'm working on")
and you raise the question yourself, whenever you feel like it.

Granted, it watches the *shape* of the day rather than each window: a long
stretch in one file, a flip-flop between two applications, a title that
keeps coming back, coming back to the machine after an afternoon out, the
first thing you open in the morning, still being at it at midnight. That is
what it spends its unprompted remarks on. None of it leaves the machine,
and the little that is written down — enough to still recognise a
recurrence after a reboot — stores window titles as hashes, so it can be
counted but not read.

When it says something that seems to come from nowhere, you can ask where
it came from:

    haunt history -why           # per reply: what it recalled and what it could see
    haunt history -why -prompt   # and the prompt it was actually given

Recall is the part worth checking. It shows which memories came back, how
old they are, how strongly they matched and how often they have proved
useful before — so "it remembered" and "it guessed" stop looking the same.
The reasons live in the ghost's own store beside the conversation, never in
a session recording, and only the last dozen replies keep them.

`haunt perms` shows what a ghost asked for and what it has, and takes it
back:

    haunt perms                 # what it asked for, what it has
    haunt perms revoke window   # stops within seconds, no restart

## Doing things

A ghost can act, not only talk. Its manifest declares the **MCP tool
servers** it would like — the same servers editors and terminal agents use
— and the driver runs them, so the ghost never holds a credential and
nothing gets past the budgets:

```toml
[ghost]
capabilities = ["llm", "tools"]

[[tool]]
name = "notes"                       # tools are then "notes.append", …
command = ["mcp-server-notes", "--dir", "/home/me/notes"]
env = ["NOTES_TOKEN"]                # named, not stored — as with API keys
allow = ["append", "search"]         # optional: the manifest's own bound
```

That is the whole of it. There is no code to write: the tools are offered
to your model during ordinary conversation, so a ghost that is art plus a
persona can do things. A brain that wants a particular thing done at a
particular moment has `act` for that.

Permission is asked twice, deliberately. `tools` says the ghost may act at
all; each individual tool is asked about the first time it is about to run,
with the server's own description of it — a server offering "read a note"
and "delete the note" is one line in a manifest and two very different
questions. That second question is the only place **"just this once"** is
offered, and it records nothing.

    haunt perms                              # tools it has been allowed, per server
    haunt perms revoke tool:notes.append     # take one back

**Acting looks different from talking, always.** The ghost wears a working
face while a tool runs, names in its balloon what it did afterwards and
whether it worked, and writes it in its journal — so `haunt journal` and
`haunt history -why` both show what it has been up to. None of that is
configurable: an ambient process that quietly takes actions is a horror,
and those marks are what make it a companion instead.

The brakes are the driver's. `[ai.budget] tool_requests_per_hour` (60)
caps how fast it may act; `[ai] max_tool_calls` (6) stops one exchange
looping; and acting when nobody asked comes out of the same daily
allowance as speaking first, which a restart does not refill.

## More than one

A desktop can hold several ghosts. `haunt switch` picks one instead of the
others; `haunt add` and `haunt drop` are how you keep them:

    haunt add moth      # moth moves in alongside whoever is there
    haunt ls            # * marks who is resident
    haunt drop moth     # moth moves out, keeping its memory and its grants
    haunt run           # runs everyone resident

`make demo-troupe` puts two on screen in a nested sway if you want to see
it before committing your own desktop to it.

Each ghost is its own session — its own memory, its own brain, its own
compositor connection — so one crashing is one ghost lost rather than the
room. What they share is what belongs to you rather than to any of them:
nobody opens on top of anybody else, `haunt say` with no `-ghost` goes to
the one you last interacted with, the hourly limits on backend calls are
one ration between them rather than one each, and there is a ceiling on
unprompted remarks across all of them together. That last one is
sub-linear on purpose: a second ghost adds half an allowance rather than a
whole one, so three ghosts are more present than one without being three
times the interruptions or three times the model bill. Set it yourself with
`[curiosity] actions_per_day_total` if you disagree.

They can also see each other. What a ghost does in public — a line in its
balloon, a picture, a flourish it was asked for, being poked — reaches the
others as something they may react to without being spoken to, which is
where dynamics nobody wrote come from. Only public things: a ghost can
watch its neighbour and can never reach its neighbour's memory. Reacting
is unprompted, so it comes out of the same allowance as speaking first,
and the built-in ghost rations itself further on top of that — two
characters noticing each other occasionally is company, and two characters
commenting on everything each other does is a chat log you did not ask for
and are paying for.

Quiet hours, the fullscreen rule and reduced motion stay what they always
were: answers about the room, and every ghost obeys them.

## Live with it

    make install-user
    systemctl --user enable --now haunt

See `dist/README.md` for the details, including what to add to your sway or
Hyprland config if the service does not start.

## Make a ghost

    haunt new mycompanion
    cd mycompanion && haunt run .

That scaffolds a whole ghost: a manifest and a persona. There is no brain
in it and nothing to compile — a ghost that declares no brain runs on the
driver's built-in one, which chats in your persona, draws when asked, keeps
a journal, offers to notice what you are working on, uses whatever tool
servers you gave it, and speaks first now and then. What is left to do is
the art.

    haunt new -brain mycompanion

scaffolds the other kind, for a ghost whose logic is its own: a brain in Go
with a Makefile that builds it to WASM. Brains are stdio programs — events
in, intents out — and the same source builds as a sandboxed WASM module or
a native process. `examples/whisper` is one.

For the art, draw in layers and export a `.ora` (Krita, GIMP, MyPaint and
Pencil2D all write it):

    haunt shell init -write mycompanion.ora

Groups become variants — an `eyes` group holding `open` and `closed` is a
layer the ghost can blink with — the visible layer in each group is the
default, hidden top-level layers are left out as sketches, and every layer
is cropped to what it covers so animation stays cheap.

## Bringing over ukagaka shell art

Two decades of ukagaka shells exist. `haunt-seriko` converts their *art* —
surfaces, collisions and frame-swap animation — into a haunt shell:

    haunt-seriko -n ghost.nar        # what it would do
    haunt-seriko ghost.nar           # convert it

It reads SERIKO/1.x and 2.0, PNG+PNA and colour-keyed transparency, and
Shift-JIS files; surfaces become poses, collisions become named regions you
can poke, and animations become clips. It converts art only: SHIORI, SSTP
and SakuraScript are out of scope and stay that way, so what you get is a
shell a haunt brain wears, not somebody else's ghost.

To try one without installing anything, point the demo at it. The archive
is fetched, converted and run inside a throwaway directory, and the ghost's
memory goes there too, so nothing lands in your library:

    make demo-seriko SERIKO=~/Downloads/some_ghost.nar
    make demo-seriko SERIKO=https://example.org/some_ghost.nar

Worth knowing when one sits perfectly still: plenty of shells define no
animation at all. Their `surfaces.txt` is collision boxes and nothing else,
and every frame of movement came from the script switching surfaces — the
half this deliberately does not import. `-n` tells you how many clips a
shell actually has before you go looking for a bug.

`SERIKO_ARGS` passes flags through — `-only 0,1,2` keeps a two-hundred-
surface shell from converting all of it, `-surface N` picks what it rests
on.

A converted shell often looks static, and usually is not broken. Its idle
animation runs, but ukagaka shells lean on their script to change
expression constantly, and the script is the half this deliberately does
not import. Naming a few poses is what puts a converted shell back in
touch with its face:

    haunt-seriko -pose happy=3,sad=5,surprised=2 ghost.nar

Those names are what everything else here looks for. The face follows the
words a ghost says, and between sentences it follows the shape of the day —
sleepy after midnight, brighter when you come back from an afternoon out —
so a shell with a few poses named is a shell that keeps changing even when
nobody is talking. Only you can say which surface is a smile; `-n` lists
what the shell has.

## Commands

| | |
|---|---|
| `haunt run [-output NAME] [ghost ...]` | run the resident ghosts (each remembers where it was) |
| `haunt dev <dir>` | run with hot reload |
| `haunt say <text>` | talk to it from a script |
| `haunt history` / `haunt journal` | what was said / what it chose to remember |
| `haunt history -why [-prompt]` | why it said that: what it recalled, saw, and was asked |
| `haunt journal -entities` | who and what it thinks it knows about |
| `haunt perms grant window` | let it notice what you are working on (or ask it from its menu) |
| `haunt perms [grant\|revoke <cap>]` | what it may do — revocable while it runs |
| `haunt shush` / `unshush` | quiet hours, now |
| `haunt shell init <art.ora>` | build a shell from a layered drawing |
| `haunt-seriko <shell>` | convert ukagaka shell art into a haunt shell |
| `haunt add <name>` / `haunt drop <name>` | who lives on your desktop |
| `haunt new`, `pack`, `install`, `ls`, `switch`, `rm` | authoring and packages |

## Building

Go 1.24+, and `sway` plus `grim` for the tests, which run against a real
headless compositor rather than a mock.

    make build test
