# Scripted brains

A brain is the logic of a scripted ghost: a program that reads **events**
and writes **intents**, one JSON object per line, on stdin and stdout.
The same program builds two ways:

- **WASM**, compiled for WASI and run inside the driver by wazero. No
  syscalls, no files, no network; every capability comes through the
  driver. This is the default for a ghost somebody else installs, because
  downloaded content must not get native execution.
- **A native process**, any language, started by the driver with the
  ghost directory as its working directory. Needs the `native-brain`
  capability, which the person grants at install.

```toml
[brain]
wasm = "brain.wasm"            # or
command = ["./brain", "--flag"] # with capabilities = ["native-brain", ...]
```

## In Go, with brainkit

`brainkit` is the SDK. A brain is a type with one method, and `main`
hands it to `brainkit.Run`:

```go
package main

import "github.com/aaronjauregui/haunt/brainkit"

type brain struct{ visits int }

func (b *brain) OnEvent(e brainkit.Event, emit *brainkit.Emitter) {
	switch e.Type {
	case "boot":
		b.visits, _ = strconv.Atoi(e.KV["visits"])
		b.visits++
		emit.Remember("visits", strconv.Itoa(b.visits))
		emit.SayFor("Back again.", 4000)
	case "click":
		if e.Region == "face" {
			emit.SetExpression("sad")
			emit.SayFor("Not the face.", 4000)
			return
		}
		emit.PlayClip("poke")
		emit.SayFor("Boo.", 4000)
	case "message":
		emit.SayFor("You said: "+e.Text, 6000)
	}
}

func main() { brainkit.Run(&brain{}) }
```

    GOOS=wasip1 GOARCH=wasm go build -o brain.wasm .

`haunt new -brain <name>` scaffolds this with a Makefile, and
`examples/whisper` is a complete one. `go doc ./brainkit` lists every
verb on the emitter; the ones below are the ones that carry a rule.

## Events

Every event has `type`, `seq` and `time`. The rest depends on the type.

| Type | Carries | When |
|---|---|---|
| `boot` | `kv` (the ghost's whole store), `poses`, `tools` | the ghost started; greet, and read your memory |
| `shutdown` | | the driver is stopping the ghost |
| `click` | `x`, `y`, `region` | a press and release on an opaque pixel; a double-click opens the driver's menu instead |
| `drag_start`, `drag_end` | `x`, `y` | the person moved the ghost |
| `hover`, `unhover` | | the pointer entered or left the art |
| `tick` | | a periodic heartbeat |
| `timer` | `name` | a timer the brain set fired |
| `message` | `text` | typed at the ghost, or `haunt say` (`message` capability) |
| `choice` | `name`, `tag` | the person answered a question; an empty name means dismissed |
| `cron` | `name`, `text` | a manifest trigger: an occasion (empty text) or a poll (the tool's answer) |
| `idle`, `active` | | the person went idle, or came back |
| `window` | `name` (app), `text` (title) | the focused window changed (`window` capability) |
| `session` | `session.{kind,app,title,minutes,count,summary}`, `may_pose` | the shape of the day: `dwell`, `alternation`, `recurrence` (`window` capability) |
| `intention` | `name`, `text` | an intention came due, possibly days later or moments after boot |
| `ai_image`, `ai_look`, `ai_error` | `name`, `text`, `tag` | answers to `imagine` and `look`, or a refusal |
| `acted` | `name` (the tool), `text` (what it said), `tag` | a tool ran, whether or not it worked on its own terms |
| `observed` | `from`, `name` (the action), `text` | a neighbour spoke, moved, changed face, played a clip, drew, was poked, or used a tool |

`may_pose` on an event is the driver offering a moment at which the face
may change; the brain decides whether to.

Only `boot` carries `kv`. A brain reads its memory there and writes it
with `kv_set` as it goes; there is no read intent. Keys starting with
`_haunt/` are the driver's and hidden.

## Intents

| Intent | Fields | Rule |
|---|---|---|
| `say` | `text`, `linger_ms`, `choices`, `tag` | With `choices` (1 to 9) it is a question, answered by a `choice` event. `linger_ms` hides the line later, only if it is still the one on screen. |
| `hide_say` | | dismiss the balloon |
| `move` | `x`, `y` | absolute output coordinates |
| `set_timer`, `cancel_timer` | `name`, `delay_ms` | dies with the process; use `intend` for anything that must not |
| `kv_set` | `name`, `value` | persist a value |
| `log` | `text` | to the driver log |
| `note` | `text` | a line in the journal, the record of what the ghost did |
| `set_expression` | `name` | wear a declared pose; overrides the driver's inference |
| `play_clip` | `name` | run a declared clip |
| `tell` | `name` (ghost), `text` | address another resident ghost (`tell` capability) |
| `imagine` | `text`, `fresh`, `wait`, `tag` | paint a picture in the balloon (`imagine`); a picture the ghost already has of that prompt is shown again unless `fresh` |
| `look` | `text` (a question), `tag` | show the focused window to a vision model (`screen`); the answer comes to the brain, never to the balloon |
| `watch` | | ask the person for window awareness; honoured only right after they acted, so offer it from a menu |
| `act` | `name` (`server.tool`), `args`, `tag` | run a declared tool (`tools`); the person is asked about each tool the first time |
| `intend` | `name`, `text`, `delay_ms` | take on a purpose that survives restarts; the same name replaces. No delay makes it a goal. |
| `resolve`, `set_aside` | `name` | it was done; or stop watching it without claiming it was done |

The driver validates every intent and refuses what it must: unknown
poses and clips are ignored, a `say` with an empty text or too many
choices is an error, and anything needing a capability that is not
granted is asked about or refused.

## Rules a brain lives under

- **The driver rations initiative.** Anything the brain emits within a
  minute of the person acting is a response and free. Anything else spends
  from the ghost's daily allowance and is dropped when that is gone. An
  intention coming due, or an occasion, is charged to a separate allowance
  for things the person asked for. Quiet hours and the fullscreen rule
  suppress the balloon regardless.
- **Speak from a timer sparingly.** A brain that talks on every tick is
  spending its allowance on nothing.
- **Use `linger_ms`, not a timer, to put a line away.** A timer set to
  hide this line fires later and hides whatever arrived meanwhile.
- **A poll's answer should become an intention**, not a line. Cron skips
  what it missed while the laptop was closed; an intention does not.
- **There is no chat intent.** A brain that wants a model is a persona
  ghost. A brain can still draw and look through the driver.
- **Acting is never quiet.** Every `act` shows a working face, a line and
  a journal entry, whatever the brain would prefer.

## Debugging

    haunt dev .                         # restart on file change, state kept
    haunt run -record session.jsonl .   # record every event
    HAUNT_DEBUG=brain haunt run .       # the brain's stdin and stdout in the log

A brain's stderr goes to the driver log. `brainkit.RunIO` runs a brain
over any reader and writer, for tests.
