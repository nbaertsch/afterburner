package render

import (
	"context"
	"errors"
	"io"
	"time"

	"github.com/nbaertsch/afterburner/internal/ui/bridge"
	"github.com/nbaertsch/afterburner/internal/ui/capability"
	"github.com/nbaertsch/afterburner/internal/ui/component"
	"github.com/nbaertsch/afterburner/internal/ui/protocol"
	"github.com/nbaertsch/afterburner/internal/ui/surface"
)

const (
	DefaultWidth  = 80
	DefaultHeight = 24
)

type ColorMode string

const (
	ColorModeTrueColor    ColorMode = "truecolor"
	ColorModeANSI256      ColorMode = "256"
	ColorModeANSI16       ColorMode = "16"
	ColorModeMono         ColorMode = "mono"
	ColorModeHighContrast ColorMode = "high-contrast"
)

type RendererKind string

const (
	RendererBubble RendererKind = "bubble"
	RendererPlain  RendererKind = "plain"
	RendererTCell  RendererKind = "tcell-contingency"
)

var (
	ErrRenderFailed       = errors.New("render failed")
	ErrTCellUnavailable   = errors.New("tcell contingency renderer is not linked")
	ErrUnsupportedSurface = errors.New("unsupported render surface")
)

type Sink func(context.Context, Frame) error

type Options struct {
	Width        int
	Height       int
	Theme        Theme
	ColorMode    ColorMode
	Unicode      bool
	Now          func() time.Time
	Sink         Sink
	FailureMode  FailureMode
	MaxListItems int
}

type FailureMode string

const (
	FailureReturnError FailureMode = "return-error"
	FailurePlain       FailureMode = "plain-fallback"
)

type Frame struct {
	SurfaceID string
	Revision  uint64
	Renderer  RendererKind
	Width     int
	Height    int
	ThemeID   string
	ColorMode ColorMode
	Body      string
	Plain     string
	Rendered  time.Time
}

type Engine interface {
	RenderFrame(ctx context.Context, tree component.Tree) (Frame, error)
}

type TerminalRepaintSource interface {
	Repaint() string
}

type TerminalOutputSource interface {
	ConsumeOutput([]byte)
	Repaint() string
}

func NewBubbleRenderer(opts Options) *BubbleRenderer {
	return &BubbleRenderer{opts: normalizeOptions(opts)}
}

func NewPlainRenderer(opts Options) *PlainRenderer {
	return &PlainRenderer{opts: normalizeOptions(opts)}
}

func NewFailoverRenderer(primary Engine, fallback Engine) *FailoverRenderer {
	return &FailoverRenderer{primary: primary, fallback: fallback}
}

func WriteFrame(writer io.Writer) Sink {
	return func(_ context.Context, frame Frame) error {
		if writer == nil {
			return nil
		}
		_, err := io.WriteString(writer, frame.Body)
		return err
	}
}

func SupportedComponentKinds() []component.Kind {
	return []component.Kind{
		component.KindApplication, component.KindWindow, component.KindSurface, component.KindViewport,
		kindRoot, component.KindStack, kindColumn, component.KindRow, component.KindGrid, kindBox,
		component.KindPanel, component.KindCard, kindSection, kindSplit, component.KindTabs, kindScroll,
		kindDisclosure, component.KindSeparator, component.KindSpacer,
		component.KindText, component.KindMarkdown, component.KindCode, component.KindIcon, kindKeyValue,
		kindDetail, component.KindBadge, component.KindLink, kindEmpty,
		component.KindTable, component.KindList, component.KindTree, kindTimeline, kindLog, component.KindProgress,
		kindMeter, kindBar, kindSparkline, kindStatusGrid,
		component.KindForm, component.KindTextInput, kindPasswordInput, kindSearchInput, kindNumberInput,
		component.KindTextArea, component.KindSelect, component.KindCheckbox, component.KindRadioGroup,
		component.KindToggle, component.KindSlider, kindDateInput, kindFileInput,
		component.KindButton, component.KindToolbar, kindActionBar, component.KindCommandPalette,
		kindContextMenu, component.KindBreadcrumb, kindPagination, kindHelp, component.KindKeybindingHint,
		kindAlert, component.KindToast, kindLoading, kindErrorBoundary, kindConfirmation, kindPrompt,
		component.KindTerminal, component.KindCanvas, component.KindImage, component.KindVideo, component.KindChart,
		component.KindDialog, component.KindExtensionOutlet,
	}
}

func (r *BubbleRenderer) NegotiateUI(_ context.Context, requested protocol.Revision, capabilities []capability.Descriptor) (protocol.Revision, []capability.Descriptor, error) {
	return requested, capabilities, nil
}

var _ Engine = (*BubbleRenderer)(nil)
var _ Engine = (*PlainRenderer)(nil)
var _ Engine = (*FailoverRenderer)(nil)
var _ bridge.Renderer = (*BubbleRenderer)(nil)
var _ bridge.Renderer = (*PlainRenderer)(nil)
var _ bridge.Renderer = (*TCellRenderer)(nil)

func normalizeOptions(opts Options) Options {
	if opts.Width <= 0 {
		opts.Width = DefaultWidth
	}
	if opts.Height <= 0 {
		opts.Height = DefaultHeight
	}
	if opts.ColorMode == "" {
		opts.ColorMode = ColorModeTrueColor
	}
	if opts.Theme.ID == "" {
		opts.Theme = DefaultTheme()
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.MaxListItems <= 0 {
		opts.MaxListItems = 1000
	}
	return opts
}

func descriptorSurfaceID(descriptor surface.Descriptor) string {
	if descriptor.ID != "" {
		return descriptor.ID
	}
	return "surface"
}
