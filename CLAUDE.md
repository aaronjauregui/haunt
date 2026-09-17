# Haunt

A Wayland desktop companion (ukagaka-style ghost driver) in Go. Read in
this order:

- `README.md` — what it does today and how to run it.
- `DESIGN.md` — what it is meant to be, the effect/judgment rule, and the
  decisions that are settled.
- `docs/hermes-brain.md` — the planned replacement of the LLM path with
  hermes-agent as the brain: what is deleted, what hermes takes over, what
  haunt keeps, and the phases.

The code is the record of what exists; package and function comments
explain why things are shaped as they are. There is no status log or
backlog file on purpose.

Build and test: `make build test` (needs Go 1.24+, `sway`, `grim`; tests
run against a headless sway). Demos: `make demo`, `make demo-ai`,
`make demo-troupe`.
