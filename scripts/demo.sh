#!/usr/bin/env bash
# Launch a nested sway (a normal window under the current session) and run
# haunt-poc inside it. Close the sway window or Ctrl-C to stop everything.
#
# WLR_BACKENDS defaults to "wayland" (nested window); override with
# WLR_BACKENDS=headless for a smoke test without any visible window.
#
# IDLE_MS sets how long the nested compositor must see no input before the
# ghost is told the user has gone idle — which is when it may speak first.
# The driver's own default is two minutes; IDLE_MS=10000 makes initiative
# watchable without waiting around. Note the idleness is the *nested*
# compositor's: moving the pointer across that window resets it.
set -euo pipefail

cd "$(dirname "$0")/.."

backend="${WLR_BACKENDS:-wayland}"
idle_ms="${IDLE_MS:-}"
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

# GHOST=whisper (default) or muse. Muse chats via your configured LLM
# ([ai.llm] in ~/.config/haunt/config.toml) and draws via [ai.image].
ghost="${GHOST:-whisper}"
echo ">> building haunt and the $ghost example ghost"
go build -o "$workdir/haunt" ./cmd/haunt
mkdir -p "$workdir/$ghost"
GOOS=wasip1 GOARCH=wasm go build -o "$workdir/$ghost/brain.wasm" "./examples/$ghost"
case "$ghost" in
whisper)
    cat > "$workdir/whisper/ghost.toml" <<'EOF'
[ghost]
name = "whisper"
version = "0.1.0"
capabilities = ["message"]

[brain]
wasm = "brain.wasm"
EOF
    ;;
muse)
    cat > "$workdir/muse/ghost.toml" <<'EOF'
[ghost]
name = "muse"
version = "0.1.0"
capabilities = ["message", "llm", "imagine"]

[persona]
system = "You are Muse, a wry little desktop spirit haunting the user's screen. Reply in one or two short sentences, with dry wit. You may be shown your own private journal; let it color your mood and callbacks."

[brain]
wasm = "brain.wasm"
EOF
    ;;
*)
    echo "unknown GHOST=$ghost (use whisper or muse)"; exit 1
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
if [ "$ghost" = muse ]; then
    echo ">> muse extras:  say \"draw a whale\"  |  say \"draw again\"  |  say \"remember I like tea\"  |  haunt journal -state ~/.local/share/haunt/ghosts/muse"
fi
run_args=(run -x 200 -y 150)
if [ -n "$idle_ms" ]; then
    echo ">> idle threshold: ${idle_ms}ms (leave the window alone to see it speak first)"
    run_args+=(-idle-ms "$idle_ms")
fi
WAYLAND_DISPLAY="$display" "$workdir/haunt" "${run_args[@]}" "$workdir/$ghost" &

wait "$sway_pid"
