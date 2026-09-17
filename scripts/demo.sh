#!/usr/bin/env bash
# Launch a nested sway (a normal window under the current session) and run
# haunt-poc inside it. Close the sway window or Ctrl-C to stop everything.
#
# WLR_BACKENDS defaults to "wayland" (nested window); override with
# WLR_BACKENDS=headless for a smoke test without any visible window.
#
# IDLE_MS sets how long the nested compositor must see no input before the
# ghost is told the user has gone idle — which is when it may speak first,
# and when it looks at whether a goal has gone quiet. The driver's own default is two
# minutes; IDLE_MS=10000 makes initiative watchable without waiting around.
# Note the idleness is the *nested* compositor's: moving the pointer across
# that window resets it.
#
# GOAL_DELAY shortens the wait between going idle and the goal check
# (normally 5m); GOAL_DELAY=5s makes it watchable in a sitting.
set -euo pipefail

cd "$(dirname "$0")/.."

backend="${WLR_BACKENDS:-wayland}"
idle_ms="${IDLE_MS:-}"
goal_delay="${GOAL_DELAY:-}"
workdir="$(mktemp -d /tmp/haunt-demo-XXXXXX)"
# Short, because a unix socket path has about 100 characters to play with
# and the control socket lives under this.
runtime="$(mktemp -d /tmp/hd-XXXXXX)"
# Kill child processes (not the whole group: that would kill this script
# before the rm runs), wait for them, then remove the workdir. The wait
# matters for a hermes ghost: its gateway is still closing its database in
# the workdir for a few seconds after haunt is told to stop.
cleanup() {
    kill $(jobs -p) 2>/dev/null || true
    wait 2>/dev/null || true
    # A hermes profile holds the ghost's skills read-only.
    chmod -R u+w "$workdir" 2>/dev/null || true
    rm -rf "$workdir" "$runtime"
}
trap cleanup EXIT TERM INT

cat > "$workdir/sway.cfg" <<'EOF'
output * bg #24283b solid_color
EOF

# SERIKO=<path or URL> converts a ukagaka shell and runs *that* instead of
# the built-in demo ghosts. Everything happens inside the throwaway workdir:
# the archive is downloaded there, converted there, and the ghost's state
# lives there too, so trying somebody else's shell leaves nothing behind in
# your library and nothing in the directory you ran this from.
#
# GHOST=whisper (default) or wisp, or several separated by spaces —
# GHOST="whisper wisp" puts both on screen at once, which is how you watch
# the multi-ghost behaviour: neither opens on top of the other, and `haunt
# say` follows whichever you last poked. Whisper is the scripted example
# brain, built to WASM; wisp declares no brain at all and so runs on hermes
# ([hermes] in ~/.config/haunt/config.toml), drawing via [ai.image].
seriko="${SERIKO:-}"
ghosts="${GHOST:-whisper}"
ghost_dirs=()

if [ -n "$seriko" ]; then
    echo ">> building haunt and haunt-seriko"
    go build -o "$workdir/haunt" ./cmd/haunt
    go build -o "$workdir/haunt-seriko" ./cmd/haunt-seriko

    src="$seriko"
    case "$seriko" in
    http://*|https://*)
        echo ">> fetching $seriko"
        src="$workdir/$(basename "${seriko%%\?*}")"
        curl -fsSL "$seriko" -o "$src"
        ;;
    esac

    echo ">> converting $src"
    "$workdir/haunt-seriko" -o "$workdir/imported" ${SERIKO_ARGS:-} "$src"
    ghost_dirs=("$workdir/imported")
    ghosts="imported"
else
echo ">> building haunt and: $ghosts"
go build -o "$workdir/haunt" ./cmd/haunt
for ghost in $ghosts; do
    mkdir -p "$workdir/$ghost"
    ghost_dirs+=("$workdir/$ghost")
    case "$ghost" in
    whisper)
        GOOS=wasip1 GOARCH=wasm go build -o "$workdir/whisper/brain.wasm" ./examples/whisper
        cat > "$workdir/whisper/ghost.toml" <<'EOF'
[ghost]
name = "whisper"
version = "0.1.0"
capabilities = ["message", "tell"]

[brain]
wasm = "brain.wasm"
EOF
        ;;
    wisp)
        # No [brain] section and nothing to compile: art and a persona are a
        # whole ghost, and hermes is its brain (docs/hermes-brain.md). Its
        # profile is made under the demo's own hermes home (below), so give
        # it a model in [hermes.model] and its keys in the file
        # scripts/hermes-docker reads.
        cat > "$workdir/wisp/ghost.toml" <<'EOF'
[ghost]
name = "wisp"
version = "0.1.0"
capabilities = ["message", "llm", "imagine", "window", "tell"]

[persona]
system = "You are Wisp, a wry little desktop spirit haunting the user's screen. Reply in one or two short sentences, with dry wit. Let what you remember about them color your mood and callbacks."
EOF
        # WISP_MORNING_CRON gives wisp a morning occasion, which haunt's
        # morning-check-in skill answers. The spin sets it a few minutes
        # ahead, since cron has minute granularity.
        if [ -n "${WISP_MORNING_CRON:-}" ]; then
            cat >> "$workdir/wisp/ghost.toml" <<EOF

