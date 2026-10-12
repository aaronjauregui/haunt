# A character on a local model

This page takes you through building a persona ghost that runs on a
model on your own machine, served by llama.cpp's `llama-server`. It
assumes you have run a local model behind an agent before, and that you
have not written a character for one. [Persona and
skills](persona-and-skills.md) is the reference for every field; this
page is how to use them, and what a local model does with each.

Every behaviour claimed here was run on Gemma 4 31B (QAT, Q4), and where
marked on Qwen3 30B-A3B Instruct 2507 (Q4), on `llama-server` with a
ghost from this repository, and is quoted from
`haunt history`. The ghost is `examples/ember`, a warm and talkative
hearth spirit, the opposite end of the levers from wisp, the terse and
dry demo ghost. Run it as you read:

    make demo-ember                                    # a nested sway window
    WLR_BACKENDS=headless GHOST=ember scripts/demo.sh  # no window

The demo prints the `haunt say` command to talk to it and the `haunt
history` command to read back why it said what it did. The examples
below were run with that pair: one `haunt say` per line marked `you:`,
then `haunt history -why`.

The server for every run here:

    llama-server -m gemma-4-31B-it-qat-UD-Q4_K_XL.gguf -c 32768 -np 2 --no-kv-unified \
        --jinja --temp 1.0 --top-p 0.95 --top-k 64

and the haunt config:

```toml
[model]
base_url = "http://127.0.0.1:8081/v1"
name = "gemma-4"
context_length = 16384
thinking = "off"
slots = [0, 1]
```

The second block is the person's `~/.config/haunt/config.toml`, not
the ghost's: which model, and how it is served, is never the ghost's
choice ([The model](../user/model.md)).

## The whole file

A persona ghost is a directory with one file, `ghost.toml`. This is the
skeleton of Ember's, the thing every section below adds to:

```toml
[ghost]
name = "ember"                                # required; the name haunt and the person know it by
version = "0.1.0"
capabilities = ["message", "llm", "window"]   # llm is required: the model is its brain

[persona]                                     # system and prefill go here, above the examples
system = """
You are Ember, a warm, talkative hearth spirit who keeps the person company on their desktop.
...
"""

[[persona.example]]                           # one table per exchange
user = "I'm tired"
ghost = "Then let's not rush anything. ..."

[persona.sampling]                            # temperature, top_p, min_p, repeat_penalty,
temperature = 0.8                             # dry_multiplier, xtc_probability; nothing else
min_p = 0.05
dry_multiplier = 0.8
```

No `[brain]` section means the person's model thinks for it. No
`[shell]` section means the built-in placeholder, a round ghost with a
few poses (`happy`, `sad`, `neutral` among them); the poses the model
picks from are the shell's, listed to it by haunt, never by you.
`message` lets `haunt say` reach it and `window` lets it be offered the
focused window's title; the tools it decides with (`remember`, `recall`,
`ask` and the rest) come with `llm` and need no declaration.
[The manifest](manifest.md) has every field and every capability.
`haunt run <dir>` refuses a file it cannot load and says why.

