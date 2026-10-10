package ghost

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"

	"github.com/aaronjauregui/haunt/internal/proto"
)

// The face and the eyes: wearing a pose with no words, playing an
// animation, and looking at the person's window.

func init() {
	register(verb{
		id:     "face",
		desc:   "Wear one of your poses (listed in your instructions) without saying anything.",
		params: []string{"pose:string"},
		run:    tool(toolFace),
	})
	register(verb{
		id:     "clip",
		desc:   "Play one of your shell's animations.",
		params: []string{"name:string"},
		run:    tool(toolClip),
	})
	register(verb{
		id:     "look",
		desc:   "Look at the person's focused window and get a one-sentence description. The person sees you looking.",
		params: []string{"question?:string"},
		run:    tool(toolLook),
	})
}

func toolFace(g *Ghost, _ context.Context, args json.RawMessage) (string, error) {
	var a struct {
		Pose string `json:"pose"`
	}
	if err := decode(args, &a); err != nil {
		return "", err
	}
	if g.shell == nil || !slices.Contains(g.shell.Poses(), a.Pose) {
		return "", fmt.Errorf("%q is not one of your poses", a.Pose)
	}
	if !g.mayPose() {
		return "", refuse("not now: your face stays still over fullscreen windows and under reduced motion")
	}
	g.setExpression(a.Pose)
	return "wearing " + a.Pose, nil
}

func toolClip(g *Ghost, _ context.Context, args json.RawMessage) (string, error) {
	var a struct {
		Name string `json:"name"`
	}
	if err := decode(args, &a); err != nil {
		return "", err
	}
	if !g.mayPose() {
		return "", refuse("not now: no animation over fullscreen windows or under reduced motion")
	}
	if !g.playClip(a.Name) {
		return "", fmt.Errorf("%q is not one of your animations", a.Name)
	}
	g.announce(proto.ActedClip, a.Name)
	return "playing " + a.Name, nil
}

func toolLook(g *Ghost, _ context.Context, args json.RawMessage) (string, error) {
	var a struct {
		Question string `json:"question"`
	}
	if err := decode(args, &a); err != nil {
		return "", err
	}
	if !g.ensureCapability("screen") {
		return "", refuse("the screen capability is not granted")
	}
	answer, app, err := g.look(a.Question)
	if err != nil {
		return "", err
	}
	if app != "" {
		return app + ": " + answer, nil
	}
	return answer, nil
}
