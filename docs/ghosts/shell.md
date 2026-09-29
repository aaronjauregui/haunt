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
    + masks                → a fixed canvas, and a place for each part

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
| `variants` | Named alternatives. A layer with variants needs a `default`, unless it is masked. |
| `default` | The variant shown when no pose selects one. |
| `z` | Draw order; higher draws later. Ties break on declaration order. |
| `at` | Offset within the shell canvas, in art pixels. |
| `region` | The part of the ghost this layer draws (`head`, `face`, `hand`). A poke reports the topmost region it landed on, so a brain can tell being patted from being poked. |
| `pivot`, `parent` | The point transforms rotate and scale about, in art pixels from the layer's own origin, and the layer whose transform this one nests inside, so that a rotating arm carries its hand. A parent must exist, and parents may not form a cycle. |
| `mask` | The [mask](#masks) this layer's art must stay inside. |

The canvas is the union of every layer's placed bounds, except in a
shell with masks, where it is the masks' size. Clicks on transparent
pixels pass through to whatever is behind the ghost.

### Scale

```toml
[shell]
scale = 0.25
```

`scale` is how large the art is drawn: 0.25 draws it at a quarter of its
pixel size. It must be above 0 and at most 4; omitted, it is 1.
Everything in the manifest is in **art pixels**, the pixels of the
images as drawn: a layer's `at` and `pivot`, a hit rectangle, a clip's
`offset`, and every image and mask. The loader resamples the art and
scales every coordinate once, so a ghost painted at 1024 pixels sits on
the desktop at 256 while the full-resolution art stays in the ghost
directory. A clip's `rotate`, `scale` and `opacity` have no unit and are
used as written.

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
| `move` | a transform per layer, interpolated from the previous key's value: `offset` in art pixels, `rotate` in degrees clockwise about the pivot, `scale` about the pivot, `opacity` 0 to 1. Zero means unchanged; opacity unset means opaque. |
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

Named rectangles in art pixels, checked before the art. Hand-drawn
shells should name their parts on the layers instead, because a region
on a layer follows the drawing. Rectangles exist for imported shells,
whose formats describe hit areas as bare rectangles. A zero or negative
size disables one.

## Masks

A layer stack says what each part looks like, not where it may go. Parts
drawn one at a time, by a different hand or in a different sitting, land
wherever each was drawn: a mouth whose `open` variant sits a little to
the left of its `smug` one jumps around the face every time the `talk`
clip swaps them. A **mask** marks where a part goes, and every variant of
the layer bound to it is held inside it at load, so the mouths stay put.
Masks also fix the canvas, so the ghost does not change size as more art
is drawn, and they mark where something goes (a hat, a prop) before anyone
has drawn it.

Masks are opt-in. Everything in this section applies only to a shell
that declares `[shell.masks]`, which needs a layer stack; a sprite or the
placeholder cannot have masks, and a layer may not name a mask in a shell
that declares none.

```toml
[shell]
scale = 0.25                 # drawn at a quarter of the art's pixel size
default_pose = "smug"

[shell.masks]                # all the same size, and that size is the canvas
silhouette = "shell/masks/silhouette.png"
eyes = "shell/masks/eyes.png"
mouth = "shell/masks/mouth.png"
hat = "shell/masks/hat.png"

[[shell.layer]]
name = "body"                # no mask: part of the base, drawn with no face
image = "shell/body.png"
at = [212, 180]
z = 0
region = "body"

[[shell.layer]]
name = "eyes"
mask = "eyes"
variants = { smug = "shell/eyes-smug.png", closed = "shell/eyes-closed.png" }
default = "smug"
at = [400, 380]
z = 1
parent = "body"
region = "face"

[[shell.layer]]
name = "mouth"
mask = "mouth"
variants = { smug = "shell/mouth-smug.png", open = "shell/mouth-open.png" }
default = "smug"
at = [460, 492]
z = 2
parent = "body"
region = "face"

[[shell.layer]]
name = "hat"                 # an empty slot: its only variant is "none"
mask = "hat"
z = 3
parent = "body"
region = "hat"

[shell.pose]
smug = {}
asleep = { eyes = "closed", mouth = "none" }

[[shell.clip]]
name = "talk"
trigger = "talk"
keys = [
  { at = 0,   set = { mouth = "open" } },
  { at = 120, set = { mouth = "smug" } },
  { at = 240 },
]
```

### Mask files

A mask is a PNG the size of the whole canvas, painted where the part may
go. A pixel's value is its brightness times its opacity, so a greyscale
image and white paint on a transparent layer both work. A value of 128
or more is inside and anything fainter is outside, though a soft edge is
kept in the file. Masks are never drawn.

- **All masks are the same size, and that size is the canvas**, in art
  pixels. Every drawn pixel of every layer must fall on it; transparent
  margins may hang over the edge.
- **`silhouette` is required.** It marks the whole ghost.
- **`eyes` and `mouth`** are optional, and must lie on the silhouette.
- **Any other name is a slot**: a place for something worn or held
  (`hat`, `prop`, `outfit`). A slot may reach outside the silhouette,
  since a hat sits above the head.
- Names are lowercase letters, digits and `-`, and every mask must mark
  something.

### Masked layers

`mask = "<name>"` binds a layer to a mask. Every pixel of every variant
that is more than faintly visible (opacity 32 of 255 or more) must fall
inside the mask, with a little slack for an antialiased edge: 2 art
pixels, or 0.5% of the canvas's longer side if that is more. Art that
strays is refused, with the layer, the variant and where:

    shell layer "mouth" variant "open" strays outside mask "mouth": 768 pixels fall outside the mask, within (480,566)-(544,578)

Every masked layer can show nothing: it has a variant **`none`**, which
draws nothing, unless it declares a variant of that name itself. Poses
and clips select `none` like any other variant, which is how a face comes
off (`asleep` above) or a hat is not worn. A masked layer with variants
and no `default` shows `none`; one with `image` has that image as its
default and `none` besides.

A masked layer with no `image` and no `variants` is an **empty slot**:
its only variant is `none`, until art is drawn for it. The `hat` above is
one.

### The base

The layers without a mask, each at its default and untransformed, make up
the **base**: the ghost with no face and nothing worn. It is never stored;
it is derived from the layers whenever the shell loads.

So **draw the body with no face.** The neutral face is the default
variant of the `eyes` and `mouth` layers, and a face painted onto the
body would show through every other one, and through `none`. Nothing
refuses a body with a face on it.

