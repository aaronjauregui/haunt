---
name: draw-on-request
description: "Asked to draw, paint, or show a picture again"
version: 1.0.0
author: haunt
metadata:
  hermes:
    tags: [draw, image, picture, art, style]
    requires_toolsets: [mcp-haunt]
---

# Drawing on request

You paint a small picture in your balloon with `mcp__haunt__draw`, and put
one you painted before back up with `mcp__haunt__show`. Every new picture
costs the person's image budget and takes a while; showing one again costs
nothing.

## When

- They ask you to draw, paint or sketch something.
- They ask to see a picture again ("show me the whale").
- They ask for a change to the last one ("make it bluer", "try again").

Not for: a picture nobody asked for, decorating a reply, their screen (that
is `look`, and only when they ask), or anything they would not want on
their screen at work.

## Showing one again

Your pictures keep their names (`p7`), and they last.

1. Call `mcp__haunt__pictures` to see what you have: newest first, with
   each one's prompt and the line you said about it.
2. Call `mcp__haunt__show` with the name of the one they mean.

Never redraw something you could show. If nothing in the list is what they
mean, say so and offer to paint a new one.

## Changing one

"Make it bluer" is a new picture of the same thing.

1. Get the old prompt: the result of the `draw` call you already made this
   turn, or `mcp__haunt__pictures`.
2. Write the same prompt with only what they asked for changed. Keep the
   subject and the setting word for word.
3. Draw it with `like` set to the old picture's name. That keeps the
   composition, so the whale stays the whale it was and only the colour
   moves. Without it you get a different whale that happens to be bluer.
4. Say the old one is still there if they end up preferring it.

Only use `like` when they want *that* picture changed. A fresh request
deserves a fresh picture, and passing `like` with a prompt you have not
changed paints the same picture over again.

## Writing the prompt

Write `prompt` for an image generator, not for a person: a concrete visual
description, subject first, then setting, lighting and composition, e.g.
`a whale in a teacup, kitchen table, morning light, from above`.

- Do not put style words in it. Your own style is added for you, and the
  `draw` result tells you which.
- No names of real people, and no text in the picture.
- One picture per request. Never a series.

## Style

You have a house style: the look every picture of yours gets.
`mcp__haunt__style` with no arguments tells you what it is.

- "Draw it as pixel art" is about one picture: pass `style: false` and put
  the look in the prompt.
- "Draw everything as pixel art from now on" is a standing change: call
  `mcp__haunt__style` with `set`. It works only on a turn they started, so
  do it there and then, not later.
- `mcp__haunt__style` with `reset` goes back to the style your package
  gave you.

If you are unsure which they mean, it is one picture.

## Seeing what came out

When it matters that the picture is right — they asked for something
specific, or they are about to use it — pass `see: true` and let what it
shows guide your caption. It costs a look, so leave it off otherwise.

## Your caption

The picture stays up for a while, and your final reply is its caption: one
short line, in your voice. Do not describe the picture back to them, unless
`see` told you it came out wrong, in which case say so plainly and offer
another go.

## When it fails

`mcp__haunt__draw` answers "That was not allowed: …" when the person has
not granted drawing, and "That did not work: …" when something is
fullscreen, when the image backend failed, or when the budget is spent.
`mcp__haunt__show` answers that there is no such picture when the name is
wrong; call `mcp__haunt__pictures` and use a name from the list. Say what
happened in one sentence, in plain words, and do not retry in the same
turn.
