#!/usr/bin/env bash
# Record the demo as the GIF the README opens with.
#
# Runs scripts/demo.sh, captures the nested sway window with wf-recorder,
# and encodes the capture as a GIF. Recording starts and stops on Enter, so
# the take is driven by hand: a hermes ghost is half a minute of "waking
# up…" before it says anything, and that half minute does not belong in the
# GIF.
#
# The raw capture is kept beside the GIF, and RAW= reads it back, so
# finding the cut costs no further recording:
#
#   make demo-record GHOST=wisp             # perform a take
#   make demo-record RAW=docs/media/demo-raw.mp4 \
#        SEGMENTS=... CROP=...              # cut it, no re-record
#
# CROP="w:h:x:y" crops before encoding. A ghost and its balloon occupy a
# small part of a 1280x720 screen, and a GIF scaled down from the whole
# screen renders the balloon's text too small to read; cropping to the
# action and encoding at that size keeps the text at its own resolution.
#
# SEGMENTS="start-end,start-end,…" keeps only those spans, in that order,
# joined into one clip. A model's thinking is dead time on screen and a cut
# through it is invisible as long as both sides are the same still frame —
# so cut where nothing moves, never mid-gesture.
#
# GHOST picks who is recorded, as in demo.sh ("whisper", "wisp", or both).
set -euo pipefail

cd "$(dirname "$0")/.."

out="${OUT:-docs/media/demo.gif}"
raw_keep="${RAW:-}"
fps="${GIF_FPS:-12}"
max="${RECORD_MAX:-300}"
crop="${CROP:-}"
segments="${SEGMENTS:-}"

# A crop is already the size that was chosen, so it is encoded at its own
# resolution; an uncropped screen is scaled down to something a README can
# carry. GIF_WIDTH overrides either way.
if [ -n "${GIF_WIDTH:-}" ]; then
    width="$GIF_WIDTH"
elif [ -n "$crop" ]; then
    width=""
else
    width=900
fi

need() { command -v "$1" >/dev/null || { echo "need $1: $2"; exit 2; }; }
need ffmpeg "your package manager"

mkdir -p "$(dirname "$out")"

# Encode a capture to a GIF: the segments joined, cropped, then two palette
# passes in one graph. A single pass would hand GIF's 256 colours to
# ffmpeg's fixed palette instead, and a ghost's anti-aliased edges are the
# first thing that costs.
encode() {
    local src="$1" graph="" labels="" chain="" n=0 span start end
    if [ -n "$segments" ]; then
        local IFS=','
        for span in $segments; do
            start="${span%%-*}"; end="${span##*-}"
            [ "$start" != "$span" ] && [ -n "$end" ] || {
                echo "bad segment \"$span\": want start-end, e.g. 7.9-10.2"; exit 2; }
            graph="${graph}[0:v]trim=${start}:${end},setpts=PTS-STARTPTS[g${n}];"
            labels="${labels}[g${n}]"
            n=$((n + 1))
        done
        graph="${graph}${labels}concat=n=${n}:v=1[cut];"
    else
        graph="[0:v]null[cut];"
    fi

    [ -n "$crop" ] && chain="crop=${crop},"
    chain="${chain}fps=${fps}"
    [ -n "$width" ] && chain="${chain},scale=${width}:-1:flags=lanczos"

    ffmpeg -loglevel error -y -i "$src" -filter_complex \
        "${graph}[cut]${chain},split[p0][p1];\
[p0]palettegen=stats_mode=diff[pal];\
[p1][pal]paletteuse=dither=bayer:bayer_scale=3[v]" \
        -map "[v]" "$out"
    echo ">> $out ($(du -h "$out" | cut -f1), $(ffprobe -v error \
        -show_entries format=duration -of csv=p=0 "$out" | cut -c1-5)s)"
}

# RAW= re-cuts a take that is already on disk.
if [ -n "$raw_keep" ]; then
    [ -f "$raw_keep" ] || { echo "no such capture: $raw_keep"; exit 2; }
    echo ">> re-encoding $raw_keep"
    encode "$raw_keep"
    exit 0
fi

need wf-recorder "https://github.com/ammen99/wf-recorder"
need sway "your package manager"

work="$(mktemp -d /tmp/haunt-rec-XXXXXX)"
log="$work/demo.log"
demo_pid=""
rec_pid=""

# Stop the recorder and give it time to close the container, which is what
# makes the capture playable. SIGINT is the one it finalises on, and it can
# only receive it because it is started under `set -m` below: bash sets
# SIGINT to SIG_IGN in an asynchronous command when job control is off, so
# without that the interrupt is discarded and the wait never returns.
stop_recording() {
    [ -n "$rec_pid" ] || return 0
    kill -INT "$rec_pid" 2>/dev/null || true
    for _ in $(seq 1 100); do
        kill -0 "$rec_pid" 2>/dev/null || break
        sleep 0.1
    done
    if kill -0 "$rec_pid" 2>/dev/null; then
        echo ">> the recorder ignored SIGINT; the capture may be truncated"
        kill -TERM "$rec_pid" 2>/dev/null || true
        sleep 1
        kill -KILL "$rec_pid" 2>/dev/null || true
    fi
    wait "$rec_pid" 2>/dev/null || true
    rec_pid=""
}

cleanup() {
    stop_recording
    [ -n "$demo_pid" ] && kill -TERM "$demo_pid" 2>/dev/null || true
    wait 2>/dev/null || true
    rm -rf "$work"
}
trap cleanup EXIT TERM INT

echo ">> starting the demo (GHOST=${GHOST:-whisper})"
./scripts/demo.sh >"$log" 2>&1 &
demo_pid=$!

# demo.sh announces the nested display once sway is up; it is the only way
# in, since the workdir it builds is its own.
display=""
for _ in $(seq 1 600); do
    display="$(sed -n 's/^>> nested sway is up on \([^;]*\);.*/\1/p' "$log" | head -1)"
    [ -n "$display" ] && break
    kill -0 "$demo_pid" 2>/dev/null || { echo "the demo exited early:"; cat "$log"; exit 1; }
    sleep 0.1
done
[ -n "$display" ] || { echo "the demo never reported a display:"; cat "$log"; exit 1; }

cat <<MSG

>> the ghost is up in the nested sway window, on $display
>> Watch it. When it has finished waking up and you are ready to perform,
>> come back here and press Enter to START recording.

MSG
read -r _

raw="$work/raw.mp4"
# set -m: see stop_recording. Job control puts the recorder in a process
# group of its own with the default disposition for SIGINT, which is the
# only signal it finalises the file on.
set -m
WAYLAND_DISPLAY="$display" wf-recorder -f "$raw" >"$work/rec.log" 2>&1 &
rec_pid=$!
set +m
sleep 1
kill -0 "$rec_pid" 2>/dev/null || { echo "wf-recorder failed:"; cat "$work/rec.log"; exit 1; }

echo ">> RECORDING. Press Enter to stop (or it stops itself after ${max}s)."
read -r -t "$max" _ || echo ">> hit the ${max}s limit"

echo ">> stopping"
stop_recording
[ -s "$raw" ] || { echo "nothing was captured:"; cat "$work/rec.log"; exit 1; }

# The capture outlives the workdir: the first take is rarely the cut.
keep="$(dirname "$out")/$(basename "${out%.*}")-raw.mp4"
cp "$raw" "$keep"
echo ">> capture kept at $keep"
encode "$raw"
echo ">> recut it without recording again, e.g.:"
echo ">>     make demo-record RAW=$keep \\"
echo ">>         CROP=580:300:180:100 SEGMENTS=7.9-10.2,11.9-12.8"
