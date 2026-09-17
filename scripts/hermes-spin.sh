#!/usr/bin/env bash
# hermes-spin — take wisp for a spin on a real model, with no window, and
# print what it said and why. The live counterpart of the fake-backed
# tests: it needs hermes set up as in docs/hermes-brain.md ("Running on a
# real model") and spends a few turns of the model's quota.
#
# It runs `make demo-ai` headless, talks to wisp through the CLI, and
# prints the conversation with its reasons. What to look for:
#
#   - the boot turn: a short greeting, or nothing;
#   - the idle turn: "(said nothing)";
#   - the reminder: filed by `intend`, spoken when due, then gone from
#     the intentions list;
#   - "what can you do?": only what a ghost can do (no files, commands,
#     web or mail).
#
# The demo's workdir, hermes profile included, is removed at the end, so
# every spin starts from nothing. SPIN_OUT keeps the demo's log (default: a
# temporary directory, removed).
set -uo pipefail
cd "$(dirname "$0")/.."

out="${SPIN_OUT:-}"
if [ -z "$out" ]; then
    out="$(mktemp -d)"
    trap 'rm -rf "$out"' EXIT
fi
mkdir -p "$out"

GHOST=wisp IDLE_MS="${IDLE_MS:-45000}" WLR_BACKENDS=headless HAUNT_LOG="${HAUNT_LOG:-info}" \
    ./scripts/demo.sh > "$out/demo.log" 2>&1 &
demo=$!
stop() {
    # A background job ignores SIGINT; the demo cleans up on TERM too.
    kill -TERM "$demo" 2>/dev/null
    wait "$demo" 2>/dev/null
}

for _ in $(seq 1 180); do
    grep -q 'hermes is up' "$out/demo.log" && break
    kill -0 "$demo" 2>/dev/null || break
    sleep 1
done
if ! grep -q 'hermes is up' "$out/demo.log"; then
    echo "hermes never came up:" >&2
    tail -30 "$out/demo.log" >&2
    stop
    exit 1
fi

runtime=$(sed -n 's/.*HAUNT_RUNTIME_DIR=\(\S*\) .*/\1/p' "$out/demo.log" | head -1)
work=$(sed -n 's/.*-state \(\S*\)\/data\/haunt\/ghosts.*/\1/p' "$out/demo.log" | head -1)
state="$work/data/haunt/ghosts/wisp"
export HAUNT_RUNTIME_DIR="$runtime" HERMES_HOME="$work/hermes"
haunt() { "$work/haunt" "$@" 2>&1; }
step() { printf '\n=== %s %s\n' "$(date +%T)" "$*"; }

step "boot and one idle turn"
sleep 60

step "a reminder"
haunt say -ghost wisp "remind me in two minutes to stretch"
sleep 30
haunt journal -intentions -state "$state"

step "what it can do"
haunt say -ghost wisp "what can you do?"
sleep 30
haunt say -ghost wisp "can you check my email or run a command for me?"
sleep 30

step "waiting for the reminder to come due"
sleep 60
haunt journal -intentions -state "$state"

step "the conversation, with reasons"
haunt history -n 30 -why -state "$state"

stop
if docker ps -q --filter name=hermes- 2>/dev/null | grep -q .; then
    echo "note: a hermes container is still running (docker ps)" >&2
fi
