# The brain: hermes-agent

A ghost with no brain of its own runs on
[hermes-agent](https://github.com/nousresearch/hermes-agent). Hermes is the
judgment: it keeps the conversation, remembers what matters about you,
decides whether an event is worth a word and what to say. Haunt is the
body: the screen, the balloon, consent, budgets, quiet hours, and the
reminders the ghost has promised to keep.

Each ghost gets a hermes profile of its own, `haunt-<ghost>` under your
hermes home, and its own gateway process, started and stopped with the
ghost. The profile is a normal hermes profile: `hermes -p haunt-wisp`
opens the same one the ghost runs on.

The gateway takes a few seconds to start, longer in its container, and
the greeting is a model turn after that, so a ghost can be on screen for
half a minute before it says anything. Meanwhile its balloon says
"waking up…". Its greeting replaces that; with no greeting (a restart it
lets pass quietly), the balloon clears when the ghost is ready. A ghost
that could not start says so in its balloon.

Anything said to it before then is kept and answered after the greeting.
It is never lost to a start that is merely slow: the ghost gives up only
if the gateway is not up within two minutes.

While it works on an answer its balloon says "thinking…", until the first
words arrive; wearing a face or looking something up along the way does
not take that down. A tool with a wait of its own ("painting…") shows its
own words while it runs.

## Setting it up

1. **Install hermes.** Either natively:

       uv pip install -e 'hermes-agent[mcp]' aiohttp     # Python 3.11 to 3.13

   or in its container, through the wrapper in `scripts/hermes-docker`
   (below).

2. **Give the ghost a model.** Per profile, the hermes way:

       hermes -p haunt-wisp model

   or for every ghost at once, in `~/.config/haunt/config.toml`:

   ```toml
   [hermes.model]
   provider = "zai"
   name = "glm-5.3"
   ```

   The model needs a context window of at least 64K. Hermes refuses
   smaller ones, and small local models follow the ghost's rules badly
   when forced. Provider keys stay hermes's business: its `.env`, or the
   environment.

3. **Declare `llm`** in the ghost's capabilities and grant it. The demo
   ghost wisp already does.

## What hermes may use

The profile's toolset is fixed by haunt, not by the model: hermes memory,
session search, skills, and haunt's own verbs offered as tools through
`haunt mcp-serve` (speak, ask, wear a face, draw, look, hold a reminder,
tell another ghost, and whatever tool servers the manifest declares). No
shell, no files, no browser, no web. Every verb goes through the same
consent, budgets and quiet rules as anything else the ghost does.

What hermes holds about you is in the profile, in plain text:
`memories/MEMORY.md` and `memories/USER.md`.

## Hermes in a container

`scripts/hermes-docker` stands in for the `hermes` executable and runs the
hermes image with only what a ghost's brain needs mounted: the ghost's
profile, the haunt binary read-only, and the control-socket directory.

Build the image from the commit haunt is checked against (hermes's tags
are date-based):

```sh
git clone https://github.com/nousresearch/hermes-agent && cd hermes-agent
git checkout 6005aa1
docker build -t hermes-agent:6005aa1 .
```

A 403 from ghcr.io during the build is a stale `docker login ghcr.io`.

Provider keys go in `~/.config/haunt/hermes.env` (mode 0600), which the
wrapper passes to the container as environment, so no profile ever holds
one:

    GLM_API_KEY=...

Then point haunt at the wrapper:

```toml
[hermes]
command = ["/abs/path/to/haunt/scripts/hermes-docker"]
```

`HERMES_IMAGE` picks a different image tag. The container uses host
networking; the gateway binds loopback only.

A Claude subscription is not a way in: hermes's Anthropic OAuth path bills
purchased extra-usage credits only. `ANTHROPIC_API_KEY` is pay-per-token.

## Watching it think

`haunt history -why` shows what the ghost said and why: what provoked
each turn, what it looked up and which tools it used
([Memory and history](memory.md)). `make demo-ai` runs wisp on your model
in a window of its own, with a hermes profile in the demo's workdir that
is deleted with it.

Hermes's own logs are in the profile's `logs/`. `HAUNT_LOG=debug` puts the
gateway's output under haunt's `brain` log topic.

## Settings

All under `[hermes]` in the config ([Configuration](config.md)):

| Key | Default | Meaning |
|---|---|---|
| `command` | `["hermes"]` | the hermes executable, or the container wrapper |
| `home` | `$HERMES_HOME`, else `~/.hermes` | the hermes root the profiles live under |
| `model` | unset | written into every profile; unset keeps each profile's own |
| `tool_timeout` | 600 | seconds one tool call may take, which is how long an `ask` may wait on you |
| `first_token_timeout` | 30 | seconds of silence before a turn is given up on |
