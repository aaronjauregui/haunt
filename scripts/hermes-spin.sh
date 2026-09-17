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
#     web or mail);
#   - the morning check-in: a `morning` occasion a few minutes in, which
#     loads haunt's morning-check-in skill and asks for the one thing
#     today; the spin answers "I'll tell you" and types a goal, which
#     shows in the intentions list;
#   - the focus session: "did something first: skill_view, …" (it loaded
#     haunt's focus-session skill), then an `intend` for the session,
#     which shows in the intentions list;
#   - the end of the session: time is up, "How did it go?" (the spin
#     answers "Made progress"), `resolve`, then the offer of a break
#     (the spin answers "I'm done");
#   - the weekly review: `skill_view` of weekly-review, `intentions`, a
#     question per goal (the spin keeps each) and one about next week
#     (the spin says nothing new);
#   - the goal gone quiet: an eight-day-old goal seeded by
#     scripts/spinseed is raised when the spin presses a key; the ghost
#     loads keep-an-eye-on-a-goal and asks "Still want this?" (the spin
#     answers "Later", which files an errand three days out);
#   - humanizer: `skill_view humanizer`, one of hermes's bundled skills;
#   - the shopping list: `skill_view shopping-list` and a call to the
#     notes server's tool, which writes to the notes file, plus the
#     driver's warning about the misspelled-notes skill;
#   - the ghost story: `skill_view` of wisp's own ghost-story skill, which
#     `journal -skills` lists under "From its package".
#
# The ghost's questions are answered by scripts/spinkeys, a virtual
# keyboard on the demo's headless sway, picking the option that matches.
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

go build -o "$out/spinkeys" ./scripts/spinkeys || exit 1
go build -o "$out/spinseed" ./scripts/spinseed || exit 1

# The morning occasion, seven minutes out: after hermes is up and the
# first turns are over, most of the time. If hermes is slower, the occasion
# waits for the turn in flight.
morning=$(( $(date +%s) + 420 ))
morning_cron="$(date -d "@$morning" +'%-M %-H') * * *"

# Debug for balloon and input, because the spin waits for their "choices
# offered" and "input line opened" lines to know a question is up.
GHOST=wisp IDLE_MS="${IDLE_MS:-45000}" WLR_BACKENDS=headless HAUNT_LOG="${HAUNT_LOG:-info}" \
    HAUNT_DEBUG="${HAUNT_DEBUG:-balloon,input}" WISP_MORNING_CRON="$morning_cron" \
    WISP_NOTES="$out/notes.txt" SEED_STATE="$out/spinseed" GOAL_DELAY=5s \
    ./scripts/demo.sh > "$out/demo.log" 2>&1 &
demo=$!
stop() {
    [ -n "${KEYS_PID:-}" ] && kill "$KEYS_PID" 2>/dev/null
    # A background job ignores SIGINT; the demo cleans up on TERM too.
    kill -TERM "$demo" 2>/dev/null
    wait "$demo" 2>/dev/null
}
trap 'stop; exit 1' INT TERM

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
display=$(sed -n 's/.*nested sway is up on \(\S*\);.*/\1/p' "$out/demo.log" | head -1)
export HAUNT_RUNTIME_DIR="$runtime" HERMES_HOME="$work/hermes"
haunt() { "$work/haunt" "$@" 2>&1; }
step() { printf '\n=== %s %s\n' "$(date +%T)" "$*"; }

coproc KEYS { "$out/spinkeys" -display "$display"; }
keys() {
    echo "$*" >&"${KEYS[1]}"
    local reply
    read -r reply <&"${KEYS[0]}"
    [ "$reply" = ok ] || echo "spinkeys: $* -> $reply"
}

# The ghost's questions and input lines, numbered from the start of the
# run. Each answer takes the next one, so a question already up when the
# spin gets to it (an occasion fires on the minute) is not missed.
asked=0
typed=0