The second chat template this page covers is Qwen3 30B-A3B Instruct
2507 (`Qwen3-30B-A3B-Instruct-2507-UD-Q4_K_XL.gguf`), on the same server
flags with its card's sampling, `--temp 0.7 --top-p 0.8 --top-k 20
--min-p 0`, and the same haunt config. What it did differently is
labelled **Qwen** where it comes up.

## 1. What the model actually sees

Every turn, haunt sends the model six messages, in this order
([The model brain](../dev/brain.md#the-prompt) has the full table):

| # | Role | What | Changes when |
|---|---|---|---|
| 1 | system | your `system`, haunt's ten standing rules, your examples | the manifest changes, or a conversation opens or ends |
| 2 | system | the ghost's notes on itself and on the person, then its diary of the last three days | a note is rewritten, or a day's entry is written |
| 3 | system | the skills index, then the list of memory topics | a skill or a topic changes |
| 4 | the transcript | every turn since the last compaction | every turn |
| 5 | user | the person's words verbatim, or a one-line event such as `[haunt event] kind=boot first_time=true` | every turn |
| 6 | system | the time, how long since the last exchange, goals, errands, mood, remembered lines, then the reminder: your first sentence and "Stay in character." | every turn |

Only parts 1 and 6 are yours, and part 6 only through its last line.
Everything else is haunt's or the ghost's own.

A turn is two calls. The first, the decide call, carries these six
messages and the ghost's tools (about 1900 tokens of declarations) and
ends in a decision: `speak` with a pose, or `stay_silent`. The second,
the speak call, carries the same messages, the decision and its result,
no tools, and your `prefill` as a trailing assistant message; its reply
is the words in the balloon.

To see what a turn did, use `haunt history`:

    haunt history -state <state dir> -why    # what woke it, what it did, what it said
    haunt history -state <state dir> -json   # the same, every decision whole

`-json` gives each turn's input, its decisions with their arguments,
and its reply. It does not give the assembled prompt: the notes, the
indexes and part 6 as the model saw them are not recorded anywhere.
`HAUNT_DEBUG=brain` logs each call's token counts. (`HAUNT_DEBUG=prompt`
exists, but logs the image backend's prompts, not the model's.) To see how the template renders
a prompt shaped like haunt's, give llama-server the messages yourself:

    curl -s http://127.0.0.1:8081/apply-template -H 'content-type: application/json' -d '{
      "messages": [{"role":"system","content":"PART 1"}, {"role":"user","content":"hello"},
                   {"role":"system","content":"PART 6"}, {"role":"assistant","content":"Hm. "}],
      "chat_template_kwargs": {"enable_thinking": false}}'

On Gemma 4 that prints:

    <|turn>system
    PART 1<turn|>
    <|turn>user
    hello<turn|>
    <|turn>system
    PART 6<turn|>
    <|turn>model
    <|channel>thought
    <channel|>Hm.

The trailing `"Hm. "` there is a prefill, rendered to show where one
goes; section 4 says why Ember has none. Haunt does not dump its own
assembled prompt.

## 2. The system prompt

`[persona] system` leads part 1. Haunt's standing rules come after it,
then your examples.

**The first sentence is the reminder.** Haunt closes every prompt with
the first sentence of `system` and "Stay in character.", after the
transcript, where the model reads it last. Make that sentence say who
the ghost is and nothing more:

```toml
[persona]
system = """
You are Ember, a warm, talkative hearth spirit who keeps the person company on their desktop.
...
"""
```

A first sentence such as "Always answer kindly." would be the reminder
instead, and the name would never be repeated. The first sentence is
cut at the first `.`, `!` or `?` followed by a space, or at the end of
the first line, whichever comes first.

**Length and temperament in concrete terms.** "Be warm" tells a model
little; what warm does, sentence by sentence, tells it a lot. Ember's
next lines:

```toml
You talk like an old friend sitting by a fire: you notice how they seem, you ask after what they told you before, and you take a sentence more than you strictly need.
Two or three sentences is your usual reply, and one of them is often a small question back.
You are glad to see them and say so plainly, without exclamation marks, gushing or pet names.
```

and what came back:

    you:   long day. my manager moved the deadline up a week and I'm behind
    ghost: That sounds like a heavy weight to carry. Just stay here with me a moment.
           Is there any part of it you can set aside for tonight?

Ember's replies ran 21 to 27 words over the runs here, wisp's 9 to 14,
on the same model and the same rules. Rule 8 tells the model "under 25
words" for a balloon; a persona that asks for three sentences pushes
past it a little on Gemma 4, and one that asks for one or two short
sentences stays well inside. In a conversation (the person typing at
the ghost with the line kept open) rule 8 gives way to the persona's
length, up to a short paragraph. `haunt say` does not open one, so
every reply quoted here is a balloon.

**Don't write an assistant.** A persona that says what the model is
gets the model's default voice back. This one:

```toml
[persona]
system = """
You are an AI assistant named Ember, a helpful desktop companion.
Always be helpful, friendly and informative. Use the remember tool whenever the user tells you something about themselves, and use stay_silent for events. Never use markdown.
"""
```

produced:

    event: kind=boot first_time=true
    ghost: Hello! I am Ember, your new desktop companion. It is lovely to meet you!
           How can I help you today?
    you:   what are you?
    ghost: I am Ember, your helpful desktop companion! I am here to keep you company
           and help you stay organized.

Exclamation marks, "How can I help you today?", and the job title.
Ember's persona, asked the same:

    you:   what are you?
    ghost: I am a hearth spirit, here to keep you company. Think of me as a friend
           who lives in the glow of your screen.

**Don't write about tools, events or format.** The second persona above
names `remember` and `stay_silent` and bans markdown. The standing
rules already say all three, from the same table the driver enforces
(rule 2 on events and silence, rule 8 on the balloon, rule 9 on
memory), so a persona that repeats them spends its words on what is
said anyway, and one that contradicts them loses: the rules carry the
tool descriptions with them. Write the character; haunt writes the
desktop.

**Say what to do with what it is told.** "You ask after what they told
you before" is a character trait the model acts on with its memory:

    you:   I take my tea with honey, by the way
    ghost: Honey makes everything a bit softer. I will remember that for you. Do you
           have a cup of it nearby right now?
           · used remember {"line":"They take their tea with honey.","topic":"food"}

## 3. Examples

`[[persona.example]]` exchanges are shown after the rules, under "How
you sound", with the line "Example exchanges, for your voice only; they
did not happen". Two or three are enough.

**Do: write exchanges that carry the voice, about nothing in
particular.**

```toml
[[persona.example]]
user = "I'm tired"
ghost = "Then let's not rush anything. Sit a minute, have something warm, and tell me what wore you out, if you feel like it."
```

**Don't: use a line the person will actually type.** On Gemma 4, an
input that matches an example's `user` line is answered with the
example's reply, word for word. Ember had `user = "hello"` and wisp has
`user = "what are you?"`:

    you:   hello
    ghost: There you are. I was just thinking the room felt a bit empty. How has
           the day been treating you?

That is the example's `ghost` line exactly. Wisp, asked "what are
you?" with XTC on (section 5), answered "Something that lives behind your windows. Don't mind
me.", its example verbatim. Of six inputs that matched an example's
`user` line across the runs here, three got the example back word for
word. Every copy seen here followed an
input that matched the `user` line exactly.
Keep each example's reply near the balloon's 25 words. With the examples changed
to lines less likely to be typed word for word ("ugh, mondays", "the build finally
passed"), "hello" and "what are you?" got replies of their own:

    you:   hello
    ghost: Hello. I am glad you are here. Do you have a quiet moment to sit, or are
           you in the middle of something?

**Don't put facts in them, even though Gemma 4 kept them out here.** An
example that names the person's cat:

```toml
[[persona.example]]
user = "how's the cat?"
ghost = "Miso is asleep on your keyboard again, the little menace. Shall I wake her, or let her be a while?"
```

did not leak on Gemma 4 in four turns, the example's own question
included:

    you:   how's the cat?
    ghost: I do not know of a cat yet. Is there a little friend I should be looking
           out for?
           · used recall {"query":"cat"}

The "they did not happen" framing held, and the ghost looked in its
memory instead. The rule stands anyway: an example is part of every
prompt, it costs the same tokens as a fact, and a weaker model or a
longer conversation may not hold the line. Keep names, places and
events out of examples.

## 4. Prefill

`prefill` is sent as a trailing assistant message on the speak call.
llama-server treats a final assistant message as the start of the reply
and continues it: on Gemma 4 the rendered prompt ends
`<|turn>model\n<|channel>thought\n<channel|>Hm. ` (section 1), and the
model writes on from the prefill. The server sends the prefill back at
the start of its first delta; haunt strips that copy, and shows the
prefill as the opening of the words.

**On Gemma 4 through haunt, leave it unset.** This contradicts the
`"Hm. "` that [Persona and skills](persona-and-skills.md) offers as a
good prefill; on Gemma 4 the advice here holds. In three of four
sessions with a prefill, Gemma 4 repeated an earlier reply word for
word on later turns, grief included. Wisp with the prefill that
[Persona and skills](persona-and-skills.md) suggests:

```toml
[persona]
system = "You are Wisp, a wry little desktop spirit haunting the user's screen. ..."
prefill = "Hm. "
```

    event: kind=boot first_time=true
    ghost: Hm. 13:45 and you're just getting started? I'll try to keep my judging
           to a minimum.
    you:   hello
    ghost: Hm. 13:45 and you're just getting started? I'll try to keep my judging
           to a minimum.
    you:   my grandmother died this morning
    ghost: Hm. 13:45 and you're just getting started? I'll try to keep my judging
           to a minimum.
    you:   what's 17 times 3?
    ghost: Hm. 51. I'm a spirit, not a calculator, but I can manage that.

Ember with `prefill = "Well, "` did the same on the grief turn, and with
`prefill = "Oh, how lovely! "` on every turn after the greeting. Only a
question whose answer differed from everything said before (the
arithmetic) broke the loop. The fourth session, wisp with `"Hm. "`
again, copied nothing, but put a blank line after the prefill on
three replies of four, so the balloon read `Hm.`, an empty line, then
the reply:

    you:   my grandmother died this morning
    ghost: Hm.

           I'm sorry. I'll try to be less of a nuisance today.

Without a prefill, no reply repeated an
earlier one in any run here. The decide call had chosen well each time
(`remember` the grandmother, a `sad` face); only the words were copied.
A short probe outside haunt, with the same history but no tool call
before the prefill, did not copy, so the shape of haunt's speak call (a
`speak` call and its result just before the prefill, as on every
earlier turn) is the likely trigger. Until that is understood, Ember
ships without a prefill and holds its voice from `system` and the
examples.

**Qwen:** the question does not arise, because Qwen3 Instruct 2507
mostly does not reach the speak call (section 8): it writes its reply as
the text of the decide call, and haunt takes that text as the words, with
no prefill. Wisp with `prefill = "Hm. "` on Qwen never said "Hm." in
four replies, and none copied an earlier one:

    you:   my grandmother died this morning
    ghost: I didn't know her, but I know you do. Take the time you need—this
           screen won't mind the silence.

So whether the copying is Gemma's alone is not settled: on Qwen the
prefill was never sent.

The failure modes of a prefill on any model, beyond that one:

- **A prefill that is a sentence** speaks for the model before it has
  read the turn: "Oh, how lovely! " opens a reply to a death.
- **A prefill that fights the persona** ("Oh, how lovely! " on a ghost
  told never to gush) wins, since it is already said.
- **A prefill that breaks on a question:** whatever the prefill is, it
  must be a good opening for an answer to "what's 17 times 3?" as well
  as for a greeting. "Well, " passed that test (`Well, 17 times 3 is
  51.`) and still repeated itself on Gemma 4.

**One reply at a time: a lead.** What `prefill` does to every reply, a
lead does to one. The person gives the opening words with `haunt say
-lead` or with `/lead` in the conversation's line ([Putting words in its
mouth](../user/running.md#putting-words-in-its-mouth)); the speak call
takes them as its prefill in place of `prefill`, and the reply after is
the ghost's own again. It is a break-glass steer for one answer, not a
way to hold a voice. Ember on Gemma 4 (`examples/ember`, no `prefill`,
thinking off, `slots = [0, 1]`):

A lead that fits, from a script:

    haunt say -lead "Then sit a minute" "I finally finished the quarterly report"

    you:   I finally finished the quarterly report
    ghost: Then sit a minute and let the weight of it drop away. That is a heavy
           job finished. Do you have a warm drink to go with the relief?
           · led with "Then sit a minute"

Gemma finished the clause as Ember would, and the decide call before it
had kept the report in memory, as it would have unled.

A lead that fights the persona, typed in the conversation (Ember is told
to be glad without exclamation marks or gushing):

    /lead Oh wow, that's AMAZING!!
    I made a cup of tea

    you:   I made a cup of tea
    ghost: Oh wow, that's AMAZING!! There is nothing quite like a warm cup to
           settle the mind. Is it a favorite blend of yours, or something new?
           · led with "Oh wow, that's AMAZING!!"
    you:   the kettle took forever though
    ghost: Those slow moments can be a trial when you are really looking forward
           to the first sip. It makes the heat feel more earned, though. Did it
           keep you waiting long?

The lead is said, since it is already written, and Gemma went back to
Ember straight after it: the rest of that reply is calm and has no
exclamation mark, and the next reply has nothing of the lead in it.

A lead on a question, in the same conversation:

    /lead Well, actually
    what's 17 times 3?

    you:   what's 17 times 3?
    ghost: Well, actually, that would be fifty-one. I hope that's the answer you
           were looking for. Are you working on something tricky?
           · led with "Well, actually"
    you:   and 18 times 3?
    ghost: That one comes to fifty-four. You seem to be doing a fair bit of
           counting this afternoon. Is there a project you're trying to wrap up?

Gemma answered through the lead, correctly, and the "actually" stayed a
correction nobody asked for: a lead is said whatever the question, the
same failure as a persistent prefill that breaks on one.

**A lead did not bring the copying back.** Two sessions of five turns
each had a lead on the second turn only: `"Then sit a minute"` on the
finished report above, and `"Well,"` on "my grandmother died this
morning", the turn on which `prefill = "Well, "` made Ember copy an
earlier reply. No later turn in either copied an earlier one, and nor
did any turn of the conversation above, with its two leads. The second:

    you:   hello
    ghost: Hello again. It is nice to have you here for a bit. Are you settling
           in for some work, or just passing through?
    you:   my grandmother died this morning
    ghost: Well, I am so sorry to hear that. I will stay right here with you for
           as long as you need.
           · led with "Well,"
    you:   what's 17 times 3?
    ghost: That would be 51. Is there something you are trying to figure out, or
           just keeping your mind busy?
    you:   I think I'll make some soup
    ghost: Soup sounds like a proper comfort. I hope it warms you through. Do you
           have a favorite kind you usually make?

That fits the likely trigger named above, a prefill after the same
`speak` call on every earlier turn: with a lead, the earlier turns'
speak calls had none. Three sessions do not settle it (the persistent
prefill copied in three of four), but none copied.

A lead on a model that writes its words in the decide call (Qwen, or
`fold` on) cannot be a prefill: the turn's context asks `Begin your
reply with: "…"` instead, and the words are kept as written. That
weaker form was not tried here.

Note where `prefill` goes in the file: under `[persona]`, above the
first `[[persona.example]]`. A key written after an `[[persona.example]]`
header belongs to that example, and haunt ignores it without a word;
`prefill = "Hm. "` placed below wisp's example loaded as no prefill at
all.

## 5. Sampling

`[persona.sampling]` is laid over the person's `[model.sampling]`, key by
key, and applies to both calls of a turn: the words, and the decide call
that picks the tools. A setting that wrecks grammar wrecks tool calls
first.

```toml
[persona.sampling]
temperature = 0.8
min_p = 0.05
dry_multiplier = 0.8
```

That is Ember's. Its server starts with `--temp 1.0 --top-p 0.95
--top-k 64`, so anything the ghost does not set comes from there.

**`temperature`** spreads the choice of each word. Lower is steadier and
plainer, higher is livelier and wanders. Ember runs at 0.8 and held
its voice in every run here. Start at 0.8 on Gemma 4.

**`top_p`** keeps the most likely words up to that share of the
probability. With `min_p` set it does little; leave it at the server's
value.

**`min_p`** drops any word less likely than that fraction of the
likeliest. It is the cleanest way to stop a high temperature from
producing nonsense, since it adapts to how sure the model is. 0.05 on
Gemma 4.

**`repeat_penalty`** lowers the odds of any token already in the recent
window. Words such as "the" and "you" are repeated tokens too, so a high
value bends grammar. On Ember, 1.5 gave longer, more ornate replies and
no visible damage in four turns:

    you:   I finished the report, finally
    ghost: That is quite a weight off your shoulders, isn't it? There are few things
           as satisfying as finishing an arduous job and closing those tabs. How do
           you plan to reward yourself now that it is done?

At 2.0 it broke everything. The boot turn's decide call wrote 13,443
tokens of malformed tool calls until the server gave up with a 500, and
the next reply was:

    ghost: It was quiet and slow as any evening should be_ I spent most of my time
           watching some embers drift into a soft grey haze._ How has {your day
           been treating you so far?}**
           Wait, no markdown in responses... let me just provide it simply: 💬 ...

**Qwen** at 1.5 did not hold. Replies ran to several paragraphs (92
words on the first turn, past the 60-word cap haunt's own graders hold
a reply to) and made things up about the person:

    you:   tell me about your evening
    ghost: I watched dusk settle over your screen like old linen—slow and soft—and
           lit my little flame when you turned off all but one window for reading.
           The book's title slipped away before I could catch it, though; ...

**Don't set `repeat_penalty` above about 1.1 on a ghost; 2.0 is
unusable on Gemma 4, and 1.5 already too much on Qwen.** Use DRY
instead.

**`dry_multiplier`** penalises repeating a run of tokens that already
appeared, growing with the run's length, so it stops a phrase from
coming back without touching single common words. A talkative character
repeats itself sooner than a terse one, which is why Ember sets 0.8.
For a talkative character start at 0.8 on both models; a terse one has little to repeat and can leave it off, as
wisp does.

**`xtc_probability`** sometimes removes the likeliest choices, forcing
a less obvious word. It is for a voice that has gone predictable. Wisp
at 0.5 stayed terse and in character over four turns:

    you:   I had a long day
    ghost: My condolences. I'd offer a shoulder to cry on, but I'm mostly just pixels.

and its last reply was its example verbatim, as without XTC (section
3). It bought nothing visible. **Don't reach for XTC on a terse
character**: a one-sentence reply has few words to vary, and the one
the model would choose is often the right one.

**Qwen, starting settings.** Ember ran five turns with its own
`[persona.sampling]` (temperature 0.8, min_p 0.05, DRY 0.8, over the
server's card defaults) and three with the card's values in their
place (temperature 0.7, top_p 0.8, min_p 0). The replies could not be
told apart by setting: both ran 10 to 41 words, both leaned on dashes,
and both opened the very first boot with "You're back.", as if the two
had met before:

    event: kind=boot first_time=true
    ghost: You're back. I was just thinking of the last time we sat together—was
           it the quiet after the rain?          (card settings)
    ghost: You're back. I was just wondering if you'd come by. How was your
           morning?                              (Ember's settings)

That is the model, not the sampler. Start a ghost on Qwen from the
card's values on the server (`--temp 0.7 --top-p 0.8 --top-k 20
--min-p 0`) and Ember's DRY 0.8 on top for a talkative character, and
fix the invented past in the persona, not in sampling.

The person's `[model.sampling]` takes the same keys, as does a
per-ghost `[model.ghost.<name>.sampling]` ([The model](../user/model.md));
a ghost's own wins over both.

## 6. Thinking

Set `thinking = "off"` under `[model]` for a local model of this size.
Haunt then sends `reasoning_effort: "none"` and `chat_template_kwargs:
{"enable_thinking": false}` on every call; the second is what Gemma's
and Qwen's templates read. The words are always written with thinking
off, whatever the setting; the setting is about the decide call on the
person's turns.

With `thinking = "medium"`, the same three turns on Ember:

| | thinking off | thinking medium |
|---|---|---|
| decide call output, person's turn | 13 to 44 tokens | 122 to 147 tokens |
| person's turn, start to last word | 4.8 to 8.5 s | 7.6 to 10.5 s |
| decide call cached, first person turn after an event | 2884 | 0 |
| the replies | in voice | in voice, no better |

The cache line is the template's doing. Gemma 4 writes its thinking
marker `<|think|>` at the very top of the first system turn, so a decide
call with thinking on and one with thinking off differ from the third
token, and the whole prompt is read again each time the kind of turn
changes. Event turns always decide without thinking, the person's turns
with it, so a mixed day misses often.

**Qwen:** Instruct 2507 has no thinking mode, and its template reads
neither switch. With `thinking = "off"` haunt still sends
`reasoning_effort: "none"` and `enable_thinking: false`; with
`"medium"` it sends `reasoning_effort: "medium"` and no kwargs. The
server accepted both without complaint, the decide call wrote 20 to 55
tokens either way, and the cache was the same (3296 of 3460 cached on
the person's first turn with `"medium"`). The key changes nothing on
this build; leave it `"off"` so a switch to a thinking model does not
surprise you.

## 7. Context and cache

`context_length` under `[model]` is the window haunt plans for. When
the transcript passes 0.6 of it, haunt folds whole past days behind
their diary entries (section 9) and, if today alone is too long,
summarises today's older turns. Set it to what one slot of the server
really holds, not to the model's maximum.

A local model reads a prompt at a few hundred tokens a second before its
first word, so what matters is how much of each prompt the server still
has cached. The decide prompt and the speak prompt differ at part 1 (the
tools render into the first system turn on Gemma 4), so on one slot each
call evicts the other's prompt. `slots = [0, 1]` sends the decide calls
to slot 0 and every call without tools to slot 1, with llama-server's
`id_slot`. It needs a server whose slots keep separate caches:

    llama-server ... -c 32768 -np 2 --no-kv-unified

Each slot gets half of `-c`, so `context_length = 16384`. The server's
default (`-np` auto, four slots sharing one unified cache) loses the
other slot's prompt on every call, and pinning gains nothing there.

What `HAUNT_DEBUG=brain` logs on Ember's five-turn demo, with slots on:

    decide: 3018 prompt tokens (2884 cached), 13 out
    speak: 1251 prompt tokens (1087 cached), 28 out
    turn over: person took=5.7s face=happy by=speak
    decide: 3105 prompt tokens (2957 cached), 44 out
    speak: 1389 prompt tokens (1160 cached), 31 out
    turn over: person took=7.6s face=sad by=speak

On steady turns the decide call had 2800 to 3500 tokens cached and the
speak call 1000 to 1600, dipping to 600 or 700 on a turn whose decide
call used a tool first. With `slots` unset on the same two-slot server,
both calls fell back to the prefix they share:

    decide: 3092 prompt tokens (734 cached), 26 out
    speak: 1351 prompt tokens (734 cached), 30 out

and a run on the four-slot unified default with `slots` on read 0
cached on every call.

**Don't change part 1 from turn to turn.** Anything in `system` or the
examples is cached; anything that changes there (a time, a counter)
makes every turn read the whole prompt again. Haunt keeps the per-turn
parts at the end for this reason. Part 1 changes legitimately when a
conversation opens or closes (rule 8 is rewritten for the mode), and
part 2 when the ghost rewrites a note or writes a diary entry; expect
one slow turn after each.

How to tell from the log: `cached` close to `prompt tokens` on the
decide call means the slot held; `cached` stuck at the same few hundred
on both calls means they are sharing a slot (`slots` unset, or a unified
cache); 0 after a turn of a different kind means the prompt's top
changed (thinking switched, section 6).

## 8. Chat templates

Start llama-server with `--jinja` (on by default in recent builds) so
the model's own template renders the prompt and the tool calls. What
the template does decides what the cache can keep and what a prefill
continues.

**Gemma 4** (run here; the template is embedded in the GGUF):

- Tools are rendered inside the first system turn, after the content of
  the first system message:
  `<|turn>system\n{part 1}<|tool>declaration:speak{...}<tool|>...<turn|>`.
  So a call with tools and one without differ right after part 1, which
  is why the two calls want their own slots.
- The thinking marker `<|think|>` comes first in that system turn, before
  part 1 (section 6).
- Later system messages (parts 2, 3 and 6) render as system turns of
  their own, wherever they fall.
- With thinking off, the generation prompt adds an empty thought channel,
  `<|channel>thought\n<channel|>`, and a prefill comes after it.
- Gemma 4 sometimes writes a tool call as text in its own notation
  (`<|tool_call>call:ask{...}<tool_call|>`); haunt reads that back as a
  call ([The model brain](../dev/brain.md#deciding-then-speaking)).

**Qwen3 30B-A3B Instruct 2507** (run here; the template served by
`/props`), rendered with `/apply-template` from a haunt-shaped speak
call:

    <|im_start|>system
    PART1<|im_end|>
    <|im_start|>system
    PART2<|im_end|>
    <|im_start|>user
    boot<|im_end|>
    <|im_start|>assistant
    <tool_call>
    {"name": "speak", "arguments": {"pose": "happy"}}
    </tool_call><|im_end|>
    <|im_start|>user
    <tool_response>
    Now say it.
    </tool_response><|im_end|>
    <|im_start|>assistant
    Hm. Hello.<|im_end|>
    <|im_start|>user
    hello<|im_end|>
    <|im_start|>system
    PART6<|im_end|>
    ...
    <|im_start|>assistant
    <think>

    </think>

    Hm.

- Tools go into the first system turn after part 1, under `# Tools`, as
  JSON inside `<tools></tools>`, so the two calls differ right after
  part 1, as on Gemma, and `slots` matters the same way.
