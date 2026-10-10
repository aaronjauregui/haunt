# Configuration

`~/.config/haunt/config.toml` (`XDG_CONFIG_HOME` respected;
`HAUNT_CONFIG_DIR` overrides the directory). Nothing in it is required:
a missing file is the shipping configuration, and every value below shows
its default. Unknown sections are ignored, not refused. It is read once,
at startup, so a change applies at the next `haunt run` or
`systemctl --user restart haunt`.

```toml
[quiet]
hours = []                      # "HH:MM-HH:MM" windows; overnight allowed, "22:00-08:00"

[animation]
reduced_motion = false          # stop idle animation and the face at rest
expression_inference = "lexicon"   # "lexicon": a face from the words of a scripted line; "off": leave the face alone

[appearance]                    # how large ghosts are drawn; see below
scale = 1.0                     # a factor on every ghost's own size

# [appearance.ghost.wisp]       # one ghost, by its [ghost] name; replaces the factor above
# scale = 0.8

[awareness]                     # what a ghost with `window` may notice
ignore = ["1password", "bitwarden", "keepass", "gnome-keyring", "seahorse",
          "polkit", "pinentry", "gnome-disks", "banking"]
                                # never reported; case-insensitive substring match on the app
dwell = "25m"                   # how long in one window before it is worth a remark
away = "90m"                    # a gap this long counts as having been away
late_hour = 23                  # after this hour, still working is itself notable; negative switches it off

[curiosity]                     # the person's dial, never the ghost's
actions_per_day = 12            # unprompted actions per ghost, per rolling 24 hours; 0 disables initiative
# actions_per_day_total = 18    # across every resident ghost; default grows sub-linearly with the room
obligations_per_day = 24        # things you put on its books (reminders, occasions); a backstop, not a dial

[backend]                       # the models the driver calls itself; conversation is [hermes]
tool_timeout = 120              # seconds one tool-server call may take
log_prompts = false             # dump vision questions and image parameters to the log (HAUNT_BACKEND_DEBUG=1 does the same)

[backend.image]                 # optional: lets a ghost with `imagine` draw
backend = "openai"              # "openai": any /images/generations endpoint; "comfyui": the workflow API
base_url = ""
model = ""                      # model name, or a ComfyUI checkpoint filename
api_key_env = ""                # the variable holding the key; named, never stored here
size = ""                       # "512x512"; backend default when empty
steps = 0                       # ComfyUI tuning, defaults when zero
cfg = 0.0
sampler = ""
negative = ""
workflow = ""                   # ComfyUI only: a graph of your own; see below

[backend.vision]                # optional: lets a ghost with `screen` look, and one with `files` read pictures
base_url = ""                   # OpenAI-compatible, e.g. "http://localhost:11434/v1"; empty disables looking
model = ""                      # must be vision-capable
api_key_env = ""

[backend.search]                # optional: lets a ghost with `web` search and read pages
backend = ""                    # "searxng": your own instance; "brave": the Brave Search API
base_url = ""                   # the SearXNG instance, e.g. "http://127.0.0.1:8888"; Brave ignores it
api_key_env = ""                # Brave only; the variable holding the key
results = 5                     # results a search returns, 1 to 10
safesearch = "moderate"         # "off", "moderate" or "strict"
unprompted = "skills"           # cloud only: when a ghost may search on a turn you did not start: "never", "skills", "any"

[backend.budget]                # per hour, shared across every resident ghost
image_requests_per_hour = 12
vision_requests_per_hour = 60
tool_requests_per_hour = 60
search_requests_per_hour = 30       # searches and pages read together
unprompted_search_per_hour = 4      # the slice of that a ghost may spend unwatched, local or cloud
unprompted_file_per_hour = 4        # reads of shared folders on turns you did not start; yours are not rationed

[files]                         # what a ghost with `files` may read; see below
unprompted = "never"            # cloud only: when a ghost may read on a turn you did not start: "never", "skills", "any"

[files.roots]                   # name = folder; the name is all a ghost ever sees. None by default
# notes   = "~/notes"
# recipes = "~/Documents/recipes"

[model]                         # the model persona ghosts think with; see below
base_url = ""                   # OpenAI-compatible, up to its version: "http://127.0.0.1:8080/v1" for llama-server
name = ""                       # the model as the server knows it, e.g. "glm-5.3"
api_key_env = ""                # the variable holding the key; named, never stored here. Empty sends none
context_length = 0              # the model's context window in tokens
trust = ""                      # "local" or "cloud"; empty judges by base_url's host
# fold = false                  # the words in the first call; default false for local, true for cloud
thinking = ""                   # "off", "low", "medium" or "high"; empty: the provider's default
# first_token_timeout = 120     # seconds a call may go without a word; default 120 local, 30 cloud
stall_timeout = 120             # seconds a call under way may then go quiet

[model.sampling]                # each unset key is the provider's default
# temperature = 0.8
# top_p = 0.95
# min_p = 0.05                  # this and the rest are llama.cpp's; other providers ignore them
# repeat_penalty = 1.0
# dry_multiplier = 0.8
# xtc_probability = 0.0

# [model.ghost.wisp]            # one ghost, by its [ghost] name; any key above, replacing the troupe's
# name = "gemma-4-31b"
# [model.ghost.wisp.sampling]   # replaces the troupe's sampling key by key
# temperature = 1.1

[hermes]                        # the brain; see hermes.md
command = ["hermes"]
home = ""                       # $HERMES_HOME, else ~/.hermes
tool_timeout = 600              # seconds a hermes tool call (an `ask`) may wait
first_token_timeout = 30        # seconds of silence before a turn is given up on
stall_timeout = 120             # seconds a turn under way may then go quiet, the ghost's own tools aside

[hermes.model]                  # written into every ghost's profile when present
# provider = "zai"
# name = "glm-5.3"
# base_url = ""
# context_length = 0            # tell hermes the window when a server under-reports it

[log]
level = "info"                  # "error", "warn", "info" or "debug" (every topic)
debug = []                      # topics whose debug output is wanted, e.g. ["backend", "wayland"]; "all"
```

