// Package hermes is haunt's side of hermes-agent, the ghost's brain.
//
// The driver owns effects — the screen, consent, budgets, quiet hours — and
// hermes owns judgment: what to remember, whether an event is worth a word,
// what to say. This package is the seam. It sets up a ghost's hermes
// profile, runs the gateway that serves it, and carries one turn at a time
// to it: the person's words verbatim, everything else as a one-line
// envelope (see Envelope). What hermes wants done comes back the other way,
// as MCP tool calls through `haunt mcp-serve` and the control socket, and
// never through here.
//
// See docs/hermes-brain.md for the plan this implements.
package hermes