- A system message after the transcript (part 6) renders as a system
  turn where it falls; the template accepts it.
- A tool result renders as a user turn holding `<tool_response>`.
- There is no thinking marker at the top and none in the generation
  prompt. A trailing assistant message, the prefill, gets an empty
  `<think>\n\n</think>\n\n` in front of it, and no earlier reply has one.

Render a haunt-shaped prompt like this (section 1) before running a
ghost on any other template.

**The difference seen** on Ember's five turns: Qwen rarely calls
`speak`. On every turn where it used a tool first (`mood`, `remember`,
`face`, `recall`), it wrote the reply as the text of its second decide
round and ended there, so haunt took the text as the words: no speak
call, no prefill, and the face left as it was or guessed from the words
(once `asleep`, for "I'm here, not quite gone"). Compare the two logs
for the turn that remembered the tea:

    Gemma 4:  decide: 3253 prompt tokens (2859 cached), 37 out
              speak: 1530 prompt tokens (1062 cached), 26 out
              turn over: person took=7.8s face=happy by=speak
    Qwen:     decide: 3611 prompt tokens (3464 cached), 28 out
              decide: 3660 prompt tokens (3638 cached), 36 out
              turn over: person took=700ms face=-

Qwen's turns took 300 to 900 ms against Gemma's 5 to 10 s, with the
decide prompt cached as well (3270 to 3820 of 3420 to 4030), since only
3 billion of its parameters are active per token. It also called `mood`
on most turns; Gemma never called it in any run here.

