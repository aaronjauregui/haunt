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
implement `zwlr_layer_shell_v1`.

Two documents sit behind this one: `DESIGN.md` for what haunt is meant to
be and why, and `docs/hermes-brain.md` for how a ghost's thinking is handed
to [hermes-agent](https://github.com/nousresearch/hermes-agent). The code is
heavily commented and is the record of what exists.

## Try it

    make demo         # a scripted ghost, in a nested sway window
    make demo-ai      # one that thinks, on hermes-agent
    make spin-ai      # the same, headless, through a scripted conversation
    make demo-troupe  # two of them at once, sharing a screen
    make demo-seriko SERIKO=<ghost.nar>   # somebody else's ukagaka shell

Poke it, drag it, double-click for a menu, right-click to type at it. A
menu can be answered with the mouse or with the number keys.

## A ghost that thinks

A ghost with no brain of its own — art and a persona, nothing compiled —
runs on **hermes-agent**. Hermes is the judgment: it keeps the
conversation, remembers what matters about you, decides whether an event is
worth a word and what to say. Haunt is the body and the conscience: the
screen, the balloon, consent, budgets, quiet hours, and the reminders it has
promised to keep. Each ghost gets a hermes profile of its own
(`haunt-<ghost>` under your hermes home) and its own gateway, started and
stopped with the ghost.

To set it up:

1. Install hermes (`uv pip install -e 'hermes-agent[mcp]' aiohttp`, Python
   3.11–3.13), or build its Docker image and use `scripts/hermes-docker`;
   `docs/hermes-brain.md` §12 has the container recipe.
2. Give the ghost a model. Either per profile, the hermes way:

       hermes -p haunt-wisp model

   or for every ghost at once, in `~/.config/haunt/config.toml`:

   ```toml
   [hermes]
   # command = ["hermes"]           # the default; or scripts/hermes-docker
   # home = "~/.hermes"             # the default, or $HERMES_HOME
   # tool_timeout = 600             # seconds a tool (an `ask`) may take
   # first_token_timeout = 30       # seconds of silence before giving up

   [hermes.model]
   provider = "zai"
   name = "glm-5.3"
   ```

   Hermes needs a model with a context window of at least 64K; small local
   models are refused, and follow the ghost's rules badly when forced.
   Provider keys stay hermes's business (its `.env`, or the environment).
3. Declare `llm` in the ghost's capabilities and run it.

What hermes may use is fixed by haunt, not by the model: its memory, its
session search, its skills, and haunt's own verbs as tools — speak, ask,
wear a face, draw, look, hold a reminder, tell another ghost — offered
through `haunt mcp-serve`. No shell, no files, no browser. Every one of those
verbs goes through the same gates as anything else the ghost does.

Talking to it is just talking. It remembers what you tell it in hermes
memory, and when you tell it it has something wrong it corrects that
memory — there is no separate belief store to edit. What hermes holds is in
the profile (`memories/MEMORY.md`, `memories/USER.md`) for you to read.

Routines are hermes skills: short Markdown procedures the model loads when
one fits. Haunt gives every ghost a few of its own (`skills/`: a morning
check-in, a focus session, a weekly review, keeping an eye on a goal,
drawing on request), a ghost package can carry more in a `skills/`
directory of its own, and the ghost may write new ones as it learns how you
like things done. Hermes's own library is left out, because nearly all of
it needs a shell, files or the web; a manifest may ask for the few that do
not by name:

```toml
[hermes]
bundled_skills = ["humanizer", "weekly-review-planning"]   # also meeting-action-items, songwriting-and-ai-music
```

`haunt journal -skills` lists all of these. Haunt installs its own, the
package's and the bundled ones read-only, so the model cannot rewrite them.
A morning occasion (`[[trigger]]` with `name = "morning"` and no tool) runs
the morning check-in, and one named `weekly` runs the weekly review.

Ask it to remember to *do* something:

    remind me in 10 minutes to check the build

and it takes that on as an **intention**, which is not a timer. It outlives
the process — a ghost restarted at lunchtime still means to tell you at
four — it can tell you what it is holding, and it stays held until it has
actually been handed back. So a reminder whose moment arrives during quiet
hours, or over a fullscreen game, stays in front of it until it has said
it, instead of evaporating. `haunt journal -intentions` lists what yours is
carrying.

Leave the time out and you have said something different:

    remember the thesis matters to me

That has no moment, so nothing is scheduled. The ghost can hold it as a
goal, and from then on it notices when something to do with the thesis is
in front of you. If a week goes by with nothing, it will say so — once, and
then not again for a fortnight. It knows nothing on the first day and that
is deliberate: it watches what you named, from when you named it. All it
writes down is the words it is looking for and the times it saw them, and
`haunt journal -intentions` prints both. Tell it you have stopped caring
and it sets the goal aside: still on the books, no longer watched.

Most things that happen on the desktop reach the model as a one-line event
(`[haunt event] kind=idle minutes=2 …`), and most deserve no reply; the
model answers those with silence and nothing is shown. The driver decides
first whether an event may reach the model at all, from the same daily
allowance that has always rationed a ghost speaking first — and a wake that
ends in silence costs nothing.

Drawing and looking are the driver's own verbs, so their backends are
configured in haunt:

```toml
[ai.image]                                # optional: it can draw
backend = "comfyui"
base_url = "http://localhost:8188"
model = "your-checkpoint.safetensors"

[ai.vision]                               # optional: it can look at a window
base_url = "http://localhost:11434/v1"
model = "qwen2.5vl:7b"
```

Any OpenAI-compatible endpoint works for both. Cloud keys are named, not
stored — set `api_key_env = "..."` and the driver reads that variable from
its own environment.

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
first thing you open in the morning, still being at it at midnight. Those
are the events it may be woken for. None of it leaves the machine except as
that one line to your model, and the little that is written down — enough
to still recognise a recurrence after a reboot — stores window titles as
hashes, so it can be counted but not read.

When it says something that seems to come from nowhere, you can ask where
it came from:

    haunt history                # the conversation, read back from hermes
    haunt history -why           # per reply: what woke it, what it saw, what it did

`-why` shows whether a line answered you or an event, what window was in
front of it, which tools it used, and the turns where it chose to say
nothing. The reasons live in the ghost's own store, never in a session
recording, and only the last dozen turns keep them. The conversation itself
is hermes's, so `haunt history` needs the ghost's gateway running.

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
to hermes alongside the ghost's own verbs, through the same haunt MCP
server, so a ghost that is art plus a persona can do things. A brain that wants a particular thing done at a
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
caps how fast it may act, `[ai] tool_timeout` bounds one call, and the
model is only woken unasked out of the same daily allowance as speaking
first, which a restart does not refill.

A package skill can be written for one of these tools. Hermes shows it only
when the tool is there, under the name hermes gives it —
`mcp__haunt__<server>__<tool>`, with dashes and dots made underscores:

```yaml
metadata:
  hermes:
    requires_toolsets: [mcp-haunt]
    requires_tools: [mcp__haunt__notes__append]
```

The driver logs a warning for a package skill whose tools it does not
offer, since hermes hides such a skill without a word.

### Being woken by the world

Tools are pull-only: the model reaches for them in the middle of a turn. A **trigger** is the other direction — the driver asking, on a
schedule, and telling the ghost what it heard:

```toml
[[trigger]]
cron = "*/15 * * * *"
name = "calendar"
tool = "calendar.upcoming"           # a tool from one of the servers above
args = { within = "2h" }
```

That is the whole of haunt's integration story, and the reason there is no
integration code anywhere in it: your calendar is an MCP server and one
trigger line, your house is an MCP server and one trigger line, and so is
whatever you use next year.

A poll never speaks. What it does is give the ghost something it now means
to raise with you — because a schedule silently skips whatever it missed
while the laptop was shut, and a thing the ghost is *meaning to do* survives
that, and a restart, and a quiet hour. It also leaves the ghost's screen
alone: no balloon, no journal entry, nothing in its memory, and nothing at
all when the answer is the same as last time. The tool itself is consented
to by name like any other, in a question that says it is asking about a
standing arrangement rather than about one call — and it is only ever asked
while you are at the desk.

One trigger does something else. Give it `quiet_when` and it becomes a
presence check:

```toml
[[trigger]]
cron = "*/5 * * * *"
name = "presence"
tool = "home.person_state"
args = { person = "me" }
quiet_when = "not_home"              # your server's word for it, not ours
```

When the answer contains that text the ghost keeps quiet, which is a better
answer to "should this be talking" than `22:00-08:00` is. It wears off by
itself after half an hour, so a laptop that stops asking gets its ghost back
rather than staying mute.

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
room, and the one that crashed comes back a couple of seconds later. It
will greet you again, which is how you find out. A ghost that dies over and
over is eventually left stopped and said so; one that never got as far as
the screen is not retried at all, because that is a manifest to fix rather
than a ghost to revive. What they share is what belongs to you rather than
to any of them:
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
and a ghost on hermes is woken about its neighbours at most once an hour
on top of that — two characters noticing each other occasionally is
company, and two characters commenting on everything each other does is a
chat log you did not ask for and are paying for.

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

That scaffolds a whole ghost: a manifest, a persona and an example skill.
There is no brain in it and nothing to compile — a ghost that declares no
brain runs on hermes, which talks in your persona, remembers you, takes on
reminders, draws when asked, uses whatever tool servers you gave it, and
speaks first now and then. What is left to do is the art.

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
| `haunt history` / `haunt journal` | what was said / what it did |
| `haunt history -why` | why it said that: what woke it, what it saw, what it did |
| `haunt journal -intentions` | what it is still meaning to do |
| `haunt journal -skills` | the routines it has, and which it wrote itself |
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
