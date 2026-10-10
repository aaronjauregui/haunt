# Haunt

[![test](https://github.com/aaronjauregui/haunt/actions/workflows/test.yml/badge.svg)](https://github.com/aaronjauregui/haunt/actions/workflows/test.yml)

A desktop companion for Wayland inspired by the venerable Ukagaka. A ghost lives on your screen: a character with a personality, a shell of layered art that blinks and changes expression, and a balloon it speaks in.

![A ghost dragged across the screen, asked "what are you?" by typing at it, and answering in its balloon](docs/media/demo.gif)

It remembers you across sessions, takes on reminders, paints a picture when you ask for one, looks things up on the web if you let it, can act through the tool servers you give it, and speaks first now and then, within a daily allowance you set.

Note that this is not intended to be compatible with Ukagaka, it's a ground-up redesign: there is no SHIORI, SSTP or SakuraScript, and this runs on wlroots-based Wayland compositors (sway, Hyprland) and others that implement `zwlr_layer_shell_v1`.

Haunt controls the body and the conscience: the screen, consent, budgets, quiet hours, and consent gates. A ghost is art plus a persona, with no code to write. One that wants scripted logic ships a sandboxed WASM brain; one that wants to think does so with the model you choose, local or cloud, over any OpenAI-compatible endpoint, while haunt keeps its conversation, its memory as plain Markdown files, and its skills.

Most of the code here was written by an LLM, under my direction and review.

## Try it

    make demo

A ghost in a nested sway window, needing nothing but Go and `sway`: its
brain is a WASM module built from `examples/whisper`, so there is no
model, no key and no network. Poke it, drag it, double-click for a menu,
right-click to type at it.

    make demo-ai      # the ghost above: also needs a model in [model]
    make demo-troupe  # two at once, one scripted and one thinking

`demo-ai` is what the picture shows, and what it asks of you is in
[The model](docs/user/model.md).

A compositor without `zwlr_layer_shell_v1` cannot keep a ghost, but it can
still show you one: the demos run sway nested inside whatever session is
already running, and `WLR_BACKENDS=x11 ./scripts/demo.sh` does the same
from an X11 one.

## Install

    make install-user
    systemctl --user enable --now haunt

Needs Go 1.25+ to build, and `grim` if a ghost is allowed to look at your
screen. See [Installing](docs/user/install.md).

## Documentation

| For | Start at |
|---|---|
| People running a ghost | [User guide](docs/user/install.md) |
| People making a ghost | [Ghost creator guide](docs/ghosts/anatomy.md) |
| People changing haunt | [Developer guide](docs/dev/architecture.md) and `go doc` |

[docs/index.md](docs/index.md) lists every page.

## Building

    make build test

Tests run against a real headless sway, so `sway` is needed for them.

## License

Apache 2.0; see [LICENSE](LICENSE). Copyright 2026 Aaron Jauregui.
Vendored code under `third_party/` is under its own terms, kept in
each directory.