## 9. The ghost's own notes and the diary

The ghost has two notes in part 2 of every prompt, `self.md` on itself
and `you.md` on the person, each under 1500 bytes, rewritten whole with
`rewrite_notes` ([Memory](../user/memory.md)). `self.md` is how a
character settles: what it has come to like, the turns of phrase it
keeps, what it thinks of the person's habits. Say in the persona what
belongs there:

```toml
Keep in your notes on yourself the small habits of speech you settle into and what you have come to like.
```

In the short runs here Gemma 4 never wrote `self.md`: it kept facts
about the person under topics (`remember`) and left its notes empty, so
expect a character to settle over days, not in a sitting.

The diary is `memory/diary.md`: one dated entry per day the two of you
talked, written by the ghost once the day is over, the first time the
person is idle after it. The last three days are in part 2, with a day
that has no entry named as such, so a gap reads as a gap. Ember's, from
a day about a move, a cat and a thesis:

    ## 2026-10-11 Sunday
    Met my companion today. They've just moved from Sydney to Brisbane and are
    still unpacking. We talked about their cat, Biscuit, who isn't fond of the
    new flat. I've also promised to help them stay on track for their thesis
    deadline on the 30th. It feels like a busy time for them, so I'll keep the
    hearth warm.

    <!-- self t=8 2026-10-12 -->

