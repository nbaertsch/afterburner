package cli

import (
	"reflect"
	"testing"

	"github.com/nbaertsch/afterburner/internal/registry"
)

func TestCommandNeedsCopilotCompatibility(t *testing.T) {
	for _, route := range []Route{
		{Command: "install"},
		{Command: "enable"},
		{Command: "extension", Args: []string{"install", "package.zip"}},
		{Command: "extension", Args: []string{"update", "fixture"}},
		{Command: "extension", Args: []string{"rollback", "fixture"}},
	} {
		if !commandNeedsCopilotCompatibility(route) {
			t.Fatalf("route should require compatibility discovery: %#v", route)
		}
	}
	for _, route := range []Route{
		{Command: "disable"},
		{Command: "uninstall"},
		{Command: "extension", Args: []string{"pack", "source", "package.zip"}},
		{Command: "extension", Args: []string{"validate", "source"}},
		{Command: "extension", Args: []string{"list"}},
	} {
		if commandNeedsCopilotCompatibility(route) {
			t.Fatalf("route should work without Copilot discovery: %#v", route)
		}
	}
}

func TestBuiltinRepairIDsSelectsOnlyUnverifiedBuiltins(t *testing.T) {
	value := registry.Registry{Extensions: map[string]registry.Entry{
		"black-box": {
			Verified: false,
			Manifest: registry.Manifest{Visibility: "builtin"},
		},
		"byo-models": {
			Verified: true,
			Manifest: registry.Manifest{Visibility: "builtin"},
		},
		"custom": {
			Verified: false,
			Manifest: registry.Manifest{Visibility: "private"},
		},
	}}
	got := builtinRepairIDs(value)
	if !reflect.DeepEqual(got, []string{"black-box"}) {
		t.Fatalf("repair IDs = %#v", got)
	}
}
