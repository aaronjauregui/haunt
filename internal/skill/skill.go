// Package skill hosts skills in the agentskills format: a directory with
// a SKILL.md whose front matter names and describes it. Haunt's own
// skills are embedded; a ghost's learned ones live under its directory
// and the model writes them. The host renders the index the prompt
// carries, serves a skill's body on demand, and curates learned skills
// that go unused.
//
// See docs/ghosts/persona-and-skills.md for the format.
package skill

import (
	"io/fs"
	"time"
)

// Skill is one skill as the host sees it.
type Skill struct {
	Name        string
	Description string
	Dir         string
	Learned     bool
	Stale       bool
}

// Host serves skills to the brain.
type Host interface {
	Index() string // names and 60-char descriptions
	View(name string) (string, error)
	Write(name, body string) error // learned skills only
	Curate(now time.Time) error
	List() []Skill
}

// Open loads the embedded skills and the learned ones under learnedDir,
// hiding any whose required tools are not in toolsAvailable.
func Open(embedded fs.FS, learnedDir string, toolsAvailable []string) (Host, error) {
	panic("unimplemented")
}