Any OpenAI-compatible endpoint works for images and vision: Ollama,
llama.cpp server, vLLM, cloud providers. Cloud keys are named, not
stored: set `api_key_env` and haunt reads that variable from its own
environment.

## The model

`[model]` is one OpenAI-compatible chat completions endpoint: llama.cpp's
`llama-server`, Ollama, vLLM, z.ai, OpenAI or OpenRouter. Every resident
ghost uses it unless its own `[model.ghost.<name>]` table says otherwise;
a key set there replaces the troupe's for that ghost alone, and the
rest carry over. A ghost's package never chooses a model.

Where the model runs decides how far it is trusted. A `base_url` on this
machine or the private network (`localhost`, `127.0.0.1`, `[::1]`,
`10.x`, `172.16-31.x`, `192.168.x`, IPv6 `fd..`) is local; any other host
is cloud. A host name is judged by its text, never by what it resolves
to, so a model on your own network reached by name is cloud until
`trust = "local"` says otherwise. `trust = "cloud"` treats a local server
as cloud.

A local model reads the web and your shared folders without being asked
for the capability and on turns you did not start, within quiet hours
and the unprompted rations; a cloud one is asked, and follows the
`unprompted` policies below. Telling another ghost and using a tool
server are asked about the same way on both. [What it may
do](permissions.md#where-the-model-runs) has the whole table.

`fold` decides how a ghost's turn is shaped. Unfolded, the model first
decides what to do and then writes the words in a second call; folded,
it writes them in the first. A second call costs a local server little
and a cloud provider a round trip, hence the defaults.

`thinking` caps the model's reasoning. When a call has it off, haunt
sends both `reasoning_effort` and `chat_template_kwargs.enable_thinking =
false`, since providers differ on which they read; a provider ignores
the one it does not know. The sampling keys are sent only when set.

A call the provider refuses with 429 or a 5xx is tried three times in
all, a second and then two apart, or after the provider's own
`Retry-After` when that is under a minute. A call that says nothing for
`first_token_timeout` seconds, or stops for `stall_timeout` once under
way, is given up on. Unset, the first is 120 seconds for a local model,
which reads a long conversation at a few hundred tokens a second before
its first word, and 30 for a cloud one.

## Drawing with your own ComfyUI workflow

`[backend.image] workflow` points at a graph you built in ComfyUI and
exported with **Save (API format)** — the API export, not the one that
saves the canvas. Haunt submits that graph for every picture, with the
parts that vary substituted into it:

| In the graph | Becomes |
|---|---|
| `{{prompt}}` | what the ghost is drawing, its style already folded in. Required, at least once. |
| `{{negative}}` | `negative` above, joined with whatever the ghost's package asks to keep out. |
| `{{seed}}` | the seed the picture is painted from, recorded with it. A graph that leaves this out gets its own seed each time, and the ghost cannot then paint a variation of an earlier picture. |
| `{{width}}`, `{{height}}` | `size`, or 1024 when it is empty. |

`{{prompt}}` and `{{negative}}` are substituted inside a string, so
`"ugly, {{negative}}"` works. `{{seed}}`, `{{width}}` and `{{height}}`
must be the whole value, because they become numbers.

A minimal SDXL graph, with only the nodes that matter shown:

```json
{
  "4": {"class_type": "CheckpointLoaderSimple",
        "inputs": {"ckpt_name": "sd_xl_base_1.0.safetensors"}},
  "6": {"class_type": "CLIPTextEncode",
        "inputs": {"clip": ["4", 1], "text": "{{prompt}}"}},
  "7": {"class_type": "CLIPTextEncode",
        "inputs": {"clip": ["4", 1], "text": "{{negative}}"}},
  "5": {"class_type": "EmptyLatentImage",
        "inputs": {"width": "{{width}}", "height": "{{height}}", "batch_size": 1}},
  "3": {"class_type": "KSampler",
        "inputs": {"model": ["4", 0], "positive": ["6", 0], "negative": ["7", 0],
                   "latent_image": ["5", 0], "seed": "{{seed}}", "steps": 25,
                   "cfg": 7.0, "sampler_name": "dpmpp_2m", "scheduler": "karras",
                   "denoise": 1.0}},
  "8": {"class_type": "VAEDecode", "inputs": {"samples": ["3", 0], "vae": ["4", 2]}},
  "9": {"class_type": "SaveImage", "inputs": {"images": ["8", 0], "filename_prefix": "haunt"}}
}
```

The graph owns how the picture is made, so `model`, `steps`, `cfg` and
`sampler` are ignored when a workflow is set; haunt says so at startup if
you have set them anyway. `size` and `negative` still apply, through the
placeholders. The graph needs at least one `SaveImage` or `PreviewImage`
node — that is where haunt collects the picture — and if it has several,
the lowest-numbered one wins.

The file is read and checked when haunt starts. A graph that is the wrong
export, has nowhere to put the prompt, or hands back no picture is an
error naming the file, rather than a ghost that says it cannot draw.

**Prompts suit the model.** SD 1.5 and SDXL want comma-separated tags
(`a whale, underwater, soft light, wide shot`); Flux and the newer models
want a sentence. That is a property of the checkpoint you chose, so it
belongs here rather than in a ghost: a ghost's `[imagine] style` is
appended to whatever it wrote, and writing that style in the shape your
model likes is what makes the two read as one prompt.

## Searching the web

`[backend.search]` is what a ghost that declares the `web` capability
searches with. Empty, and there is nothing to search with: the ghost is
told so, and the capability alone grants nothing.

Two providers. A SearXNG instance is the local-first one — your own
machine or your own server, no key, and the queries are yours to see:

```toml
[backend.search]
backend  = "searxng"
base_url = "http://127.0.0.1:8888"
results  = 5
```

SearXNG serves HTML only until you ask it for JSON. In its
`settings.yml`:

```yaml
search:
  formats: [html, json]
```

Without it the instance answers a search with 403, and haunt says so.

The Brave Search API needs no server of your own and does need a key.
The endpoint is fixed; name the variable holding the key, and export it
where haunt can read it:

```toml
[backend.search]
backend     = "brave"
api_key_env = "BRAVE_API_KEY"
results     = 5
```

Brave's free tier allows one request a second. A ghost searching twice in
a turn is well inside it; if a burst does hit the limit, the ghost is
told the provider is rate-limiting and says so rather than inventing an
answer.

`unprompted` decides whether a ghost on a cloud model may search on a
turn you did not start. A ghost on a local model searches on any turn
of its own, within the ration and outside quiet hours, whatever this
says ([The model](#the-model)):

| Value | A ghost searches unasked |
|---|---|
| `never` | not at all; it may only look things up when you ask |
| `skills` | on the turns its skills act on — an occasion, a reminder coming due, a quiet goal, waking up — and never on what it sees in your windows |
| `any` | on any turn of its own, within the ration |

`search_requests_per_hour` covers searching and reading pages together.
`unprompted_search_per_hour` is the slice of it a ghost may spend while
you are not asking, local or cloud, and quiet hours stop unprompted
searches outright.

Queries go to the provider you configured; pages are fetched from this
machine. A page is only ever fetched from a public address: a ghost given
a link to something on your own machine or your own network refuses it.
`docs/user/permissions.md` has the rest of what reading the web means.

## Sharing folders

`[files.roots]` is the folders a ghost that declares the `files`
capability may read. Empty, and nothing is shared: the ghost is told so,
and the capability alone grants nothing.

```toml
[files]
unprompted = "never"

[files.roots]
notes   = "~/notes"
recipes = "~/Documents/recipes"
```

Each key is the folder's name, and it is the only thing a ghost sees: it
reads `notes/2026/todo.md`, never the path on your disk. A name is 1 to
32 lowercase letters, digits, `-` or `_`, starting with a letter or
digit. A folder is an absolute path or starts with `~/`. A bad name, a
relative path or a repeated name is an error at startup naming the
config file. A folder that is missing, such as a drive that is not
mounted, is a warning in the log, not an error: the ghost sees it as
unavailable, and it is tried again the next time it is asked for.

Share specific folders, not your home. A ghost reads anything inside a
shared folder, hidden files included, and a search stops after 5,000
files, so a search of `~` would hit that limit every time and find less
than a search of `~/notes`.

`unprompted` decides whether a ghost on a cloud model may read on a
turn you did not start. The values mean what they mean for
`[backend.search]`, and the default is `never`, because your files are
more personal than the web. A ghost on a local model reads on any turn
of its own, within the ration and outside quiet hours, whatever this
says:

| Value | A ghost reads your files unasked |
|---|---|
| `never` | not at all; only when you ask |
| `skills` | on the turns its skills act on — an occasion, a reminder coming due, a quiet goal, waking up |
| `any` | on any turn of its own, within the ration |

Reads you ask for are not rationed. `unprompted_file_per_hour` is how
many a ghost may make unasked, local or cloud, and quiet hours stop them
outright. A
picture is described by the `[backend.vision]` model and spends from
`vision_requests_per_hour` as well; with no vision model, pictures cannot
be read.

The sizes are fixed, not settings. They keep a huge log or a hostile
picture from hurting haunt, and they are what a ghost runs into:

| Limit | Value |
|---|---|
| a piece of a text file or a PDF's text, per read | 1,500 bytes |
| what a file's kind is judged from | its first 8 KiB |
| a PDF | 50 MB and 300 pages, its text extracted within 10 s |
| a PDF's text, kept for paging on | 10 minutes |
| a picture | 20 MB and 40 megapixels |
| a picture's long edge, as the vision model sees it | 1568 px |
| a search | 5,000 files, 50 MB read, 100 matches, 5 s |
| a file whose contents a search skips | over 5 MB (its name still matches) |
| a matched line, as shown | 160 characters |
| a folder listing | 200 entries, then how many more |

A search that stops at a limit says which one, and the ghost searches a
narrower folder rather than deciding there is nothing. Search matches
the words as written, ignoring case, and skips `.git` and
`node_modules`. PDFs and pictures match by name only; a scanned PDF with
no text layer has nothing to read. `docs/user/permissions.md` has the
rest of what sharing a folder means.

## A ghost's size

Each ghost's shell sets the size it is drawn at
([The shell](../ghosts/shell.md#scale)); `[appearance]` adjusts that for
your screen. `scale` is a factor on every ghost's own size: `0.5` draws
them all at half. An `[appearance.ghost.<name>]` table sets one ghost's
factor in place of that one, keyed by the `name` in its manifest; quote a
name with anything but letters, digits, `-` and `_` in it:

```toml
[appearance.ghost."my ghost"]
scale = 1.5
```

A factor must be above 0; zero or less is refused when the config loads.
A ghost is never drawn at more than four times its art's own pixel size,
whatever the factor, and the log says when a factor was cut short. A
ghost keeps its place when its size changes: its top-left corner stays
where it was left.

## Environment

| Variable | Effect |
|---|---|
| `HAUNT_CONFIG_DIR` | directory holding `config.toml` |
| `HAUNT_RUNTIME_DIR` | where control sockets go (default under `XDG_RUNTIME_DIR`) |
| `HAUNT_LOG` | log level, overriding the config |
| `HAUNT_DEBUG` | comma-separated debug topics, overriding the config |
| `HAUNT_BACKEND_DEBUG` | `1` logs backend requests |
| `HERMES_HOME` | hermes root when `[hermes] home` is unset |
| `HERMES_IMAGE` | image tag for `scripts/hermes-docker` |
| `HAUNT_HERMES_ENV` | keys file for the container wrapper (default `~/.config/haunt/hermes.env`) |

A misspelled level or topic is reported at startup rather than silently
ignored. The log topics are listed by `go doc ./internal/hlog Topics`.
