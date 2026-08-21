package main

import (
	"fmt"
	"sort"
	"strings"

	"github.com/aaronjauregui/haunt/internal/ghost"
	"github.com/aaronjauregui/haunt/internal/library"
)

// cmdPerms lists and changes what an installed ghost is allowed to do.
//
// The design promised this from the start and the install prompt has been
// pointing at it for as long as it has existed ("installing with no
// capabilities granted (haunt perms to change later)"). Until now there was
// nowhere to change it: consent was a single yes/no at install and a
// granted capability was granted for good, which is the wrong shape for a
// thing that lives on your desktop for months.
//
// Revocation takes effect on a running ghost, not at next start — see
// library.GrantWatch. A permission you can only withdraw by restarting the
// thing is not really withdrawn.
func cmdPerms(args []string) error {
	if len(args) == 0 {
		return permsList("")
	}
	switch args[0] {
	case "grant":
		return permsSet(args[1:], true)
	case "revoke":
		return permsSet(args[1:], false)
	default:
		if strings.HasPrefix(args[0], "-") {
			return fmt.Errorf("perms: unknown flag %q", args[0])
		}
		if len(args) > 1 {
			return fmt.Errorf("perms: expected a ghost name, or grant/revoke <capability>")
		}
		return permsList(args[0])
	}
}

// permsWho resolves the ghost being talked about: the named one, or the
// resident one when there is only one to mean. With a room full of ghosts
// it asks rather than picks — granting a capability to the wrong character
// is not a mistake to make silently.
func permsWho(name string) (string, error) {
	if name != "" {
		return name, nil
	}
	return soleResident("name one")
}

// permsList prints what the ghost asked for and what it has.
func permsList(name string) error {
	name, err := permsWho(name)
	if err != nil {
		return err
	}
	declared, err := declaredCaps(name)
	if err != nil {
		return err
	}
	grants, err := library.LoadGrants(name)
	if err != nil {
		return err
	}

	fmt.Printf("%s\n", name)
	if len(declared) == 0 {
		fmt.Println("  asks for nothing; there is nothing to grant")
	}
	for _, c := range declared {
		mark := "no "
		if grants.Granted(c) {
			mark = "yes"
		}
		note := ""
		if grants.IsRefused(c) {
			// Worth saying: this is the difference between "not yet" and
			// "the ghost has been told to stop asking".
			note = "  (refused — it will not ask again)"
		}
		fmt.Printf("  [%s] %-13s %s%s\n", mark, c, ghost.KnownCapabilities[c], note)
	}

	servers := permsListTools(name, grants)

	// Grants the manifest no longer asks for: an update can drop a
	// capability, and a stale grant left in the file is worth saying out
	// loud even though the driver ignores it (a capability must be both
	// declared and granted to be active).
	var stale []string
	for _, c := range grants.Capabilities {
		// A tool grant is listed above, against the server it belongs to —
		// unless that server is gone, in which case it belongs here for
		// exactly the reason the rest of this list exists: an update can
		// drop a tool server, and a yes left behind for one is a yes with
		// nowhere to appear.
		if server, isTool := ghost.ToolGrantServer(c); isTool {
			if contains(servers, server) {
				continue
			}
			stale = append(stale, c)
			continue
		}
		if !contains(declared, c) {
			stale = append(stale, c)
		}
	}
	if len(stale) > 0 {
		sort.Strings(stale)
		fmt.Printf("\n  granted but no longer requested: %s\n", strings.Join(stale, ", "))
		fmt.Println("  (inactive — the driver requires both — revoke to tidy up)")
	}
	return nil
}

// permsSet grants or revokes one capability.
func permsSet(args []string, grant bool) error {
	verb := "revoke"
	if grant {
		verb = "grant"
	}
	if len(args) < 1 || len(args) > 2 {
		return fmt.Errorf("perms %s: expected a capability, and optionally a ghost name", verb)
	}
	cap := args[0]
	name := ""
	if len(args) == 2 {
		name = args[1]
	}
	name, err := permsWho(name)
	if err != nil {
		return err
	}
	// A tool grant ("tool:notes.append") is settled against the servers the
	// manifest declares rather than against the capability table: the tools
	// themselves are the server's business and are not known until it runs,
	// so what can be checked here is the half that is in the manifest.
	if server, ok := ghost.ToolGrantServer(cap); ok {
		if err := checkToolGrant(name, cap, server, grant); err != nil {
			return err
		}
	} else {
		if _, known := ghost.KnownCapabilities[cap]; !known {
			return fmt.Errorf("perms %s: %q is not a capability (%s), nor a tool (tool:<server>.<tool>)",
				verb, cap, capabilityList())
		}
		declared, err := declaredCaps(name)
		if err != nil {
			return err
		}
		// Granting something the ghost never asked for would be theatre: the
		// driver requires both a declaration and a grant, so it would stay
		// inactive and the listing would say so forever.
		if grant && !contains(declared, cap) {
			return fmt.Errorf("perms grant: %s does not ask for %q, so granting it would do nothing", name, cap)
		}
	}

	grants, err := library.LoadGrants(name)
	if err != nil {
		return err
	}
	had := grants.Granted(cap)
	if grant && had && !grants.IsRefused(cap) {
		fmt.Printf("%s already has %s\n", name, cap)
		return nil
	}
	if !grant && !had {
		fmt.Printf("%s does not have %s\n", name, cap)
		return nil
	}
	if grant {
		// Granting also clears a standing refusal: saying yes here is also
		// saying the ghost may ask about this again.
		grants.Allow(cap)
	} else {
		// Revoking is not the same as "never ask me again" — that answer
		// belongs to the person being asked, in the balloon.
		grants.Withdraw(cap)
	}
	if err := library.SaveGrants(name, grants); err != nil {
		return err
	}

	if grant {
		fmt.Printf("granted %s to %s\n", cap, name)
	} else {
		fmt.Printf("revoked %s from %s\n", cap, name)
	}
	// Said plainly, because "will apply on restart" is what people expect
	// of a permission change and is not what happens here.
	fmt.Println("in effect now, including for a ghost that is already running")
	return nil
}

