#!/usr/bin/env bash
# Launch a nested sway (a normal window under the current session) and run
# haunt-poc inside it. Close the sway window or Ctrl-C to stop everything.
#
# WLR_BACKENDS defaults to "wayland" (nested window); override with
# WLR_BACKENDS=headless for a smoke test without any visible window.
set -euo pipefail

cd "$(dirname "$0")/.."

backend="${WLR_BACKENDS:-wayland}"
workdir="$(mktemp -d /tmp/haunt-demo-XXXXXX)"
trap 'kill 0 2>/dev/null; rm -rf "$workdir"' EXIT

cat > "$workdir/sway.cfg" <<'EOF'
output * bg #24283b solid_color
EOF

echo ">> building haunt-poc"
go build -o "$workdir/haunt-poc" ./cmd/haunt-poc

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

echo ">> nested sway is up on $display; spawning ghost (drag it with the mouse)"
WAYLAND_DISPLAY="$display" "$workdir/haunt-poc" -x 200 -y 150 &

wait "$sway_pid"
