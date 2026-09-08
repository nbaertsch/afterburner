package assets

import _ "embed"

//go:generate go run ./generate

//go:embed generated/byo-models.zip
var byoModels []byte

//go:embed generated/black-box.zip
var blackBox []byte

//go:embed generated/openai-server.zip
var openAIServer []byte

func Builtins() map[string][]byte {
	return map[string][]byte{
		"byo-models":    append([]byte(nil), byoModels...),
		"black-box":     append([]byte(nil), blackBox...),
		"openai-server": append([]byte(nil), openAIServer...),
	}
}
