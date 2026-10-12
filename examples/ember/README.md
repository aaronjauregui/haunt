# ember

A persona ghost: a warm, talkative hearth spirit. It is the other end of
the levers from wisp, the dry and terse demo ghost, and it is the ghost
the guide to building a character on a local model
([docs/ghosts/local-models.md](../../docs/ghosts/local-models.md)) is
written against: each fragment the guide shows is a piece of
`ghost.toml` here, or a change to it that was run and read back.

It has no brain of its own and no art. It thinks with the model under
`[model]` in your `~/.config/haunt/config.toml`, and wears the built-in
placeholder shell.

    make demo-ember                                  # a nested sway window
    WLR_BACKENDS=headless GHOST=ember scripts/demo.sh  # no window

The demo prints the `haunt say` line to talk to it and the `haunt
history` line to read back why it said what it did. Its memory and
transcript are in the demo's throwaway directory and go with it.

To keep it, run it from here instead:

    haunt run examples/ember
