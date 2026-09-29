# Packaging

A ghost ships as one signed archive, `<name>.haunt`. Installers verify the
signature and pin the author's key per ghost name, so a later package
under the same name must come from the same author or it is refused.

## Signing key

    haunt keygen        # ~/.config/haunt/author.key and author.pub, once

The key is Ed25519. `keygen` refuses to overwrite an existing one. The
public key's fingerprint is what `haunt install` shows as the author.

## Building a package

    haunt pack mycompanion             # writes mycompanion.haunt
    haunt pack -o out.haunt mycompanion

`pack` loads and validates the manifest first, so a broken `ghost.toml`
fails here rather than on the person's machine. For a scripted ghost the
brain module named in the manifest must already be built.

`pack` and `install` both refuse a manifest whose shell names a file
outside the ghost directory, but neither opens the art, so a shell that
will not load still packs.

What goes in: every regular file under the directory, sorted, with fixed
metadata so the same directory always packs to the same bytes. Skipped:
dot-directories (`.git`) and a directory named `brain`, since the shipped
artefact is `brain.wasm`. Everything under `skills/` is included. The
unpacked size is capped at 256 MB.

The archive is an uncompressed tar holding `ghost.tgz`, the author's
public key, and a signature over the content bytes.

## Installing

    haunt install mycompanion.haunt
    haunt install -yes mycompanion.haunt      # grant every requested capability

Install prints the name, version, author fingerprint, requested
capabilities with what each allows, and the tool servers the ghost would
run with the environment variables it would pass through. Then it asks
about the capabilities. Whatever is withheld is asked for again in the
balloon when it matters.

Installed, the ghost lives at `~/.local/share/haunt/ghosts/<name>/pkg/`,
with its memory and grants beside it. Reinstalling a newer package keeps
both.

## Working without packaging

    haunt run ./mycompanion    # a directory, as it is
    haunt dev ./mycompanion    # and restart in place when files change

A directory run this way has every capability it declares granted, with
no questions, and keeps its state under the library like an installed
ghost of the same name. `-state DIR` puts the state elsewhere.
