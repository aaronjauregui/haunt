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

	// Grants the manifest no longer asks for: an update can drop a
	// capability, and a stale grant left in the file is worth saying out
	// loud even though the driver ignores it (a capability must be both
	// declared and granted to be active).
	var stale []string
	for _, c := range grants.Capabilities {
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
	if _, known := ghost.KnownCapabilities[cap]; !known {
		return fmt.Errorf("perms %s: %q is not a capability (%s)", verb, cap, capabilityList())
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
