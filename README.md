# Haunt

A desktop companion for Wayland. A **ghost** lives on your screen: a
character with a personality, a **shell** of layered art that blinks and
changes expression, and a **balloon** it speaks in. It remembers you across
sessions, takes on reminders, can act through the tool servers you give it,
and speaks first now and then, within a daily allowance you set.

The words are the ukagaka community's. Nothing else is: there is no SHIORI,
SSTP or SakuraScript, and this runs on wlroots-based Wayland compositors
(sway, Hyprland) and others that implement `zwlr_layer_shell_v1`.

A ghost's thinking is [hermes-agent](https://github.com/nousresearch/hermes-agent).
Haunt is the body and the conscience: the screen, consent, budgets, quiet
hours, and the promises it keeps. A ghost is art plus a persona, with no
code to write; one that wants scripted logic ships a sandboxed WASM brain.

Most of the code here was written by an LLM, under my direction and
review. The design, the decisions and the mistakes are mine.

## Try it

    make demo         # a scripted ghost, in a nested sway window
    make demo-ai      # one that thinks, on hermes-agent
    make demo-troupe  # two of them at once, sharing a screen

Poke it, drag it, double-click for a menu, right-click to type at it.

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
