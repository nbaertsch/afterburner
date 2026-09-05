package localization

type Locale string

type Direction string

const (
	DirectionLTR  Direction = "ltr"
	DirectionRTL  Direction = "rtl"
	DirectionAuto Direction = "auto"
)

type MessageID string

type Message struct {
	ID          MessageID         `json:"id"`
	Default     string            `json:"default"`
	Description string            `json:"description,omitempty"`
	Values      map[string]string `json:"values,omitempty"`
}

type Bundle struct {
	Locale       Locale             `json:"locale"`
	Direction    Direction          `json:"direction,omitempty"`
	Fallback     Locale             `json:"fallback,omitempty"`
	Messages     map[MessageID]Text `json:"messages"`
	PluralRules  []PluralRule       `json:"pluralRules,omitempty"`
	LastModified string             `json:"lastModified,omitempty"`
}

type Text struct {
	Value       string            `json:"value"`
	Description string            `json:"description,omitempty"`
	Metadata    map[string]string `json:"metadata,omitempty"`
}

type PluralRule struct {
	Category string `json:"category"`
	Rule     string `json:"rule"`
}

type Reference struct {
	MessageID MessageID         `json:"messageId"`
	Fallback  string            `json:"fallback,omitempty"`
	Values    map[string]string `json:"values,omitempty"`
}
