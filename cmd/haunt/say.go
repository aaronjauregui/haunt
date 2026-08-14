package main

import (
	"flag"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/aaronjauregui/haunt/internal/control"
	"github.com/aaronjauregui/haunt/internal/proto"
)

// cmdSay pushes a text message to a running ghost. With one ghost running,
// the name may be omitted.
func cmdSay(args []string) error {
	fs := flag.NewFlagSet("say", flag.ExitOnError)
	name := fs.String("ghost", "", "target ghost name (default: the only one running)")
	fs.Parse(args)
	if fs.NArg() == 0 {
		return fmt.Errorf("say: message text expected")
	}
	text := strings.Join(fs.Args(), " ")

	target := *name
	if target == "" {
		var err error
		if target, err = soleRunningGhost(); err != nil {
			return err
		}
	}
	return control.Send(target, proto.Event{Type: proto.EventMessage, Text: text})
}

// soleRunningGhost finds the single live control socket, erroring if there
// are none or several.
func soleRunningGhost() (string, error) {
	socks, err := filepath.Glob(filepath.Join(control.SocketDir(), "*.sock"))
	if err != nil {
		return "", err
	}
	switch len(socks) {
	case 0:
		return "", fmt.Errorf("no ghost is running")
	case 1:
		base := filepath.Base(socks[0])
		return strings.TrimSuffix(base, ".sock"), nil
	default:
		var names []string
		for _, s := range socks {
			names = append(names, strings.TrimSuffix(filepath.Base(s), ".sock"))
		}
		return "", fmt.Errorf("multiple ghosts running (%s); pass -ghost", strings.Join(names, ", "))
	}
}
