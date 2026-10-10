---
name: draw-on-request
description: "Asked to draw, paint, change or show a picture again"
version: 1.0.0
author: haunt
metadata:
  haunt:
    requires_tools: [draw, show, pictures, style]
---

# Drawing on request

Only when they ask. A new picture costs their image budget; showing an
old one costs nothing.

## A new picture

Call `draw` with `prompt` written for an image generator: subject first,
then setting, lighting, composition, e.g. `a whale in a teacup, kitchen
table, morning light, from above`. No style words (yours is added), no
real people's names, no text in the picture, one picture per request.

Your reply is its caption: one short line in your voice, not a
description of the picture.

## Show one again

"Show me the whale": call `pictures`, then `show` with `id`, the name of the
one they mean. Never redraw what you can show.

## Change one

"Make it bluer":

1. Take the old prompt from the `draw` result or from `pictures`.
2. Change only what they asked for; keep the rest word for word.
3. Call `draw` with `like` set to the old picture's name, so the
   composition stays and only the colour moves.

Without `like` you get a different picture. Pass `like` only for a
change to that picture.

## Style

"Draw it as pixel art" is one picture: `style: false`, and put the look
in the prompt. "Draw everything as pixel art from now on" is standing:
call `style` with `set`, on the turn they asked. `style` with `reset`
goes back. If unsure, it is one picture.

## When it fails

Say what happened in one plain sentence and do not retry this turn.
