package style

type TokenID string

type TokenCategory string

const (
	CategoryColor      TokenCategory = "color"
	CategorySpace      TokenCategory = "space"
	CategoryTypography TokenCategory = "typography"
	CategoryBorder     TokenCategory = "border"
	CategoryMotion     TokenCategory = "motion"
	CategoryElevation  TokenCategory = "elevation"
	CategoryOpacity    TokenCategory = "opacity"
)

const (
	ColorBackground       TokenID = "color.background"
	ColorBackgroundRaised TokenID = "color.background.raised"
	ColorForeground       TokenID = "color.foreground"
	ColorForegroundMuted  TokenID = "color.foreground.muted"
	ColorAccent           TokenID = "color.accent"
	ColorAccentForeground TokenID = "color.accent.foreground"
	ColorSuccess          TokenID = "color.success"
	ColorWarning          TokenID = "color.warning"
	ColorDanger           TokenID = "color.danger"
	ColorInfo             TokenID = "color.info"
	ColorBorder           TokenID = "color.border"
	ColorFocusRing        TokenID = "color.focusRing"
	ColorSelection        TokenID = "color.selection"
	ColorTerminalBlack    TokenID = "color.terminal.black"
	ColorTerminalRed      TokenID = "color.terminal.red"
	ColorTerminalGreen    TokenID = "color.terminal.green"
	ColorTerminalYellow   TokenID = "color.terminal.yellow"
	ColorTerminalBlue     TokenID = "color.terminal.blue"
	ColorTerminalMagenta  TokenID = "color.terminal.magenta"
	ColorTerminalCyan     TokenID = "color.terminal.cyan"
	ColorTerminalWhite    TokenID = "color.terminal.white"
	SpaceNone             TokenID = "space.none"
	SpaceXXS              TokenID = "space.2xs"
	SpaceXS               TokenID = "space.xs"
	SpaceS                TokenID = "space.s"
	SpaceM                TokenID = "space.m"
	SpaceL                TokenID = "space.l"
	SpaceXL               TokenID = "space.xl"
	SpaceXXL              TokenID = "space.2xl"
	FontFamilyUI          TokenID = "font.family.ui"
	FontFamilyMono        TokenID = "font.family.mono"
	FontSizeXS            TokenID = "font.size.xs"
	FontSizeS             TokenID = "font.size.s"
	FontSizeM             TokenID = "font.size.m"
	FontSizeL             TokenID = "font.size.l"
	FontSizeXL            TokenID = "font.size.xl"
	FontWeightRegular     TokenID = "font.weight.regular"
	FontWeightMedium      TokenID = "font.weight.medium"
	FontWeightBold        TokenID = "font.weight.bold"
	LineHeightTight       TokenID = "lineHeight.tight"
	LineHeightNormal      TokenID = "lineHeight.normal"
	RadiusNone            TokenID = "radius.none"
	RadiusS               TokenID = "radius.s"
	RadiusM               TokenID = "radius.m"
	RadiusL               TokenID = "radius.l"
	BorderWidthThin       TokenID = "border.width.thin"
	BorderWidthThick      TokenID = "border.width.thick"
	DurationInstant       TokenID = "duration.instant"
	DurationFast          TokenID = "duration.fast"
	DurationNormal        TokenID = "duration.normal"
	DurationSlow          TokenID = "duration.slow"
	ElevationNone         TokenID = "elevation.none"
	ElevationRaised       TokenID = "elevation.raised"
	OpacityDisabled       TokenID = "opacity.disabled"
)

type ThemeMode string

const (
	ThemeModeLight        ThemeMode = "light"
	ThemeModeDark         ThemeMode = "dark"
	ThemeModeHighContrast ThemeMode = "highContrast"
	ThemeModeTerminal     ThemeMode = "terminal"
)

type TokenValue struct {
	ID          TokenID       `json:"id"`
	Category    TokenCategory `json:"category"`
	Value       string        `json:"value"`
	Description string        `json:"description,omitempty"`
}

type TokenRef struct {
	ID       TokenID `json:"id"`
	Fallback string  `json:"fallback,omitempty"`
}

type Theme struct {
	ID          string       `json:"id"`
	DisplayName string       `json:"displayName"`
	Mode        ThemeMode    `json:"mode"`
	Tokens      []TokenValue `json:"tokens"`
	Extends     string       `json:"extends,omitempty"`
}

type ComponentStyle struct {
	Classes    []string            `json:"classes,omitempty"`
	Tokens     map[TokenID]string  `json:"tokens,omitempty"`
	Attributes map[string]string   `json:"attributes,omitempty"`
	State      map[string]TokenRef `json:"state,omitempty"`
}

func SemanticTokenIDs() []TokenID {
	return []TokenID{
		ColorBackground, ColorBackgroundRaised, ColorForeground, ColorForegroundMuted,
		ColorAccent, ColorAccentForeground, ColorSuccess, ColorWarning, ColorDanger,
		ColorInfo, ColorBorder, ColorFocusRing, ColorSelection, ColorTerminalBlack,
		ColorTerminalRed, ColorTerminalGreen, ColorTerminalYellow, ColorTerminalBlue,
		ColorTerminalMagenta, ColorTerminalCyan, ColorTerminalWhite, SpaceNone,
		SpaceXXS, SpaceXS, SpaceS, SpaceM, SpaceL, SpaceXL, SpaceXXL, FontFamilyUI,
		FontFamilyMono, FontSizeXS, FontSizeS, FontSizeM, FontSizeL, FontSizeXL,
		FontWeightRegular, FontWeightMedium, FontWeightBold, LineHeightTight,
		LineHeightNormal, RadiusNone, RadiusS, RadiusM, RadiusL, BorderWidthThin,
		BorderWidthThick, DurationInstant, DurationFast, DurationNormal, DurationSlow,
		ElevationNone, ElevationRaised, OpacityDisabled,
	}
}