[[trigger]]
cron = "$WISP_MORNING_CRON"
name = "morning"
EOF
        fi
        # A skill of wisp's own: a package's skills/ is installed into its
        # hermes profile beside haunt's (docs/hermes-brain.md, section 14).
        mkdir -p "$workdir/wisp/skills/ghost-story"
        cat > "$workdir/wisp/skills/ghost-story/SKILL.md" <<'EOF'
---
name: ghost-story
description: "When they ask for a ghost story: a tiny spooky tale"
version: 1.0.0
author: wisp
metadata:
  hermes:
    tags: [story, spooky, fun]
    requires_toolsets: [mcp-haunt]
---

# Ghost story

Wisp tells ghost stories the way it haunts: briefly, and from the inside.

1. Wear a pose that suits a story, if your shell has one, with
   `mcp__haunt__face`.
2. Tell the story in `mcp__haunt__say`, three sentences at most. It is
   told in the first person, by a ghost, about the living. It ends on a
   small twist, never on gore.
3. Your final reply is one line in which you deny that the story was
   about anyone the person knows.

Never tell the same story twice. If your session history has one, tell a
different one.
EOF
        ;;
    *)
        echo "unknown ghost \"$ghost\" in GHOST (use whisper and/or wisp)"; exit 1
        ;;
    esac
done
fi
ghost_count=${#ghost_dirs[@]}

echo ">> starting nested sway (backend: $backend)"
WLR_BACKENDS="$backend" WLR_LIBINPUT_NO_DEVICES=1 SWAYSOCK="$workdir/sway.sock" \
    sway -V -c "$workdir/sway.cfg" 2> "$workdir/sway.log" &
sway_pid=$!

display=""
for _ in $(seq 1 100); do
    display="$(sed -n "s/.*Running compositor on wayland display '\([^']*\)'.*/\1/p" "$workdir/sway.log" | head -1)"
    [ -n "$display" ] && break
    kill -0 "$sway_pid" 2>/dev/null || { echo "sway exited early:"; tail "$workdir/sway.log"; exit 1; }
    sleep 0.1
done
[ -n "$display" ] || { echo "sway never reported a display"; tail "$workdir/sway.log"; exit 1; }

echo ">> nested sway is up on $display; spawning $ghosts (click it, drag it, right-click to type at it)"
# The control socket lives in the throwaway runtime dir, so the command a
# person copies has to point at it too.
echo ">> from another terminal, talk to it with:"
echo ">>     HAUNT_RUNTIME_DIR=$runtime go run ./cmd/haunt say \"hello\""
echo ">> and read back why it said that with:"
echo ">>     go run ./cmd/haunt history -state $workdir/data/haunt/ghosts/<name> -why"
if [ "$ghost_count" -gt 1 ]; then
    echo ">> two ghosts. \`say\` with no -ghost goes to whichever you last clicked:"
    echo ">>     go run ./cmd/haunt say \"who am I talking to\""
    echo ">>   or address one by name, with -ghost whisper"
    echo ">>   A ghost that has been run before comes back to where it was left, so"
    echo ">>   to watch the second one *step aside* from a shared default corner,"
    echo ">>   start from a clean slate:  XDG_DATA_HOME=\$(mktemp -d) make demo-troupe"
fi
if [[ " $ghosts " == *" wisp "* ]]; then
    echo ">> wisp runs on hermes. try:  say \"remind me in two minutes to stretch\"  |  say \"draw a whale\"  |  double-click for the menu"
    echo ">>     journal:  go run ./cmd/haunt journal -state $workdir/data/haunt/ghosts/wisp"
fi
run_args=(run)
if [ "$ghost_count" -eq 1 ]; then
    # Where to open is a question about one ghost; with several, the
    # driver arbitrates and the flag is refused.
    run_args+=(-x 200 -y 150)
fi
if [ -n "$idle_ms" ]; then
    echo ">> idle threshold: ${idle_ms}ms (leave the window alone to see it speak first)"
    run_args+=(-idle-ms "$idle_ms")
fi
if [ -n "$goal_delay" ]; then
    echo ">> checking on goals ${goal_delay} after going idle"
    run_args+=(-goal-check-delay "$goal_delay")
fi
# The ghost's memory and cache go in the workdir, not the real library: a
# demo is for looking at, and an imported shell you are trying out has no
# business leaving a state directory behind under somebody's name. The same
# goes for its hermes profile: a hermes home of its own, so a demo ghost
# neither resumes the last demo's conversation nor writes into the real
# ghost of the same name. ([hermes] home in the config would override it.)
XDG_DATA_HOME="$workdir/data" HAUNT_RUNTIME_DIR="$runtime" HERMES_HOME="$workdir/hermes" \
    WAYLAND_DISPLAY="$display" "$workdir/haunt" "${run_args[@]}" "${ghost_dirs[@]}" &

wait "$sway_pid"
