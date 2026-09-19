# The shell

A shell is the ghost's visual form. The model is small on purpose and is
the one the ukagaka ecosystem arrived at: a **stack of named layers**,
each of which may have several **variants**, and a **pose** is a choice
of variant per layer. Layering is what stops expressions and dress-up
multiplying into a redrawn body per combination.

The format nests, so complexity is paid for only when used:

    one PNG                → one layer, one pose, no animation
    + variants             → expressions
    + clips                → animation by swapping variants on a clock
    + transforms           → interpolated motion of layers

## From a drawing

Draw in layers and export OpenRaster (`.ora`), which Krita, GIMP, MyPaint
and Pencil2D all write. Then:

    haunt shell init -write mycompanion.ora     # writes shell/ and the [shell] section

The rules are meant to be guessable from the layer palette:

- A top-level layer becomes a layer with one appearance.
- A **group** becomes a layer whose children are its variants: an `eyes`
  group holding `open` and `closed` is a layer the ghost can blink with. A
  flat name with a slash (`eyes/closed`) means the same thing.
- The **visible** child of a group is the default variant. A hidden
  top-level layer is left out entirely, because that is where sketches,
  rulers and reference photos live.
- Drawing order becomes z order.
- Every layer is cropped to the pixels it covers, with the trim folded
  into its offset. A layer's variants are cropped together so they share
  one origin. This is what keeps a blink cheap: the driver repaints the
  bounds of what changed, and a blink drawn on a full-canvas sheet would
  repaint the whole ghost.

Names are tidied to what a TOML key and a file name can carry. The
command prints what it decided: layers left out, names changed.

## The `[shell]` section

The simplest shell is one image:

```toml
[shell]
sprite = "shell.png"
```

A layer stack:

```toml
[shell]
default_pose = "neutral"

[[shell.layer]]
name = "body"
image = "shell/body.png"
z = 0
region = "body"

[[shell.layer]]
name = "eyes"
variants = { open = "shell/eyes-open.png", closed = "shell/eyes-closed.png", happy = "shell/eyes-happy.png" }
default = "open"
at = [40, 30]
z = 1
region = "face"

[[shell.layer]]
name = "mouth"
variants = { flat = "shell/mouth-flat.png", smile = "shell/mouth-smile.png", open = "shell/mouth-open.png" }
default = "flat"
at = [48, 70]
z = 1
region = "face"

[shell.pose]
neutral = {}
happy = { eyes = "happy", mouth = "smile" }
sad = { eyes = "closed", mouth = "flat" }

[[shell.clip]]
name = "blink"
trigger = "sometimes"
keys = [
  { at = 0,  set = { eyes = "closed" } },
  { at = 90, set = { eyes = "open" } },
]

[[shell.clip]]
name = "talk"
trigger = "talk"
keys = [
  { at = 0,   set = { mouth = "open" } },
  { at = 120, set = { mouth = "flat" } },
  { at = 240 },
]

[[shell.clip]]
name = "breathe"
trigger = "always"
keys = [
  { at = 0,    move = { body = { offset = [0, 0] } } },
  { at = 1800, move = { body = { offset = [0, -2] } }, ease = "sine" },
  { at = 3600, move = { body = { offset = [0, 0] } }, ease = "sine" },
]
```

### Layers

| Field | Meaning |
|---|---|
| `name` | The slot a pose selects into. Required and unique. |
| `image` | The art for a layer with one appearance. Exclusive with `variants`. |
| `variants` | Named alternatives. A layer with variants needs a `default`. |
| `default` | The variant shown when no pose selects one. |
| `z` | Draw order; higher draws later. Ties break on declaration order. |
| `at` | Offset within the shell canvas. |
| `region` | The part of the ghost this layer draws (`head`, `face`, `hand`). A poke reports the topmost region it landed on, so a brain can tell being patted from being poked. |
| `pivot`, `parent` | The point transforms rotate and scale about, and the layer whose transform this one nests inside. Validated now; a rotating arm carrying its hand is what they are for. |

The canvas is the union of every layer's placed bounds. Clicks on
transparent pixels pass through to whatever is behind the ghost.

### Poses

`[shell.pose.<name>]` maps layer names to variants. Layers not mentioned
return to their default, so a pose describes a whole face rather than a
diff from whatever came before. `default_pose` names the one worn at
boot.

Five pose names mean something to the driver: **`happy`, `sad`,
`surprised`, `asleep`, `thinking`**. When a line is said without a face
being set, the driver picks one of these from the words. A shell that
names its poses these gets a face that follows what it says without its
brain asking for anything. A brain, or a model opening its reply with a
pose tag or calling its `face` tool, always wins over the inference. The driver also offers moments (a session
event, a return to the desk) at which a brain may change its face; whether
to is the brain's decision.

### Clips

A clip is a sequence of keyframes that swap variants and move layers on a
clock. `trigger` says who starts it:

| Trigger | Runs |
|---|---|
| `always` | restart as soon as it ends |
| `sometimes` | at random intervals of a few seconds (3 to 12) |
| `rarely` | at random intervals of a minute or so (45 to 150 seconds) |
| `talk` | loops for as long as the ghost is speaking |
| `runonce` | once, at boot |
| `manual` | only when the brain asks (the default) |

There is no numeric rate on purpose: the coarse vocabulary is the
granularity a person authoring by hand can reason about, and a shell that
needs a blink exactly every 4.7 seconds is a shell nobody has drawn.

Each key has `at` (milliseconds from the clip's start) and any of:

| Field | Meaning |
|---|---|
| `set` | a variant per layer, as a pose does; layers not named keep what the pose selected. Stepped, since a change of drawing cannot be tweened. |
| `move` | a transform per layer, interpolated from the previous key's value: `offset` in shell pixels, `rotate` in degrees clockwise about the pivot, `scale` about the pivot, `opacity` 0 to 1. Zero means unchanged; opacity unset means opaque. |
| `ease` | how to interpolate on the way to this key: `linear` (default), `sine` (smooth in and out) or `step` (hold until the key arrives). |

Several clips may run at once; they usually drive different layers, and
where two reach for the same layer the one started most recently wins.
Clips that only swap variants cost two timers and two repaints per cycle;
clips that move layers are resampled at 15 frames a second while running.
Under reduced motion only clips a brain asks for run.

### Hit regions

```toml
[[shell.hit]]
region = "hat"
at = [30, 0]
size = [60, 40]
```

Named rectangles, checked before the art. Hand-drawn shells should name
their parts on the layers instead, because a region on a layer follows
the drawing. Rectangles exist for imported shells, whose formats describe
hit areas as bare rectangles. A zero or negative size disables one.
