// Package skills carries haunt's own hermes skills: routines a ghost on
// hermes can follow, written against its haunt tools (docs/dev/hermes.md,
// "Skills"). The driver installs them into every ghost's profile, read-only,
// so hermes lists them in the prompt and the model loads one with skill_view
// when it applies.
//
// One directory per skill, holding a SKILL.md and nothing else. Each skill
// declares `requires_toolsets: [mcp-haunt]`: hermes files MCP tools under
// `mcp-<server>`, and the index hides a skill whose toolsets are missing,
// so these never show outside a ghost's profile.
package skills

import "embed"

// Haunt is the skills, rooted so each skill is a top-level directory.
// Only SKILL.md files are embedded: a skill here is prose, never code.
//
//go:embed */SKILL.md
var Haunt embed.FS