// checkToolGrant validates a "tool:<server>.<tool>" grant against the
// manifest: the server must be one the ghost declares, and the ghost must
// ask for the tools capability at all. Which tools that server offers is
// not checked, because only the running server knows — and a name typed
// slightly wrong is a grant that does nothing, which the listing says.
func checkToolGrant(name, key, server string, grant bool) error {
	if !grant {
		// Revoking is always allowed, including a grant left behind by a
		// server the ghost no longer declares. Tidying up must not require
		// the manifest to still agree.
		return nil
	}
	m, err := loadInstalled(name)
	if err != nil {
		return err
	}
	if !contains(m.Ghost.Capabilities, "tools") {
		return fmt.Errorf("perms grant: %s does not ask for the tools capability, so %q would do nothing", name, key)
	}
	for _, t := range m.Tools {
		if t.Name == server {
			return nil
		}
	}
	return fmt.Errorf("perms grant: %s declares no tool server called %q", name, server)
}

// loadInstalled reads an installed ghost's manifest.
func loadInstalled(name string) (*ghost.Manifest, error) {
	dir, err := library.PkgDir(name)
	if err != nil {
		return nil, err
	}
	return ghost.LoadManifest(dir)
}

// declaredCaps reads what the installed ghost's manifest asks for.
func declaredCaps(name string) ([]string, error) {
	dir, err := library.PkgDir(name)
	if err != nil {
		return nil, err
	}
	m, err := ghost.LoadManifest(dir)
	if err != nil {
		return nil, err
	}
	caps := append([]string(nil), m.Ghost.Capabilities...)
	sort.Strings(caps)
	return caps, nil
}

func capabilityList() string {
	names := make([]string, 0, len(ghost.KnownCapabilities))
	for c := range ghost.KnownCapabilities {
		names = append(names, c)
	}
	sort.Strings(names)
	return strings.Join(names, ", ")
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// permsListTools prints the tool servers the ghost declares and which of
// their tools have a standing yes.
//
// The listing is necessarily one-sided: the manifest names servers, the
// servers name tools, and only a running server can say what it offers. So
// what is shown is the servers as declared, and under each the tools that
// have actually been decided about — which is the list a person wants
// anyway, since it is the list of things they said yes to and might want to
// take back.
// Returns the server names it printed, so the caller can tell a grant that
// belongs to one of them from a grant left behind by one that is gone.
func permsListTools(name string, grants *library.Grants) []string {
	m, err := loadInstalled(name)
	if err != nil || len(m.Tools) == 0 {
		// A manifest that would not load has already been reported by the
		// capability listing above; nothing here to add.
		return nil
	}
	servers := make([]string, 0, len(m.Tools))
	fmt.Println("\n  tool servers it declares:")
	for _, t := range m.Tools {
		bound := "any tool it offers"
		if len(t.Allow) > 0 {
			bound = strings.Join(t.Allow, ", ")
		}
		servers = append(servers, t.Name)
		fmt.Printf("    %-13s %s\n", t.Name, bound)
		for _, c := range decidedTools(grants, t.Name) {
			mark := "no "
			if grants.Granted(c) {
				mark = "yes"
			}
			note := ""
			if grants.IsRefused(c) {
				note = "  (refused — it will not ask again)"
			}
			fmt.Printf("      [%s] %s%s\n", mark, strings.TrimPrefix(c, "tool:"), note)
		}
	}
	fmt.Println("  (each tool is asked about the first time it is used;")
	fmt.Println("   change one with: haunt perms grant tool:" + m.Tools[0].Name + ".<tool>)")
	return servers
}

// decidedTools returns the tool grant keys for one server that the user has
// answered either way, sorted.
func decidedTools(grants *library.Grants, server string) []string {
	seen := map[string]bool{}
	var out []string
	for _, list := range [][]string{grants.Capabilities, grants.Refused} {
		for _, c := range list {
			if s, ok := ghost.ToolGrantServer(c); !ok || s != server || seen[c] {
				continue
			}
			seen[c] = true
			out = append(out, c)
		}
	}
	sort.Strings(out)
	return out
}
