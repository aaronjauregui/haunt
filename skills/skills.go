// Package skills carries haunt's own skills: routines any ghost can
// follow, written against the ghost's tools. internal/skill reads them
// from the embedded FS, lists them in the prompt's skill index, and serves
// one's body when the model views it.
//
// One directory per skill, holding a SKILL.md and nothing else, in the
// agentskills format (docs/ghosts/persona-and-skills.md). A skill that
// cannot be followed without a tool beyond speaking names those tools
// under `metadata.haunt.requires_tools`, and the index hides it from a
// ghost that lacks one, so a ghost without the web is never told how to
// search it.
package skills

import "embed"

// Haunt is the skills, rooted so each skill is a top-level directory.
// Only SKILL.md files are embedded: a skill here is prose, never code.
//
//go:embed */SKILL.md
var Haunt embed.FS
