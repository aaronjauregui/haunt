# Doing things

A ghost can act, not only talk. Its manifest declares **MCP tool servers**,
the same servers editors and terminal agents use, and haunt runs them, so
the ghost never holds a credential and nothing gets past the budgets.
There is no integration code in haunt: a calendar is an MCP server, a
smart home is an MCP server, and so is whatever you use next year.

The install sheet lists which server programs a package would run on your
machine and which environment variables it would pass through. Each
individual tool is still asked about separately, the first time it is
used ([What it may do](permissions.md)).

## Scheduled polls

A tool is normally reached for by the model mid-turn. A **trigger** in the
manifest is the other direction: haunt asks a tool on a schedule and tells
the ghost what it heard, for example a calendar every fifteen minutes.

A poll never speaks. What it does is give the ghost something it now means
to raise with you, which survives a closed laptop, a restart and a quiet
hour. It shows nothing on screen, writes nothing in the journal, and does
nothing at all when the answer is the same as last time. The tool it
calls is consented to like any other, in a question that says it is
asking about a standing arrangement, and only while you are at the desk.

## Presence

A trigger with `quiet_when` is a presence check. When the tool's answer
contains that text, the ghost keeps quiet, which is a better answer to
"should this be talking" than a fixed evening window. It wears off by
itself after half an hour, so a laptop that stops asking gets its ghost
back rather than staying mute.

How to declare servers, triggers and presence checks is in the creator
guide: [Tools and triggers](../ghosts/tools-and-triggers.md).
