# The model

A ghost with no brain of its own thinks with your model. The model is
the judgment: whether an event is worth a word, what to say, what to
remember. Haunt is the body: the screen, the balloon, consent, budgets,
quiet hours, the reminders the ghost has promised to keep, and the
conversation and memory it keeps for the model between turns.

You pick the model, in `~/.config/haunt/config.toml`. A ghost's package
never names one. Haunt talks to it directly over the OpenAI-compatible
chat completions API, which llama.cpp's `llama-server`, Ollama, vLLM,
z.ai, OpenAI and OpenRouter all speak. Nothing else needs installing.

## Setting it up

1. **Name the model** under `[model]`:

   ```toml
   [model]
   base_url = "http://127.0.0.1:8080/v1"
   name = "gemma-4-31b"
   context_length = 32768
   ```

   `base_url` is the endpoint up to and including its version;
   `/chat/completions` is added to it. `name` is the model as the server
   knows it. `context_length` is its context window, which decides when
   older turns are summarised; unset, 32K is assumed. A window of 32K or
   more is comfortable.

2. **Declare `llm`** in the ghost's capabilities and grant it. The demo
   ghost wisp already does. Without the grant, the ghost asks for it in
   its balloon the first time you talk to it.

Every resident ghost uses `[model]`. One ghost can have its own, by its
`[ghost] name`; each key set there replaces the troupe's for that ghost
alone:

```toml
[model.ghost.wisp]
name = "glm-5.3"
base_url = "https://api.z.ai/api/paas/v4"
api_key_env = "ZAI_API_KEY"
```

## A local model

`llama-server` from llama.cpp, with the model's chat template on (the
default) and a context of at least 32K:

    llama-server -m gemma-4-31b-it-qat-q4_0.gguf -c 32768 --port 8080

One server is shared by every ghost; each ghost's character comes from
its persona, its examples, its prefill and its sampling, all of which
are per request. The llama.cpp sampling keys (`min_p`, `repeat_penalty`,
`dry_multiplier`, `xtc_probability`) go under `[model.sampling]`, or a
ghost's own `[model.ghost.<name>.sampling]`; other providers ignore
them.

A local model reads a long conversation at a few hundred tokens a second
before its first word, so the first turn after a start can take a minute.
The server keeps the prompt in its cache between turns, and later turns
start in a second or two.

A turn's two calls ([How a turn goes](#how-a-turn-goes)) have different
prompts: the deciding call declares the ghost's tools and the call that
writes the words does not. On one server slot each call pushes the
other's prompt out of the cache, and every turn reads most of its prompt
again. `slots = [0, 1]` under `[model]` pins the calls with tools to
slot 0 and the rest (the words, the summary of older turns) to slot 1,
so each finds its own prompt where it left it. The server must give
each slot its own cache: start it with `-np 2 --no-kv-unified`. With
the default (slots chosen by the server, one unified cache shared
between them) a call on one slot evicts what the other slot kept, and
pinning gains nothing. Separate slots split the context between them
(`-c 32768 -np 2` gives each 16384), so set `context_length` to a
slot's share, not the whole. The pair is sent as llama-server's
`id_slot`, so set it only for llama-server; unset, no slot is named and
the server picks.

## A cloud model

A key is never in the config. Name the environment variable that holds
it, and export it where haunt runs:

```toml
[model]
base_url = "https://api.z.ai/api/paas/v4"
name = "glm-5.3"
api_key_env = "ZAI_API_KEY"
context_length = 131072
```

Under the systemd unit, the variable has to be in the unit's environment:
a line in `~/.config/environment.d/haunt.conf` (`ZAI_API_KEY=…`, mode
0600) reaches every user service and the desktop session from the next
login, and `systemctl --user set-environment ZAI_API_KEY=…` reaches the
running manager now; an `Environment=` line in a drop-in works too. An empty `api_key_env` sends no key,
which is what a local server wants.

## Where it runs decides what it may do

A model on this machine or your private network is **local**: a
`base_url` of `localhost`, `127.0.0.1`, `[::1]`, `10.x`, `172.16-31.x`,
`192.168.x` or an IPv6 `fd..` address. Any other host is **cloud**. A
host name is judged by its text, never by what it resolves to, so a
model on your network reached by name is cloud until you say
`trust = "local"`; `trust = "cloud"` treats a local server as cloud.

