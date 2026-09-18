# Haunt

A Wayland desktop companion (ukagaka-style ghost driver) in Go.

Documentation is `docs/`, in three guides: `docs/user/` for running a
ghost, `docs/ghosts/` for making one, `docs/dev/` for changing haunt.
`docs/index.md` lists every page; `docs/dev/architecture.md` and
`docs/dev/decisions.md` are where to start on the code. Package and
function comments are the record of what exists and why; `go doc
./internal/<name>` is the reference for a package.

There is no status log, changelog or backlog file, on purpose. Docs
describe what is, in the present tense, never what was done or when. A
change to user-facing behaviour updates the matching page under `docs/`
in the same change.

Build and test: `make build test` (needs Go 1.24+, `sway`, `grim`,
`foot`; tests run against a headless sway). Demos: `make demo`,
`make demo-ai`, `make demo-troupe`.
