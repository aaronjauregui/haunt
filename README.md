# Haunt

A desktop companion for Wayland. A **ghost** — a character with a
personality and its own logic — lives on your screen: it has a **shell**
(layered art that blinks, breathes and changes expression) and speaks in a
**balloon**. It remembers you across sessions, and it will occasionally
speak first.

The terms are the ukagaka community's, kept deliberately. Nothing else is:
there is no compatibility with SHIORI, SSTP or SakuraScript, and this runs
on wlroots-based Wayland compositors (sway, Hyprland) and others that
implement `zwlr_layer_shell_v1`. See `DESIGN.md` for the whole design and
its reasoning.

## Try it

    make demo        # a scripted ghost, in a nested sway window
    make demo-ai     # one that talks to your LLM

Poke it, drag it, right-click to type at it. `make demo-ai` needs an
OpenAI-compatible endpoint in `~/.config/haunt/config.toml`:

```toml
[ai.llm]
base_url = "http://localhost:11434/v1"   # Ollama, llama.cpp, anything
model = "qwen2.5:7b"

[ai.embed]                                # optional: long-term memory
model = "nomic-embed-text"

[ai.image]                                # optional: it can draw
backend = "comfyui"
base_url = "http://localhost:8188"
model = "your-checkpoint.safetensors"
```

Everything AI is opt-in twice — the ghost asks for a capability, you grant
it — and the driver holds the keys and the budgets, never the ghost.

## Live with it

    make install-user
    systemctl --user enable --now haunt

See `dist/README.md` for the details, including what to add to your sway or
Hyprland config if the service does not start.

## Make a ghost

    haunt new mycompanion
    cd mycompanion && make run

That scaffolds a working ghost: a manifest, a shell, and a brain in Go
compiled to WASM. Brains are stdio programs — events in, intents out — and
the same source builds as a sandboxed WASM module or a native process.
`examples/whisper` is scripted; `examples/muse` uses the LLM.

## Commands

| | |
|---|---|
| `haunt run [-output NAME] [ghost]` | run a ghost (remembers where it was) |
| `haunt dev <dir>` | run with hot reload |
| `haunt say <text>` | talk to it from a script |
| `haunt history` / `haunt journal` | what was said / what it chose to remember |
| `haunt shush` / `unshush` | quiet hours, now |
| `haunt new`, `pack`, `install`, `ls`, `switch`, `rm` | authoring and packages |

## Building

Go 1.24+, and `sway` plus `grim` for the tests, which run against a real
headless compositor rather than a mock.

    make build test
