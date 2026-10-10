//go:build hermescheck

package hermes

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"slices"
	"sort"
	"strings"
	"testing"
)

// personsToolsets are toolsets a ghost has when the person chooses what
// brings them, and haunt leaves on. context_engine comes with a plugin
// context engine set in context.engine: hermes adds it beside an explicit
// list (hermes_cli/tools_config.py _get_platform_tools,
// _context_engine_active) and the agent gives the model the engine's
// tools (agent/agent_init.py _inject_context_engine_tools), as it does a
// memory provider's.
var personsToolsets = []string{"context_engine"}

// imageToolsets is what testdata/toolsets.py prints: every toolset the
// image knows, with every bundled plugin enabled.
type imageToolsets struct {
	Toolsets map[string]struct {
		Tools []string `json:"tools"`
		// DisableRemoves is what naming it in disabled_toolsets subtracts:
		// all its tools, or for a hermes-* bundle only those outside the
		// core list.
		DisableRemoves []string `json:"disable_removes"`
		Plugin         bool     `json:"plugin"`
	} `json:"toolsets"`
	Aliases map[string]string `json:"aliases"`
	Legacy  []string          `json:"legacy"`
	// PlatformGated are toolsets hermes gives only to sessions on the
	// platforms listed, and ClientSurface those a desktop client adds to
	// its own sessions; neither reaches the API server.
	PlatformGated map[string][]string `json:"platform_gated"`
	ClientSurface []string            `json:"client_surface"`
	Plugins       []struct {
		Key          string   `json:"key"`
		Kind         string   `json:"kind"`
		Error        string   `json:"error"`
		Unregistered []string `json:"unregistered"`
	} `json:"plugins"`
}

// TestHermesToolsets checks the profile's toolset restriction against the
// hermes image in $HERMES_IMAGE: run when the pin moves, by `make
// hermes-check`, which needs docker and nothing else (no network, no
// gateway, no model).
//
// A toolset passes when apiServerToolsets, disabledToolsets or
// personsToolsets names it, when hermes keeps it off the API server, or
// when every tool it has is one a ghost is given or one disabledToolsets
// subtracts. Anything else fails: were a later hermes to widen an
// explicit list to it, nothing would take its tools away again. A plugin
// toolset must be named outright, because hermes turns it on beside an
// explicit list. A disabledToolsets entry hermes does not know is a
// warning: it subtracts nothing, and is likely a toolset since renamed.
func TestHermesToolsets(t *testing.T) {
	image := os.Getenv("HERMES_IMAGE")
	if image == "" {
		t.Fatal("HERMES_IMAGE is unset; `make hermes-check` sets it to the pinned image")
	}
	script, err := os.ReadFile("testdata/toolsets.py")
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("docker", "run", "--rm", "-i", "--network", "none",
		"--entrypoint", "/opt/hermes/.venv/bin/python", image, "-")
	cmd.Stdin = bytes.NewReader(script)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("enumerating %s: %v\n%s", image, err, stderr.String())
	}
	var img imageToolsets
	if err := json.Unmarshal(out, &img); err != nil {
		t.Fatalf("reading what %s printed: %v\n%s", image, err, out)
	}

	allowed := map[string]bool{}
	given := map[string]bool{}
	for _, name := range apiServerToolsets {
		allowed[name] = true
		for _, tool := range img.Toolsets[name].Tools {
			given[tool] = true
		}
	}
	denied := map[string]bool{}
	subtracted := map[string]bool{}
	for _, name := range disabledToolsets {
		denied[name] = true
		for _, tool := range img.Toolsets[name].DisableRemoves {
			subtracted[tool] = true
		}
	}
	for tool := range subtracted {
		delete(given, tool)
	}

	names := make([]string, 0, len(img.Toolsets))
	for name := range img.Toolsets {
		names = append(names, name)
	}
	sort.Strings(names)
	var fails []string
	for _, name := range names {
		ts := img.Toolsets[name]
		var verdict string
		switch {
		case allowed[name]:
			verdict = "allowed"
		case denied[name]:
			verdict = "disabled"
		case slices.Contains(personsToolsets, name):
			verdict = "the person's: comes with the context engine they chose"
		case ts.Plugin:
			verdict = "FAIL: a plugin toolset, which hermes turns on beside an explicit list"
		case img.PlatformGated[name] != nil && !slices.Contains(img.PlatformGated[name], "api_server"):
			verdict = "kept by hermes to " + strings.Join(img.PlatformGated[name], ", ")
		case slices.Contains(img.ClientSurface, name):
			verdict = "added by a desktop client only"
		case len(ts.Tools) == 0:
			verdict = "no tools in this image"
		default:
			var loose []string
			for _, tool := range ts.Tools {
				if !given[tool] && !subtracted[tool] {
					loose = append(loose, tool)
				}
			}
			if len(loose) == 0 {
				verdict = "covered: every tool is given or subtracted"
			} else {
				verdict = "FAIL: not named, and nothing subtracts " + strings.Join(loose, ", ")
			}
		}
		if strings.HasPrefix(verdict, "FAIL") {
			fails = append(fails, name)
		}
		t.Logf("%-22s %s", name, verdict)
	}

	for _, name := range disabledToolsets {
		if _, ok := img.Toolsets[name]; ok {
			continue
		}
		if _, ok := img.Aliases[name]; ok || slices.Contains(img.Legacy, name) {
			continue
		}
		t.Logf("WARN: disabledToolsets names %q, which this image does not know", name)
	}
	for _, p := range img.Plugins {
		if len(p.Unregistered) > 0 {
			// A declared tool that did not register has no toolset to
			// check; its plugin failed to load here, or loads only in a
			// running gateway.
			t.Logf("WARN: plugin %s (%s) declares %s, which did not register (%s)",
				p.Key, p.Kind, strings.Join(p.Unregistered, ", "), p.Error)
		}
	}

	// skills.go names hermes's tools for the pinned commit: the tools a
	// ghost has, and the pattern that finds a subtracted tool in a skill.
	// given holds apiServerToolsets' tools only, so a context engine's,
	// like a memory provider's, is not among them.
	var ghost []string
	for tool := range given {
		ghost = append(ghost, tool)
	}
	sort.Strings(ghost)
	want := slices.Sorted(slices.Values(ghostTools))
	if !slices.Equal(ghost, want) {
		t.Errorf("ghostTools is %v; with this image a ghost has %v", want, ghost)
	}
	var unmatched []string
	for tool := range subtracted {
		// patch and discord are left out of absentTools on purpose: each
		// is an ordinary word (skills.go).
		if tool != "patch" && tool != "discord" && absentTools.FindString(tool) != tool {
			unmatched = append(unmatched, tool)
		}
	}
	if len(unmatched) > 0 {
		sort.Strings(unmatched)
		t.Logf("WARN: absentTools does not match these subtracted tools: %s", strings.Join(unmatched, ", "))
	}

	if len(fails) > 0 {
		t.Errorf("%d toolset(s) neither named nor covered: %s; name them in disabledToolsets (profile.go), "+
			"or in apiServerToolsets if a ghost should have one", len(fails), strings.Join(fails, ", "))
	}
}
