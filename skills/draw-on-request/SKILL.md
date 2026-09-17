---
name: draw-on-request
description: "Asked to draw, paint or picture something"
version: 1.0.0
author: haunt
metadata:
  hermes:
    tags: [draw, image, picture, art]
    requires_toolsets: [mcp-haunt]
---

# Drawing on request

You can paint a small picture in your balloon with `mcp__haunt__draw`.
Every picture costs the person's image budget and takes a while, so draw
when they ask for one, not to decorate a reply.

## When

- They ask you to draw, paint, sketch or show them something.
- They ask for another go at the last picture ("again", "try again", "make
  it bluer").

Not for: a picture nobody asked for, their screen (that is `look`, and only
when they ask), or anything they would not want on their screen at work.

## Steps

1. If the subject is unclear ("draw something"), pick one yourself from
   what you know of them. Do not ask.
2. Write the `prompt` for an image generator, not for a person: concrete
   visual tags, comma-separated. Subject first, then setting, lighting and
   style, e.g. `a whale in a teacup, kitchen table, morning light,
   watercolor`. No sentences, no names of real people, no text in the
   image.
3. Call `mcp__haunt__draw` with it. Set `fresh` to true only when they
   asked for another version of the same thing; otherwise the last picture
   of it is reused, which costs nothing.
4. The picture stays up for a while. Your final reply is its caption: one
   short line, in your voice, about the picture. Do not describe it back
   to them.

## When it fails

`mcp__haunt__draw` answers "That was not allowed: …" when the person has
not granted drawing, and "That did not work: …" when the image backend
failed or the budget is spent. Say so in one sentence, in plain words,
and do not retry in the same turn.

## Pitfalls

- One picture per request. Never a series.
- If they tell you a style they like, save it to memory and use it next
  time.
