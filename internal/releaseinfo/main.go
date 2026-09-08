package main

import (
	"encoding/json"
	"os"
	"sort"

	"github.com/nbaertsch/afterburner/internal/assets"
	"github.com/nbaertsch/afterburner/internal/compatibility"
	"github.com/nbaertsch/afterburner/internal/runtimepkg"
)

func main() {
	builtinIDs := make([]string, 0, len(assets.Builtins()))
	for id := range assets.Builtins() {
		builtinIDs = append(builtinIDs, id)
	}
	sort.Strings(builtinIDs)
	_ = json.NewEncoder(os.Stdout).Encode(struct {
		RuntimeDigest   string   `json:"runtimeDigest"`
		CopilotProfiles []string `json:"copilotProfiles"`
		BuiltinIDs      []string `json:"builtinIds"`
	}{
		RuntimeDigest:   runtimepkg.Digest(),
		CopilotProfiles: compatibility.ProfileIDs(),
		BuiltinIDs:      builtinIDs,
	})
}
