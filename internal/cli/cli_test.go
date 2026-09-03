package cli

import (
	"reflect"
	"testing"
)

func TestClassify(t *testing.T) {
	tests := []struct {
		name string
		in   []string
		want Route
	}{
		{"zero", nil, Route{Command: "run", Args: []string{}}},
		{"ordinary", []string{"--version"}, Route{Command: "run", Args: []string{"--version"}}},
		{"unknown command", []string{"prompt", "hello"}, Route{Command: "run", Args: []string{"prompt", "hello"}}},
		{"management", []string{"doctor", "--json"}, Route{Command: "doctor", Args: []string{"--json"}}},
		{"explicit run", []string{"run", "install"}, Route{Command: "run", Args: []string{"install"}, ForcedPassthrough: true}},
		{"terminator", []string{"--", "update", ""}, Route{Command: "run", Args: []string{"update", ""}, ForcedPassthrough: true}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Classify(tt.in); !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("Classify(%q) = %#v, want %#v", tt.in, got, tt.want)
			}
		})
	}
}

func TestExtractLaunchOptions(t *testing.T) {
	options, forwarded, err := extractLaunchOptions([]string{
		"--safe-mode", "--resume=abc", "--disable-extension", "black-box", "-p", "hello",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !options.safeMode || !reflect.DeepEqual(options.disabledExtensions, []string{"black-box"}) {
		t.Fatalf("options = %#v", options)
	}
	if !reflect.DeepEqual(forwarded, []string{"--resume=abc", "-p", "hello"}) {
		t.Fatalf("forwarded = %#v", forwarded)
	}
}