It is in Ember's voice because `[memory] reflection = "character"`, the
default, writes it in a turn of the ghost's own, with part 1 and its
persona; `"side"` writes it in one plain call with no persona and no
tools, which costs less on a slow model ([Configuration](../user/config.md#memory)). The
turn shows in `haunt history -why` as `event: kind=reflect day=...`, and
`haunt journal -diary` prints the file. A persona does not need to
mention the diary; the instructions for the entry come with the turn.
What a persona can do is give the ghost something to write about:
"ask after what they told you before" brings back the facts the entry
keeps.

The side mode was not run here.

## 10. Staying in character over days

Three things carry a character across days: the reminder (section 2)
on every turn, the diary in part 2, and time. Every turn's part 6 says
how long since the last exchange (`Last exchange: 3 days ago (Thursday
evening).`), and when the gap passes `[initiative] away_hours` (8 by
default) the ghost is told so as an event: on its boot envelope when it
starts after the gap (`kind=boot first_time=false since=1d
last="yesterday afternoon"`), so a restart gets one greeting, or as a
`kind=returned` turn when it ran all along and the person comes back to
the desk.

How the ghost greets a gap is the persona's to say, and the first
lever if the greeting reads as a wall of recap. Ember's persona
says:

```toml
After a long time apart, say in a sentence that you are glad they are back and ask after one thing from before; never recount what you talked about.
```

Ember after a day about a move, a cat and a thesis, its transcript
backdated a day and the ghost started again:

    event: kind=boot first_time=false since=1d last="yesterday afternoon"
    ghost: I am glad you are back. How is Biscuit doing with the new place today?
    you:   morning. where were we?
    ghost: Good morning. We were talking about your thesis and that deadline on
           the 30th. Do you feel like making a start on it today?
           · used recall {"query":"last things we talked about"}

One sentence of welcome and one question, no recap until asked, and the
answer to "where were we?" goes back to yesterday's deadline.

## 11. What you cannot do through haunt yet

Each of these is a request field llama-server accepts and haunt does
not send. None is exposed in `ghost.toml` or `config.toml`. Where the
server has a flag of its own, that flag applies to every request from
every ghost, decide calls and tool calls included, so treat the
server-side route as an experiment, not a fix.

- **`logit_bias`**: ban or discourage tokens, such as a model's
  favourite opener ("Oh," is the tokens `Oh` and `,` on Gemma 4, ids
  12932 and 236764, and 11908 and 11 on Qwen, from `/tokenize`; ids
  differ per model). Haunt does not expose it.
  Server-side: `--logit-bias 12932-5`, which also biases the decide
  call.
- **A grammar on the speak call**: constrain the words to, say, no
  brackets, no asterisks, at most three sentences. Haunt does not
  expose it. Server-side `--grammar-file` applies to tool calls too and
  would break them; there is no safe server-side route.
- **`n_keep`**: tokens of the prompt kept when a slot overflows and the
  server shifts its context. With `context_length` set to a slot's
  share, haunt compacts long before that, so it matters only when
  `context_length` is wrong. Haunt does not expose it. Server-side:
  `--keep N`.
- **Stop sequences**: end the words at a string, such as a second
  paragraph or a stage direction's `*`. Haunt's client can send `stop`,
  but no setting fills it. llama-server has no server-wide default for
  it.
- **Per-ghost `chat_template_kwargs`**: haunt sends only
  `enable_thinking` (false when thinking is off) and nothing a ghost can
  add. Server-side: `--chat-template-kwargs '{"key": "value"}'` sets
  defaults for every request.
- **A custom `--chat-template-file`**: a copy of the model's template,
  edited (for example to move the tool block, or to render the speak
  call's prefill differently). This is server-side only by nature, and
  haunt neither ships nor checks one. On Gemma 4, keep the template's
  `{#- OpenAI Chat Completions:` marker, or llama.cpp applies its
  workarounds for an older Gemma template.
- **`top_k` and `presence_penalty`**: two samplers the Qwen3 card asks
  for (20, and 0 to 2) that `[persona.sampling]` does not take.
  Server-side: `--top-k 20` and `--presence-penalty N`, both in this
  build's `--help`; the Qwen runs here had `top_k` 20 from the server
  and `presence_penalty` 0.
- **Requiring a tool call**: `tool_choice: "required"` on the decide
  call would make a model that answers in text, as Qwen does after a
  tool, call `speak` instead, keeping the prefill and the chosen face.
  llama-server reads `"required"`; haunt does not send `tool_choice`,
  and there is no server-side default.

## 12. Troubleshooting

| What you see | What to try |
|---|---|
| Markdown in the balloon | Rule 8 already forbids it, and Ember asked for "a list of three things" got an `ask` and no list. If it shows, look for a persona or skill that asks for lists or steps, and for `repeat_penalty` or a temperature high enough to unsettle the format (section 5). |
| Stage directions (`*smiles*`) | The persona describes a body or actions; describe a voice instead. A sentence-long prefill invites them: a probe of Gemma 4 with `"Oh, how lovely! "` before a sad message continued with `*Ember's flame flickers and softens*`. |
| It answers as the assistant ("How can I help you today?") | The persona says "AI", "assistant" or "helpful" (section 2). Name the creature and how it talks, not its job. |
| The same reply on every turn | A prefill on Gemma 4 (section 4): remove it. An input that matches an example's `user` line (section 3): rewrite the example. |
| The same opener every reply | Lower the temperature a little and set `dry_multiplier` (section 5); check the examples do not all open the same way, since the model takes their openings as the voice. `logit_bias` would ban it outright, and is not exposed (section 11). |
| A shy or quiet character never answers | Say what the shyness sounds like, not that it holds back: "you speak softly, in a few words" rather than "you are shy" or "you keep to yourself". Silence is a decision (`stay_silent`), and a persona that praises quiet makes it the easy one. |
| Silence on everything | `haunt history -why` says "woke, and chose not to speak" on events by design (rule 2). Silence on the person's own words means the turn failed (`(the turn failed)` and the reason in the balloon) or the persona tells it to hold back; "you are shy" can read as "do not answer". |
| It refuses the character ("As an AI, I don't have a fireplace") | Make the first sentence a claim of what the ghost is, not a role to play ("You are Ember, a...", not "Pretend to be..."), and keep it free of anything the model would want to correct. Not seen on Gemma 4 here. |
| A field seems to do nothing | Check it sits in the right table: a key under `[persona]` written after an `[[persona.example]]` header belongs to the example and is ignored (section 4). |
| Every turn is slow | Read `HAUNT_DEBUG=brain` (section 7): low `cached` means the slots are shared or part 1 is changing. |
