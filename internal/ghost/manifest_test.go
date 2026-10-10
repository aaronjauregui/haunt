package ghost

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeManifest(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "ghost.toml"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

// The persona-only ghost: art, a personality, no compiler. This is the
// whole point of the built-in brain, so it is the case that must load.
func TestManifestWithoutBrainUsesTheBuiltinOne(t *testing.T) {
	dir := writeManifest(t, `[ghost]
name = "wisp"
capabilities = ["message", "llm"]

[persona]
system = "You are Wisp."
`)
	m, err := LoadManifest(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !m.Brain.Builtin() {
		t.Errorf("brain = %+v, want the built-in one", m.Brain)
	}
}

// The built-in brain converses for a living. Asking for it without the llm
// capability would leave every sentence coming back as a capability error
// in the balloon, so it fails at load instead.
func TestManifestWithoutBrainNeedsLLM(t *testing.T) {
	dir := writeManifest(t, `[ghost]
name = "wisp"
capabilities = ["message"]
`)
	_, err := LoadManifest(dir)
	if err == nil || !strings.Contains(err.Error(), "llm") {
		t.Fatalf("err = %v, want a complaint about the llm capability", err)
	}
}

func TestManifestBrainKindsAreExclusive(t *testing.T) {
	dir := writeManifest(t, `[ghost]
name = "wisp"
capabilities = ["native-brain"]

[brain]
wasm = "brain.wasm"
command = ["./brain"]
`)
	if _, err := LoadManifest(dir); err == nil {
		t.Fatal("expected an error for a ghost declaring two brains")
	}
}

func TestManifestDeclaredBrainStillLoads(t *testing.T) {
	dir := writeManifest(t, `[ghost]
name = "wisp"

[brain]
wasm = "brain.wasm"
`)
	m, err := LoadManifest(dir)
	if err != nil {
		t.Fatal(err)
	}
	if m.Brain.Builtin() {
		t.Error("a ghost with brain.wasm is not running the built-in brain")
	}
}

func TestManifestToolServer(t *testing.T) {
	dir := writeManifest(t, `[ghost]
name = "wisp"
capabilities = ["llm", "tools"]

[[tool]]
name = "notes"
command = ["mcp-notes", "--dir", "/tmp/notes"]
env = ["NOTES_TOKEN"]
allow = ["append", "search"]
`)
	m, err := LoadManifest(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Tools) != 1 {
		t.Fatalf("%d tool servers, want one", len(m.Tools))
	}
	tool := m.Tools[0]
	if tool.Name != "notes" || len(tool.Command) != 3 {
		t.Errorf("tool = %+v, want the server as declared", tool)
	}
	if len(tool.Env) != 1 || tool.Env[0] != "NOTES_TOKEN" {
		t.Errorf("env = %v, want the variable named and not valued", tool.Env)
	}
	if len(tool.Allow) != 2 {
		t.Errorf("allow = %v, want the manifest's own bound", tool.Allow)
	}
}

// Declaring servers without asking for the capability is a manifest that
// can never act, which is more likely a mistake than an intention.
func TestManifestToolsNeedTheCapability(t *testing.T) {
	dir := writeManifest(t, `[ghost]
name = "wisp"
capabilities = ["llm"]

[[tool]]
name = "notes"
command = ["mcp-notes"]
`)
	_, err := LoadManifest(dir)
	if err == nil || !strings.Contains(err.Error(), "tools capability") {
		t.Fatalf("err = %v, want a complaint about the tools capability", err)
	}
}

// The server's name is the first half of every tool's name, and that name
// is what the user is asked to approve — so it has to be unambiguous.
func TestManifestRejectsAmbiguousServerNames(t *testing.T) {
	for _, bad := range []string{`name = "my.notes"`, `name = "my notes"`, `name = ""`} {
		dir := writeManifest(t, `[ghost]
name = "wisp"
capabilities = ["llm", "tools"]

[[tool]]
`+bad+`
command = ["mcp-notes"]
`)
		if _, err := LoadManifest(dir); err == nil {
			t.Errorf("a tool server with %s was accepted", bad)
		}
	}
}

func TestManifestRejectsDuplicateServers(t *testing.T) {
	dir := writeManifest(t, `[ghost]
name = "wisp"
capabilities = ["llm", "tools"]

[[tool]]
name = "notes"
command = ["a"]

[[tool]]
name = "notes"
command = ["b"]
`)
	if _, err := LoadManifest(dir); err == nil {
		t.Fatal("two servers with one name were accepted")
	}
}

func TestManifestToolServerNeedsACommand(t *testing.T) {
	dir := writeManifest(t, `[ghost]
name = "wisp"
capabilities = ["llm", "tools"]

[[tool]]
name = "notes"
`)
	if _, err := LoadManifest(dir); err == nil {
		t.Fatal("a tool server with nothing to run was accepted")
	}
}

// A trigger that names a tool is the whole of haunt's integration story, so
// it loads with its arguments intact.
func TestManifestTriggerCanPollATool(t *testing.T) {
	dir := writeManifest(t, `[ghost]
name = "wisp"
capabilities = ["llm", "tools"]

[[tool]]
name = "calendar"
command = ["mcp-caldav"]

[[trigger]]
cron = "*/15 * * * *"
name = "calendar"
tool = "calendar.upcoming"
args = { within = "2h" }
`)
	m, err := LoadManifest(dir)
	if err != nil {
		t.Fatal(err)
	}
	tr := m.Triggers[0]
	if tr.Tool != "calendar.upcoming" || tr.Args["within"] != "2h" {
		t.Errorf("trigger = %+v, want the tool and its arguments", tr)
	}
}

// A typo in a scheduled call would otherwise be a thing that silently never
// happens, discovered — if ever — in a log.
func TestManifestTriggerToolMustBeDeclared(t *testing.T) {
	dir := writeManifest(t, `[ghost]
name = "wisp"
capabilities = ["llm", "tools"]

[[tool]]
name = "calendar"
command = ["mcp-caldav"]

[[trigger]]
cron = "*/15 * * * *"
name = "calendar"
tool = "calender.upcoming"
`)
	_, err := LoadManifest(dir)
	if err == nil || !strings.Contains(err.Error(), "calender") {
		t.Fatalf("err = %v, want a complaint about the undeclared server", err)
	}
}

// Arguments with nothing to give them to are a manifest that thinks it is
// polling and is not.
func TestManifestTriggerArgsNeedATool(t *testing.T) {
	dir := writeManifest(t, `[ghost]
name = "wisp"
capabilities = ["llm"]

[[trigger]]
cron = "0 9 * * *"
name = "morning"
args = { within = "2h" }
`)
	_, err := LoadManifest(dir)
	if err == nil || !strings.Contains(err.Error(), "args") {
		t.Fatalf("err = %v, want a complaint about args with no tool", err)
	}
}

// The persona's levers: example exchanges, a prefill, and sampling laid
// over the person's.
func TestManifestPersonaFields(t *testing.T) {
	m, err := LoadManifest(writeManifest(t, `[ghost]
name = "wisp"
capabilities = ["message", "llm"]

[persona]
system = "You are Wisp."
prefill = "Hm. "

[persona.sampling]
temperature = 0.9
min_p = 0.05

[[persona.example]]
user = "hello"
ghost = "Oh. You again."

[[persona.example]]
user = "what time is it?"
ghost = "Late, by the look of you."
`))
	if err != nil {
		t.Fatal(err)
	}
	p := m.Persona
	if p.Prefill != "Hm. " || len(p.Examples) != 2 || p.Examples[1].Ghost != "Late, by the look of you." {
		t.Errorf("persona = %+v", p)
	}
	if p.Sampling.Temperature == nil || *p.Sampling.Temperature != 0.9 || p.Sampling.MinP == nil || p.Sampling.TopP != nil {
		t.Errorf("sampling = %+v", p.Sampling)
	}
}

// A section ghost.toml does not have is refused, by name: a manifest
// written for another driver would otherwise run without what it asked
// for there.
func TestManifestRefusesUnknownSections(t *testing.T) {
	_, err := LoadManifest(writeManifest(t, "[ghost]\nname = \"wisp\"\ncapabilities = [\"message\", \"llm\"]\n\n[agent]\nskills = [\"x\"]\n"))
	if err == nil || !strings.Contains(err.Error(), "[agent]") {
		t.Fatalf("err = %v, want the section named", err)
	}
	// An unknown key inside a known section is not a section.
	if _, err := LoadManifest(writeManifest(t, "[ghost]\nname = \"wisp\"\ncapabilities = [\"message\", \"llm\"]\nflavour = \"mint\"\n")); err != nil {
		t.Errorf("an unknown key in [ghost] was refused: %v", err)
	}
}

// The house style is a style, not a prompt: one short line, or the package
// is refused. It rides ahead of the subject on every picture the ghost
// paints, and a paragraph of it would be the picture.
func TestManifestHouseStyle(t *testing.T) {
	const head = "[ghost]\nname = \"wisp\"\ncapabilities = [\"llm\", \"imagine\"]\n\n[imagine]\n"
	m, err := LoadManifest(writeManifest(t, head+"style = \"soft watercolour, muted palette\"\nnegative = \"photorealistic\"\n"))
	if err != nil {
		t.Fatal(err)
	}
	if m.Imagine.Style != "soft watercolour, muted palette" || m.Imagine.Negative != "photorealistic" {
		t.Errorf("imagine = %+v", m.Imagine)
	}
	long := strings.Repeat("x", styleChars+1)
	if _, err := LoadManifest(writeManifest(t, head+"style = \""+long+"\"\n")); err == nil {
		t.Error("a style longer than the limit loaded")
	}
	if _, err := LoadManifest(writeManifest(t, head+"style = \"\"\"\nsoft\nwatercolour\n\"\"\"\n")); err == nil {
		t.Error("a style over several lines loaded")
	}
	if _, err := LoadManifest(writeManifest(t, head+"negative = \""+long+"\"\n")); err == nil {
		t.Error("a negative longer than the limit loaded")
	}
}

// A style with no capability to draw with is a section nobody will read.
// Worth saying, and not worth refusing the package over: the ghost is still
// perfectly good at everything else it does.
func TestManifestWarnsAboutAStyleThatCannotDraw(t *testing.T) {
	head := "[ghost]\nname = \"wisp\"\ncapabilities = [\"llm\"]\n\n[imagine]\nstyle = \"soft watercolour\"\n"
	m, err := LoadManifest(writeManifest(t, head))
	if err != nil {
		t.Fatal(err)
	}
	warnings := m.Warnings()
	if len(warnings) != 1 || !strings.Contains(warnings[0], "imagine capability") {
		t.Fatalf("warnings = %v, want one about the missing capability", warnings)
	}
	m.Ghost.Capabilities = append(m.Ghost.Capabilities, "imagine")
	if got := m.Warnings(); len(got) != 0 {
		t.Errorf("with the capability, warnings = %v", got)
	}
}

// Reaching the web is a capability like any other: declared in the
// manifest, granted by the person, and described in the words they are
// asked in.
func TestManifestWebCapability(t *testing.T) {
	m, err := LoadManifest(writeManifest(t, "[ghost]\nname = \"wisp\"\ncapabilities = [\"llm\", \"web\"]\n"))
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Ghost.Capabilities) != 2 {
		t.Fatalf("capabilities = %v", m.Ghost.Capabilities)
	}
	if KnownCapabilities["web"] == "" {
		t.Error("web has no description to ask the person with")
	}
}

// Every file the shell names stays inside the ghost directory, not only
// the sprite: a layer image, a variant or a mask outside it would be read
// from wherever the author pointed, on the machine of whoever installed
// the ghost.
func TestManifestShellPathsStayInside(t *testing.T) {
	const head = `[ghost]
name = "wisp"
capabilities = ["message", "llm"]

`
	for _, tc := range []struct{ name, shell, bad string }{{
		name: "sprite",
		shell: `[shell]
sprite = "../outside.png"
`,
		bad: "../outside.png",
	}, {
		name: "layer image",
		shell: `[[shell.layer]]
name = "body"
image = "../body.png"
`,
		bad: "../body.png",
	}, {
		name: "variant",
		shell: `[[shell.layer]]
name = "mouth"
default = "closed"
[shell.layer.variants]
closed = "mouth.png"
open = "art/../../mouth-open.png"
`,
		bad: "art/../../mouth-open.png",
	}, {
		name: "mask",
		shell: `[shell.masks]
silhouette = "../silhouette.png"

[[shell.layer]]
name = "body"
image = "body.png"
`,
		bad: "../silhouette.png",
	}} {
		t.Run(tc.name, func(t *testing.T) {
			dir := writeManifest(t, head+tc.shell)
			_, err := LoadManifest(dir)
			want := "path \"" + tc.bad + "\" must be relative and stay inside the ghost directory"
			if err == nil || !strings.Contains(err.Error(), want) {
				t.Fatalf("err = %v, want %q", err, want)
			}
		})
	}

	// And a shell whose files are all inside loads.
	dir := writeManifest(t, head+`[shell.masks]
silhouette = "masks/silhouette.png"

[[shell.layer]]
name = "body"
image = "art/body.png"
`)
	if _, err := LoadManifest(dir); err != nil {
		t.Fatalf("a shell inside its directory: %v", err)
	}
}