# answer PATTERN [SECONDS]: wait for the ghost's next question, and pick
# the first choice matching PATTERN (case-insensitive), or the first choice
# if none does.
answer() {
    local pattern=$1 wait=${2:-120} i line opts n=1
    for ((i = 0; i < wait; i++)); do
        line=$(grep 'choices offered:' "$out/demo.log" | sed -n "$((asked + 1))p")
        [ -n "$line" ] && break
        sleep 1
    done
    if [ -z "$line" ]; then
        echo "(no question within ${wait}s)"
        return 1
    fi
    asked=$((asked + 1))
    opts=${line#*choices offered: }
    IFS='|' read -ra choices <<< "$opts"
    for i in "${!choices[@]}"; do
        if [[ "${choices[$i],,}" =~ ${pattern,,} ]]; then
            n=$((i + 1))
            break
        fi
    done
    sleep 2 # as a person would: read it first
    echo "asked: $opts -> answering $n"
    keys key "$n"
}

# typein TEXT [SECONDS]: wait for the ghost's next input line (the
# ask_text tool), and type TEXT into it.
typein() {
    local text=$1 wait=${2:-60} i
    for ((i = 0; i < wait; i++)); do
        [ "$(grep -c 'input line opened' "$out/demo.log")" -gt "$typed" ] && break
        sleep 1
    done
    if [ "$i" -ge "$wait" ]; then
        echo "(no input line within ${wait}s)"
        return 1
    fi
    typed=$((typed + 1))
    sleep 2
    echo "typing: $text"
    keys type "$text"
    keys enter
}

step "boot and one idle turn"
sleep 60

step "a goal gone quiet (haunt's keep-an-eye-on-a-goal skill)"
# spinseed left an eight-day-old goal that was never seen. The goal check
# ran when the compositor went idle; a key press brings the person back,
# which is when the driver raises it.
grep 'a goal has gone quiet' "$out/demo.log" || echo "(the goal check found nothing)"
keys enter
answer "later" 180
sleep 30
haunt journal -intentions -state "$state"

step "the morning check-in (haunt's skill, from a cron occasion at $(date -d "@$morning" +%H:%M))"
now=$(date +%s)
[ "$now" -lt "$morning" ] && sleep $((morning - now))
answer "tell" 240 && typein "finish the quarterly report"
sleep 45
haunt journal -intentions -state "$state"

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

step "a focus session (haunt's skill)"
# Length and subject are both given, so the skill has nothing to ask
# before it starts.
haunt say -ghost wisp "let's do a 2 minute focus session on the slides"
sleep 90
haunt journal -intentions -state "$state"

step "the end of the focus session"
answer "progress" 180 && answer "done" 120
sleep 45
haunt journal -intentions -state "$state"

step "a ghost story (wisp's own skill)"
haunt say -ghost wisp "tell me a ghost story"
sleep 90

step "humanizer (a bundled hermes skill)"
haunt say -ghost wisp "make this sound less like an AI wrote it: In today's fast-paced digital landscape, it is crucial to leverage synergies."
sleep 90

step "a shopping list (a skill that needs a tool server's tool)"
haunt say -ghost wisp "put oat milk on my shopping list"
sleep 90
echo "notes file:"
cat "$out/notes.txt" 2>/dev/null || echo "(nothing written)"
grep 'hermes will not list the skill' "$out/demo.log" || echo "(no warning about the misspelled skill)"

step "a weekly review (haunt's skill)"
# Keep every goal, and nothing new: answer each question until none comes.
haunt say -ghost wisp "let's do a weekly review"
answer "keep|nothing|^ *no" 180 && while answer "keep|nothing|^ *no" 60; do :; done
sleep 30
haunt journal -intentions -state "$state"
haunt journal -skills -state "$state" wisp

step "the conversation, with reasons"
haunt history -n 80 -why -state "$state"

stop
if docker ps -q --filter name=hermes- 2>/dev/null | grep -q .; then
    echo "note: a hermes container is still running (docker ps)" >&2
fi