On a local model, nothing the ghost reads leaves the machine through
the model, so reading is loose. A ghost whose package declared `web` or
`files` reads with no question asked, on your turns and on its own,
within quiet hours and the hourly ration; a "never ask again" you gave
still holds, but `haunt perms revoke` does not stop it. What it keeps (a
remembered line, its notes, a skill it writes) is labelled with where
the words came from.

On a cloud model the capability is asked for the first time, reading on
its own follows `[backend.search] unprompted` and `[files] unprompted`,
and after a read, taking something on, settling it or changing its
drawing style is put to you, as is searching the web after reading a
file. What it keeps is labelled too, and refused when its words repeat
what it read this turn.

On both, telling another ghost and using a tool server after a read are
put to you, and nothing can run a command or write a file. The ghost is
told these rules in its own prompt, written from the same table the
driver enforces. [What it may do](permissions.md#where-the-model-runs)
has the rest.

## How a turn goes

A turn is two calls by default on a local model: one in which the model
decides what to do (look something up, take on a reminder, wear a face,
say something or nothing), and one in which it writes the words, with
thinking off and the ghost's prefill. On a cloud model the words come in
the first call, saving a round trip: the model writes its words and
names the face to wear with them in one reply. `fold` in `[model]` says
which: `true` for one call, `false` for two.

`thinking` caps the model's reasoning: `off`, `low`, `medium` or `high`,
or unset for the provider's default. The deciding call thinks on your
turns and not on events, which mostly end in silence; the words are
always written with thinking off. On a local model, set `thinking =
"off"`: Gemma 4 31B answers you in about a third of the time without
it, and haunt's skills and decisions are built to be followed step by
step without reasoning.

## Waiting and failing

A ghost on screen says "waking up…" until it has said hello, and
"thinking…" while it works on an answer to you, until its first words
arrive. Anything you say meanwhile is kept and answered in turn. A
picture that takes a while to paint shows its own words while it runs.

A provider that answers 429 or 5xx is tried three times, a second and
then two apart, or after the wait it asks for when that is under a
minute. A call that says nothing for `first_token_timeout` seconds (120
local, 90 cloud), or goes quiet for `stall_timeout` (120) once under way,
is given up on. A turn that fails says why in the balloon ("the model
said nothing for 2m0s — is it running?"), `haunt history` marks it
`(the turn failed)`, and anything you said meanwhile goes to the model
next. A ghost with no `[model] base_url` cannot think, and says so
whenever you talk to it.

## Watching it think

`haunt history -why` shows what the ghost said and why: what provoked
each turn, what it looked up and which tools it used
([Memory and history](memory.md)). `HAUNT_DEBUG=brain` logs each call's
token counts, how many of them the server had cached, and a line at the
end of every turn with how long it took and the face it wore. `make
demo-ai` runs wisp on your `[model]` in a window of its own, with its
memory and transcript in a throwaway directory.

## Settings

All under `[model]` ([Configuration](config.md)):

| Key | Default | Meaning |
|---|---|---|
| `base_url` | none | the endpoint, up to and including its version |
| `name` | none | the model as the server knows it |
| `api_key_env` | none | the variable holding the key; none sends no key |
| `context_length` | 32768 | the context window in tokens |
| `trust` | by `base_url` | `local` or `cloud` |
| `fold` | `false` local, `true` cloud | the words in the deciding call |
| `thinking` | the provider's | `off`, `low`, `medium`, `high` |
| `first_token_timeout` | 120 local, 90 cloud | seconds a call may go without a sign of life |
| `stall_timeout` | 120 | seconds a call under way may go quiet |
| `slots` | none | llama-server only: `[decide, words]` slot numbers, such as `[0, 1]`, two different numbers from 0 |
| `[model.sampling]` | the provider's | `temperature`, `top_p`, `min_p`, `repeat_penalty`, `dry_multiplier`, `xtc_probability` |
| `[model.ghost.<name>]` | none | any key above, for one ghost |

A ghost's own `[persona.sampling]` is laid over these, key by key.
