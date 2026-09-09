package extensions

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/nbaertsch/afterburner/internal/registry"
)

type parsedVersion struct {
	core       []int
	prerelease []versionIdentifier
}

type versionIdentifier struct {
	text      string
	number    int
	isNumeric bool
}

func CompareVersions(left, right string) (int, error) {
	leftVersion, err := parseVersion(left)
	if err != nil {
		return 0, err
	}
	rightVersion, err := parseVersion(right)
	if err != nil {
		return 0, err
	}
	return compareVersionParts(leftVersion, rightVersion), nil
}

func ValidateCompatibility(manifest registry.Manifest, afterburnerVersion, copilotVersion string) error {
	if afterburnerVersion != "" && afterburnerVersion != "dev" {
		ok, err := satisfiesVersion(afterburnerVersion, manifest.Requires.Afterburner)
		if err != nil {
			return fmt.Errorf("extension %q has invalid Afterburner requirement: %w", manifest.ID, err)
		}
		if !ok {
			return fmt.Errorf("extension %q requires Afterburner %s; current version is %s",
				manifest.ID, manifest.Requires.Afterburner, afterburnerVersion)
		}
	}
	if copilotVersion != "" && len(manifest.Requires.CopilotCLI) > 0 {
		for _, requirement := range manifest.Requires.CopilotCLI {
			ok, err := satisfiesVersion(copilotVersion, requirement)
			if err != nil {
				return fmt.Errorf("extension %q has invalid Copilot CLI requirement: %w", manifest.ID, err)
			}
			if ok {
				return nil
			}
		}
		return fmt.Errorf("extension %q does not support Copilot CLI %s", manifest.ID, copilotVersion)
	}
	return nil
}

func validateCompatibilitySyntax(manifest registry.Manifest) error {
	if _, err := satisfiesVersion("0.0.0", manifest.Requires.Afterburner); err != nil {
		return err
	}
	for _, requirement := range manifest.Requires.CopilotCLI {
		if _, err := satisfiesVersion("0.0.0", requirement); err != nil {
			return err
		}
	}
	return nil
}

func satisfiesVersion(version, requirement string) (bool, error) {
	versionParts, err := parseVersion(version)
	if err != nil {
		return false, err
	}
	terms := strings.Fields(strings.TrimSpace(requirement))
	if len(terms) == 0 {
		return false, fmt.Errorf("empty version requirement")
	}
	matches := true
	for _, term := range terms {
		operator := "="
		value := term
		for _, candidate := range []string{">=", "<=", ">", "<", "="} {
			if strings.HasPrefix(term, candidate) {
				operator = candidate
				value = strings.TrimSpace(strings.TrimPrefix(term, candidate))
				break
			}
		}
		requiredParts, err := parseVersion(value)
		if err != nil {
			return false, fmt.Errorf("invalid version %q", value)
		}
		comparison := compareVersionParts(versionParts, requiredParts)
		matched := map[string]bool{
			"=":  comparison == 0,
			">":  comparison > 0,
			">=": comparison >= 0,
			"<":  comparison < 0,
			"<=": comparison <= 0,
		}[operator]
		if !matched {
			matches = false
		}
	}
	return matches, nil
}

func parseVersion(value string) (parsedVersion, error) {
	value = strings.TrimPrefix(strings.TrimSpace(value), "v")
	if value == "" {
		return parsedVersion{}, fmt.Errorf("empty version")
	}
	coreValue, prereleaseValue, hasPrerelease := strings.Cut(value, "-")
	coreFields := strings.Split(coreValue, ".")
	if len(coreFields) == 0 {
		return parsedVersion{}, fmt.Errorf("invalid version %q", value)
	}
	version := parsedVersion{core: make([]int, len(coreFields))}
	for index, field := range coreFields {
		number, err := strconv.Atoi(field)
		if err != nil || number < 0 {
			return parsedVersion{}, fmt.Errorf("invalid version %q", value)
		}
		version.core[index] = number
	}
	if !hasPrerelease {
		return version, nil
	}
	if prereleaseValue == "" {
		return parsedVersion{}, fmt.Errorf("invalid version %q", value)
	}
	for _, field := range strings.Split(prereleaseValue, ".") {
		if field == "" {
			return parsedVersion{}, fmt.Errorf("invalid version %q", value)
		}
		identifier := versionIdentifier{text: field}
		if number, err := strconv.Atoi(field); err == nil && number >= 0 {
			identifier.number = number
			identifier.isNumeric = true
		} else {
			for _, character := range field {
				if character != '-' &&
					(character < '0' || character > '9') &&
					(character < 'A' || character > 'Z') &&
					(character < 'a' || character > 'z') {
					return parsedVersion{}, fmt.Errorf("invalid version %q", value)
				}
			}
		}
		version.prerelease = append(version.prerelease, identifier)
	}
	return version, nil
}

func compareVersionParts(left, right parsedVersion) int {
	length := max(len(left.core), len(right.core))
	for index := 0; index < length; index++ {
		var leftValue, rightValue int
		if index < len(left.core) {
			leftValue = left.core[index]
		}
		if index < len(right.core) {
			rightValue = right.core[index]
		}
		if leftValue < rightValue {
			return -1
		}
		if leftValue > rightValue {
			return 1
		}
	}
	if len(left.prerelease) == 0 && len(right.prerelease) == 0 {
		return 0
	}
	if len(left.prerelease) == 0 {
		return 1
	}
	if len(right.prerelease) == 0 {
		return -1
	}
	length = max(len(left.prerelease), len(right.prerelease))
	for index := 0; index < length; index++ {
		if index >= len(left.prerelease) {
			return -1
		}
		if index >= len(right.prerelease) {
			return 1
		}
		leftPart := left.prerelease[index]
		rightPart := right.prerelease[index]
		if leftPart.isNumeric && rightPart.isNumeric {
			if leftPart.number < rightPart.number {
				return -1
			}
			if leftPart.number > rightPart.number {
				return 1
			}
			continue
		}
		if leftPart.isNumeric != rightPart.isNumeric {
			if leftPart.isNumeric {
				return -1
			}
			return 1
		}
		if leftPart.text < rightPart.text {
			return -1
		}
		if leftPart.text > rightPart.text {
			return 1
		}
	}
	return 0
}
