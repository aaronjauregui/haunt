package main

import (
	"flag"
	"fmt"
	"strings"

	"github.com/aaronjauregui/haunt/internal/control"
	"github.com/aaronjauregui/haunt/internal/proto"
)

// cmdSay pushes a text message to a running ghost. Without a name it goes
// to the ghost you last interacted with, which with one ghost running is
// simply that one.
func cmdSay(args []string) error {
	fs := flag.NewFlagSet("say", flag.ExitOnError)
	name := fs.String("ghost", "", "target ghost name (default: the one you last interacted with)")
	fs.Parse(args)
	if fs.NArg() == 0 {
		return fmt.Errorf("say: message text expected")
	}
	text := strings.Join(fs.Args(), " ")

	target := *name
	if target == "" {
		var err error
		if target, err = sayTarget(); err != nil {
			return err
		}
	}
	return control.Send(target, proto.Event{Type: proto.EventMessage, Text: text})
}

// sayTarget picks the ghost an untargeted message is for: the one
// the user last poked, typed at or answered. It is only ambiguous when
// several are running and none has been spoken to yet — a fresh login,
// before anybody has touched anything — and then it says so rather than
// guessing.
func sayTarget() (string, error) {
	if name := control.Recent(); name != "" {
		return name, nil
	}
	live := control.Running()
	if len(live) == 0 {
		return "", fmt.Errorf("no ghost is running")
	}
	return "", fmt.Errorf("several ghosts are running (%s) and none has been spoken to yet; pass -ghost",
		strings.Join(live, ", "))
}
