package render

import (
	"context"
	"encoding/json"

	"github.com/nbaertsch/afterburner/internal/ui/component"
)

func TerminalTree(surfaceID string, revision uint64, source TerminalRepaintSource) component.Tree {
	text := ""
	if source != nil {
		text = source.Repaint()
	}
	props, _ := json.Marshal(map[string]any{"repaint": text})
	return component.Tree{
		SurfaceID: surfaceID,
		Revision:  revision,
		Root: component.Node{
			ID:    "terminal",
			Kind:  component.KindTerminal,
			Props: props,
		},
	}
}

func RenderTerminal(ctx context.Context, renderer Engine, surfaceID string, revision uint64, source TerminalRepaintSource) (Frame, error) {
	if renderer == nil {
		renderer = NewPlainRenderer(Options{})
	}
	return renderer.RenderFrame(ctx, TerminalTree(surfaceID, revision, source))
}
