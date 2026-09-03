package compatibility

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/nbaertsch/afterburner/internal/copilot"
)

type Profile struct {
	ID                 string   `json:"id"`
	Version            string   `json:"version"`
	AppSHA256          string   `json:"appSha256"`
	RuntimeSHA256      string   `json:"runtimeSha256"`
	BYOModelsTransform string   `json:"byoModelsTransform"`
	RequiredAppAnchors []string `json:"requiredAppAnchors"`
}

var profiles = []Profile{
	{
		ID:                 "copilot-1.0.83-1-win32-x64",
		Version:            "1.0.83-1",
		AppSHA256:          "1e0b57f7d2ffd5dba6b921b5df2eac812399e5ddb00ed37ba80ab678a4734f06",
		RuntimeSHA256:      "1227c94b366888b8f2a3024955e731b04cb84027b4087297772c632f3215a0e3",
		BYOModelsTransform: "picker-kn",
		RequiredAppAnchors: []string{
			`W=(0,Kn.useRef)(r[0]??null),Y=(0,Kn.useRef)(null),{rows:Z,columns:de}=vi()`,
		},
	},
	{
		ID:                 "copilot-1.0.83-2-win32-x64",
		Version:            "1.0.83-2",
		AppSHA256:          "c3a972692fe469d23ef513167ede8161942da997b5145b65161788573127c600",
		RuntimeSHA256:      "07fc4e759e3edd37436ec0f6652dd698cd7c07be926cbbc76ef5f18a42bd4d5f",
		BYOModelsTransform: "picker-vn",
		RequiredAppAnchors: []string{
			`W=(0,Vn.useRef)(r[0]??null),Y=(0,Vn.useRef)(null),{rows:ee,columns:ce}=Ci()`,
		},
	},
	{
		ID:                 "copilot-1.0.83-3-win32-x64",
		Version:            "1.0.83-3",
		AppSHA256:          "96bad7f67c9fcd2146ac51f1cb86e24f971629aee07d1d8fd33a65634cabd7f9",
		RuntimeSHA256:      "e8cf4557936c3fc08eba59990b2c08cb03d800c7cfd9f40a36abe5d28ca74843",
		BYOModelsTransform: "picker-wn",
		RequiredAppAnchors: []string{
			`j=(0,Wn.useRef)(r[0]??null),W=(0,Wn.useRef)(null),{rows:X,columns:ce}=Ti()`,
		},
	},
}

type Selection struct {
	Package copilot.Package
	Profile Profile
}

func Select(packages []copilot.Package) (Selection, error) {
	var matches []Selection
	var probeFailures []string
	for _, pkg := range packages {
		for _, profile := range profiles {
			if pkg.Complete &&
				strings.EqualFold(pkg.AppSHA256, profile.AppSHA256) &&
				strings.EqualFold(pkg.RuntimeSHA256, profile.RuntimeSHA256) {
				if err := validateProbes(pkg, profile); err != nil {
					probeFailures = append(probeFailures, fmt.Sprintf("%s: %v", pkg.Version, err))
					continue
				}
				matches = append(matches, Selection{Package: pkg, Profile: profile})
			}
		}
	}
	if len(matches) == 0 {
		if os.Getenv("AFTERBURNER_ALLOW_UNPROFILED") == "1" {
			pkg, err := copilot.SelectNewestComplete(packages)
			if err != nil {
				return Selection{}, err
			}
			return Selection{
				Package: pkg,
				Profile: Profile{ID: "unprofiled-test-override", Version: pkg.Version},
			}, nil
		}
		if len(probeFailures) > 0 {
			return Selection{}, fmt.Errorf("compatible Copilot package failed structural probes: %s", strings.Join(probeFailures, "; "))
		}
		return Selection{}, fmt.Errorf("no installed Copilot package matches an embedded compatibility profile")
	}

	sort.SliceStable(matches, func(i, j int) bool {
		return compareVersion(matches[i].Package.Version, matches[j].Package.Version) > 0
	})
	return matches[0], nil
}

func validateProbes(pkg copilot.Package, profile Profile) error {
	if pkg.Path == "" {
		return nil
	}
	source, err := os.ReadFile(filepath.Join(pkg.Path, "app.js"))
	if err != nil {
		return fmt.Errorf("read app.js: %w", err)
	}
	for _, anchor := range profile.RequiredAppAnchors {
		count := bytes.Count(source, []byte(anchor))
		if count != 1 {
			return fmt.Errorf("required app.js anchor matched %d times", count)
		}
	}
	return nil
}

func compareVersion(left, right string) int {
	parse := func(value string) []int {
		fields := strings.FieldsFunc(value, func(r rune) bool { return r < '0' || r > '9' })
		result := make([]int, len(fields))
		for i, field := range fields {
			result[i], _ = strconv.Atoi(field)
		}
		return result
	}
	a, b := parse(left), parse(right)
	for i := 0; i < len(a) || i < len(b); i++ {
		var av, bv int
		if i < len(a) {
			av = a[i]
		}
		if i < len(b) {
			bv = b[i]
		}
		if av > bv {
			return 1
		}
		if av < bv {
			return -1
		}
	}
	return strings.Compare(left, right)
}

func Profiles() []Profile {
	return append([]Profile(nil), profiles...)
}

func FindProfile(id string) (Profile, bool) {
	for _, profile := range profiles {
		if profile.ID == id {
			return profile, true
		}
	}
	return Profile{}, false
}
