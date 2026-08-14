package main

import (
	"bufio"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/aaronjauregui/haunt/internal/ghost"
	"github.com/aaronjauregui/haunt/internal/library"
	"github.com/aaronjauregui/haunt/internal/pack"
)

// cmdKeygen creates the author signing keypair.
func cmdKeygen(args []string) error {
	if len(args) != 0 {
		return fmt.Errorf("keygen takes no arguments")
	}
	pub, err := pack.GenerateKey()
	if err != nil {
		return err
	}
	fmt.Printf("generated author key %s\n", pack.Fingerprint(pub))
	return nil
}

// cmdPack builds a signed .haunt package from a ghost directory.
func cmdPack(args []string) error {
	fs := flag.NewFlagSet("pack", flag.ExitOnError)
	out := fs.String("o", "", "output file (default <name>.haunt)")
	fs.Parse(args)
	if fs.NArg() != 1 {
		return fmt.Errorf("pack: exactly one ghost directory expected")
	}
	dir := fs.Arg(0)

	m, err := ghost.LoadManifest(dir)
	if err != nil {
		return err
	}
	if _, err := os.Stat(filepath.Join(dir, m.Brain.Wasm)); err != nil {
		return fmt.Errorf("brain module %s missing; build it first", m.Brain.Wasm)
	}
	key, err := pack.LoadKey()
	if err != nil {
		return err
	}
	path := *out
	if path == "" {
		path = m.Ghost.Name + ".haunt"
	}
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	if err := pack.Write(f, dir, key); err != nil {
		f.Close()
		os.Remove(path)
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	fmt.Printf("packed %s %s -> %s\n", m.Ghost.Name, m.Ghost.Version, path)
	return nil
}

// cmdInstall verifies and installs a .haunt package, with a consent prompt
// for requested capabilities.
func cmdInstall(args []string) error {
	fs := flag.NewFlagSet("install", flag.ExitOnError)
	yes := fs.Bool("yes", false, "grant requested capabilities without prompting")
	fs.Parse(args)
	if fs.NArg() != 1 {
		return fmt.Errorf("install: exactly one .haunt file expected")
	}

	f, err := os.Open(fs.Arg(0))
	if err != nil {
		return err
	}
	defer f.Close()
	p, err := pack.Read(f)
	if err != nil {
		return fmt.Errorf("package rejected: %w", err)
	}

	// Peek the manifest for the consent sheet.
	peek, err := os.MkdirTemp("", "haunt-peek-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(peek)
	if err := p.Extract(peek); err != nil {
		return err
	}
	m, err := ghost.LoadManifest(peek)
	if err != nil {
		return fmt.Errorf("package manifest: %w", err)
	}

	fmt.Printf("ghost:   %s %s\n", m.Ghost.Name, m.Ghost.Version)
	fmt.Printf("author:  %s\n", pack.Fingerprint(p.AuthorKey))
	if len(m.Ghost.Capabilities) > 0 {
		fmt.Println("requested capabilities:")
		for _, c := range m.Ghost.Capabilities {
			fmt.Printf("  - %s: %s\n", c, ghost.KnownCapabilities[c])
		}
	} else {
		fmt.Println("requested capabilities: none")
	}

	grantAll := *yes
	if !grantAll && len(m.Ghost.Capabilities) > 0 {
		fmt.Print("grant these capabilities? [y/N] ")
		line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
		grantAll = strings.TrimSpace(strings.ToLower(line)) == "y"
		if !grantAll {
			fmt.Println("installing with no capabilities granted (haunt perms to change later)")
		}
	}

	res, err := library.Install(library.InstallRequest{Package: p, GrantAll: grantAll})
	if err != nil {
		return err
	}
	if res.FirstInstall {
		fmt.Printf("installed %s %s (author key pinned)\n", res.Name, res.Version)
	} else {
		fmt.Printf("updated %s to %s\n", res.Name, res.Version)
	}
	if cur, _ := library.Current(); cur == "" {
		if err := library.SetCurrent(res.Name); err == nil {
			fmt.Printf("%s is now the current ghost\n", res.Name)
		}
	}
	return nil
}

// cmdLs lists installed ghosts.
func cmdLs(args []string) error {
	if len(args) != 0 {
		return fmt.Errorf("ls takes no arguments")
	}
	names, err := library.List()
	if err != nil {
		return err
	}
	cur, _ := library.Current()
	for _, n := range names {
		marker := " "
		if n == cur {
			marker = "*"
		}
		pkgDir, err := library.PkgDir(n)
		version := "?"
		if err == nil {
			if m, err := ghost.LoadManifest(pkgDir); err == nil {
				version = m.Ghost.Version
			}
		}
		fmt.Printf("%s %s %s\n", marker, n, version)
	}
	return nil
}

// cmdRm removes an installed ghost and its state.
func cmdRm(args []string) error {
	if len(args) != 1 {
		return fmt.Errorf("rm: exactly one ghost name expected")
	}
	return library.Remove(args[0])
}

// cmdSwitch selects the current ghost.
func cmdSwitch(args []string) error {
	if len(args) != 1 {
		return fmt.Errorf("switch: exactly one ghost name expected")
	}
	if err := library.SetCurrent(args[0]); err != nil {
		return err
	}
	fmt.Printf("current ghost is now %s\n", args[0])
	return nil
}
