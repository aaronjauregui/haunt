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

[backend.vision]                # optional: lets a ghost with `screen` look
base_url = ""                   # OpenAI-compatible, e.g. "http://localhost:11434/v1"; empty disables looking
model = ""                      # must be vision-capable
api_key_env = ""

[backend.budget]                # per hour, shared across every resident ghost
image_requests_per_hour = 12
vision_requests_per_hour = 60
tool_requests_per_hour = 60

[hermes]                        # the brain; see hermes.md
command = ["hermes"]
home = ""                       # $HERMES_HOME, else ~/.hermes
tool_timeout = 600              # seconds a hermes tool call (an `ask`) may wait
first_token_timeout = 30        # seconds of silence before a turn is given up on

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
