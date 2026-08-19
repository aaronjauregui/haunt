#!/usr/bin/env bash
# Launch a nested sway (a normal window under the current session) and run
# haunt-poc inside it. Close the sway window or Ctrl-C to stop everything.
#
# WLR_BACKENDS defaults to "wayland" (nested window); override with
# WLR_BACKENDS=headless for a smoke test without any visible window.
#
# IDLE_MS sets how long the nested compositor must see no input before the
# ghost is told the user has gone idle — which is when it may speak first,
# and when it may consolidate its memories. The driver's own default is two
# minutes; IDLE_MS=10000 makes initiative watchable without waiting around.
# Note the idleness is the *nested* compositor's: moving the pointer across
# that window resets it.
#
# SLEEP_DELAY and SLEEP_AGE shorten the two waits in front of memory
# consolidation (normally 5m after going idle, over memories at least 48h
# old). SLEEP_DELAY=5s SLEEP_AGE=1s makes the whole thing watchable in a
# sitting; see "testing consolidation" in DESIGN.md.
set -euo pipefail

cd "$(dirname "$0")/.."

backend="${WLR_BACKENDS:-wayland}"
idle_ms="${IDLE_MS:-}"
sleep_delay="${SLEEP_DELAY:-}"
sleep_age="${SLEEP_AGE:-}"
workdir="$(mktemp -d /tmp/haunt-demo-XXXXXX)"
# Kill child processes (not the whole group: that would kill this script
# before the rm runs), then remove the workdir.
cleanup() {
    kill $(jobs -p) 2>/dev/null || true
    rm -rf "$workdir"
}
trap cleanup EXIT TERM INT

cat > "$workdir/sway.cfg" <<'EOF'
output * bg #24283b solid_color
EOF

# GHOST=whisper (default) or wisp. Whisper is the scripted example brain,
# built to WASM; wisp declares no brain at all and so runs on the driver's
# built-in one, chatting via your configured LLM ([ai.llm] in
# ~/.config/haunt/config.toml) and drawing via [ai.image].
ghost="${GHOST:-whisper}"
echo ">> building haunt and the $ghost ghost"
go build -o "$workdir/haunt" ./cmd/haunt
mkdir -p "$workdir/$ghost"
case "$ghost" in
whisper)
    GOOS=wasip1 GOARCH=wasm go build -o "$workdir/whisper/brain.wasm" ./examples/whisper
    cat > "$workdir/whisper/ghost.toml" <<'EOF'
[ghost]
name = "whisper"
version = "0.1.0"
capabilities = ["message"]

[brain]
wasm = "brain.wasm"
EOF
    ;;
wisp)
    # No [brain] section and nothing to compile: art and a persona are a
    # whole ghost, and the driver's built-in brain runs it.
    cat > "$workdir/wisp/ghost.toml" <<'EOF'
[ghost]
name = "wisp"
version = "0.1.0"
capabilities = ["message", "llm", "imagine", "window"]

[persona]
system = "You are Wisp, a wry little desktop spirit haunting the user's screen. Reply in one or two short sentences, with dry wit. You may be shown your own private journal; let it color your mood and callbacks."
EOF
    ;;
*)
    echo "unknown GHOST=$ghost (use whisper or wisp)"; exit 1
    ;;
esac

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

echo ">> nested sway is up on $display; spawning $ghost (click it, drag it, right-click to type at it)"
echo ">> from another terminal, talk to it with:  go run ./cmd/haunt say \"hello\""
if [ "$ghost" = wisp ]; then
    echo ">> wisp extras:  say help  |  say \"draw a whale\"  |  say \"draw again\"  |  say \"remember I like tea\"  |  haunt journal -state ~/.local/share/haunt/ghosts/wisp"
fi
run_args=(run -x 200 -y 150)
if [ -n "$idle_ms" ]; then
    echo ">> idle threshold: ${idle_ms}ms (leave the window alone to see it speak first)"
    run_args+=(-idle-ms "$idle_ms")
fi
if [ -n "$sleep_delay" ]; then
    echo ">> consolidating ${sleep_delay} after going idle"
    run_args+=(-consolidate-delay "$sleep_delay")
fi
if [ -n "$sleep_age" ]; then
    echo ">> consolidating memories older than ${sleep_age}"
    run_args+=(-consolidate-after "$sleep_age")
fi
WAYLAND_DISPLAY="$display" "$workdir/haunt" "${run_args[@]}" "$workdir/$ghost" &

wait "$sway_pid"
