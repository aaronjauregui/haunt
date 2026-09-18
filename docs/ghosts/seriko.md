# Converting ukagaka shells

Two decades of ukagaka shells exist. `haunt-seriko` converts their
**art** into a haunt shell: surfaces, collisions and frame-swap
animation. It reads SERIKO/1.x and 2.0 `surfaces.txt`, PNG with PNA
transparency and colour-keyed art, and Shift-JIS files, from a shell
directory or a `.nar` archive.

It converts art only. SHIORI, SSTP and SakuraScript are out of scope and
stay that way, so what you get is a shell a haunt ghost wears, not
somebody else's ghost. Cursors, tooltips, balloon offsets and reference
points are read but not converted: they describe a baseware haunt is not.

    haunt-seriko -n ghost.nar        # what it would do, and how many clips the shell has
    haunt-seriko ghost.nar           # convert it, into a directory named after the shell
    haunt run <dir>

What becomes what:

| SERIKO | Haunt |
|---|---|
| a surface's element lines | a composited base image, and a pose |
| collision rectangles | named hit regions a brain can react to |
| animation definitions | clips, with the nearer of `sometimes` and `rarely` for a random interval |
| `descript.txt` name | the ghost's name (`-name` overrides) |

The conversion prints what it decided (what was dropped, approximated or
renamed) before what it wrote. Read that first.

## Flags

| Flag | Meaning |
|---|---|
| `-o DIR` | where to write; default a directory named after the shell |
| `-surface N` | the surface the ghost rests on (0 is SERIKO's convention) |
| `-only 0,1,2` | convert only these surfaces and whatever they animate; a two-hundred-surface shell is a lot of PNGs for a first look |
| `-pose happy=3,sad=5,surprised=2` | name poses, so the face can follow the words |
| `-name NAME` | the ghost's name |
| `-n` | dry run |

## Why a converted shell looks static

Plenty of shells define no animation at all: their `surfaces.txt` is
collision boxes and nothing else, and every frame of movement came from
the script switching surfaces, which is the half this does not import.
`-n` says how many clips a shell actually has.

Even a shell with a blink looks still, because ukagaka shells lean on
their script to change expression constantly. Naming a few poses is what
puts a converted shell back in touch with its face: the driver chooses
`happy`, `sad`, `surprised`, `asleep` and `thinking` from the words a
ghost says ([The shell](shell.md)), and only a person looking at the art
can say which surface is a smile.

## Trying one without installing anything

    make demo-seriko SERIKO=~/Downloads/some_ghost.nar
    make demo-seriko SERIKO=https://example.org/some_ghost.nar
    make demo-seriko SERIKO=... SERIKO_ARGS="-only 0,1,2 -pose happy=1"

The archive is fetched, converted and run inside a throwaway directory,
and the ghost's memory goes there too, so nothing lands in your library.

The converted directory is a scripted ghost with no brain and a shell. To
give it a mind, add a `[persona]` and the `llm` capability, or a
`[brain]`.
