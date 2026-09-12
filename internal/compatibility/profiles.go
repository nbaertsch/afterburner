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
	ID                     string   `json:"id"`
	Version                string   `json:"version"`
	AppSHA256              string   `json:"appSha256"`
	RuntimeSHA256          string   `json:"runtimeSha256"`
	BYOModelsTransform     string   `json:"byoModelsTransform"`
	RequiredAppAnchors     []string `json:"requiredAppAnchors"`
	ModelPickerRowRenderer string   `json:"modelPickerRowRenderer"`
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
			`j=(0,Kn.useMemo)(()=>{let Ue=new Map;for(let nt of r){let ut=ne.get(nt.value);ut&&Ue.set(ut.rowKey,nt)}return Ue},[r,ne])`,
		},
		ModelPickerRowRenderer: "vzr",
	},
	{
		ID:                 "copilot-1.0.83-2-win32-x64",
		Version:            "1.0.83-2",
		AppSHA256:          "c3a972692fe469d23ef513167ede8161942da997b5145b65161788573127c600",
		RuntimeSHA256:      "07fc4e759e3edd37436ec0f6652dd698cd7c07be926cbbc76ef5f18a42bd4d5f",
		BYOModelsTransform: "picker-vn",
		RequiredAppAnchors: []string{
			`W=(0,Vn.useRef)(r[0]??null),Y=(0,Vn.useRef)(null),{rows:ee,columns:ce}=Ci()`,
			`j=(0,Vn.useMemo)(()=>{let Ue=new Map;for(let nt of r){let ut=ne.get(nt.value);ut&&Ue.set(ut.rowKey,nt)}return Ue},[r,ne])`,
		},
		ModelPickerRowRenderer: "Wzr",
	},
	{
		ID:                 "copilot-1.0.83-3-win32-x64",
		Version:            "1.0.83-3",
		AppSHA256:          "96bad7f67c9fcd2146ac51f1cb86e24f971629aee07d1d8fd33a65634cabd7f9",
		RuntimeSHA256:      "e8cf4557936c3fc08eba59990b2c08cb03d800c7cfd9f40a36abe5d28ca74843",
		BYOModelsTransform: "picker-wn",
		RequiredAppAnchors: []string{
			`j=(0,Wn.useRef)(r[0]??null),W=(0,Wn.useRef)(null),{rows:X,columns:ce}=Ti()`,
			`V=(0,Wn.useMemo)(()=>{let Le=new Map;for(let tt of r){let Ct=re.get(tt.value);Ct&&Le.set(Ct.rowKey,tt)}return Le},[r,re])`,
		},
		ModelPickerRowRenderer: "U6r",
	},
	{
		ID:                 "copilot-1.0.84-4-win32-x64",
		Version:            "1.0.84-4",
		AppSHA256:          "916fa57db9b90245745f1d3b7d8b66f4cccf2bbfcb7b1e21fd2b1a056d99d147",
		RuntimeSHA256:      "3eb5587f384c709b2833a77b1e72713c4d5ac8c4393047505763b8d4cdfb12c4",
		BYOModelsTransform: "picker-wn-1084",
		RequiredAppAnchors: []string{
			`W=(0,Wn.useRef)(r[0]??null),V=(0,Wn.useRef)(null),{rows:K,columns:oe}=wi()`,
			`J=(0,Wn.useMemo)(()=>{let Je=new Map;for(let mt of r){let dt=se.get(mt.value);dt&&Je.set(dt.rowKey,mt)}return Je},[r,se])`,
		},
		ModelPickerRowRenderer: "oWr",
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
				// A warm cache hit has the exact profile hashes; preflight still
				// verifies the cached file metadata before Copilot is launched.
				if !pkg.HashCacheHit {
					if err := validateProbes(pkg, profile); err != nil {
						probeFailures = append(probeFailures, fmt.Sprintf("%s: %v", pkg.Version, err))
						continue
					}
				}
				matches = append(matches, Selection{Package: pkg, Profile: profile})
			}
		}
	}
	if len(matches) == 0 {
		if pkg, ok := newestForwardCompatible(packages); ok {
			return Selection{Package: pkg, Profile: ForwardProfile(pkg)}, nil
		}
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

func newestForwardCompatible(packages []copilot.Package) (copilot.Package, bool) {
	newestKnown := profiles[0].Version
	for _, profile := range profiles[1:] {
		if compareVersion(profile.Version, newestKnown) > 0 {
			newestKnown = profile.Version
		}
	}
	var candidates []copilot.Package
	for _, pkg := range packages {
		if pkg.Complete && compareVersion(pkg.Version, newestKnown) > 0 {
			candidates = append(candidates, pkg)
		}
	}
	if len(candidates) == 0 {
		return copilot.Package{}, false
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		return compareVersion(candidates[i].Version, candidates[j].Version) > 0
	})
	return candidates[0], true
}

func ForwardProfile(pkg copilot.Package) Profile {
	versionID := strings.Map(func(value rune) rune {
		if value >= 'a' && value <= 'z' || value >= 'A' && value <= 'Z' ||
			value >= '0' && value <= '9' || value == '.' || value == '-' {
			return value
		}
		return '-'
	}, pkg.Version)
	return Profile{
		ID:                 "copilot-forward-" + versionID + "-win32",
		Version:            pkg.Version,
		AppSHA256:          pkg.AppSHA256,
		RuntimeSHA256:      pkg.RuntimeSHA256,
		BYOModelsTransform: "picker-forward",
	}
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

func ProfileIDs() []string {
	ids := make([]string, 0, len(profiles))
	for _, profile := range profiles {
		ids = append(ids, profile.ID)
	}
	return ids
}

func FindProfile(id string) (Profile, bool) {
	for _, profile := range profiles {
		if profile.ID == id {
			return profile, true
		}
	}
	return Profile{}, false
}
