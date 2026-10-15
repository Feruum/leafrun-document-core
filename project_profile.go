package documentcore

import (
	"fmt"
	"regexp"
	"slices"
	"strings"
	"unicode/utf8"
)

const (
	projectProfileInputMaxBytes      = 4096
	projectProfileInputCountMax      = 64
	projectProfileInputTotalMaxBytes = 64 << 10
	projectProfileNameMaxBytes       = 64
	projectProfileKeyMaxBytes        = 64
)

var projectNamePattern = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_.-]{0,63}$`)
var projectInputKeyPattern = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_-]{0,63}$`)

var supportedPDFStandards = map[string]struct{}{
	"1.4": {}, "1.5": {}, "1.6": {}, "1.7": {}, "2.0": {},
	"a-1b": {}, "a-1a": {}, "a-2b": {}, "a-2u": {}, "a-2a": {},
	"a-3b": {}, "a-3u": {}, "a-3a": {}, "a-4": {}, "a-4f": {}, "a-4e": {},
	"ua-1": {},
}

func validateProjectProfiles(profiles map[string]ProjectProfile, defaultProfile string) error {
	if len(profiles) == 0 {
		return projectError(ProjectManifestFile, "/profiles", "profiles_required", "at least one profile is required")
	}
	for _, name := range sortedProjectProfileNames(profiles) {
		profile := profiles[name]
		base := "/profiles/" + escapeJSONPointer(name)
		if !projectNamePattern.MatchString(name) || len(name) > projectProfileNameMaxBytes {
			return projectError(ProjectManifestFile, base, "invalid_profile_id", "profile name must be a bounded identifier")
		}
		if len(profile.Inputs) > projectProfileInputCountMax {
			return projectError(ProjectManifestFile, base+"/inputs", "too_many_profile_inputs", "profile has more than 64 template inputs")
		}
		inputBytes := 0
		for _, key := range sortedProjectStringKeys(profile.Inputs) {
			value := profile.Inputs[key]
			pointer := base + "/inputs/" + escapeJSONPointer(key)
			if !projectInputKeyPattern.MatchString(key) || len(key) > projectProfileKeyMaxBytes {
				return projectError(ProjectManifestFile, pointer, "invalid_profile_input", "input key must be a bounded identifier")
			}
			if key == "leafrun-data" {
				return projectError(ProjectManifestFile, pointer, "reserved_profile_input", "leafrun-data is reserved to Core")
			}
			if !utf8.ValidString(value) || strings.ContainsRune(value, '\x00') {
				return projectError(ProjectManifestFile, pointer, "invalid_profile_input", "input must be valid UTF-8 without NUL")
			}
			if len(value) > projectProfileInputMaxBytes {
				return projectError(ProjectManifestFile, pointer, "profile_input_too_large", fmt.Sprintf("input exceeds %d bytes", projectProfileInputMaxBytes))
			}
			inputBytes += len(key) + len(value)
			if inputBytes > projectProfileInputTotalMaxBytes {
				return projectError(ProjectManifestFile, base+"/inputs", "profile_inputs_too_large", "combined profile inputs exceed 64 KiB")
			}
		}
		if profile.PDFStandard != "" && !validPDFStandardList(profile.PDFStandard) {
			return projectError(ProjectManifestFile, base+"/pdfStandard", "unsupported_pdf_standard", "PDF standard is not supported by Typst 0.15.1")
		}
	}
	if _, ok := profiles[defaultProfile]; !ok {
		return projectError(ProjectManifestFile, "/defaultProfile", "profile_not_found", "default profile does not exist")
	}
	return nil
}

func validPDFStandardList(value string) bool {
	if len(value) > 256 {
		return false
	}
	parts := strings.Split(value, ",")
	if len(parts) == 0 {
		return false
	}
	seen := make(map[string]struct{}, len(parts))
	for _, part := range parts {
		if part == "" || strings.TrimSpace(part) != part {
			return false
		}
		if _, ok := supportedPDFStandards[part]; !ok {
			return false
		}
		if _, duplicate := seen[part]; duplicate {
			return false
		}
		seen[part] = struct{}{}
	}
	return true
}

// validateTypstInputs applies the same bounded key/value contract to frozen
// profile inputs and direct snapshot rendering options. Core owns the
// leafrun-data entry; profiles may contribute at most 64 other inputs.
func validateTypstInputs(inputs map[string]string, requireLeafrunData bool) error {
	leafrunData, hasLeafrunData := inputs["leafrun-data"]
	if hasLeafrunData && leafrunData != "/input.json" {
		return fmt.Errorf("leafrun-data is reserved to Core and must point to /input.json")
	}
	if requireLeafrunData && !hasLeafrunData {
		return fmt.Errorf("leafrun-data must point to /input.json")
	}
	maxCount := projectProfileInputCountMax
	maxBytes := projectProfileInputTotalMaxBytes
	if hasLeafrunData {
		maxCount++
		maxBytes += len("leafrun-data") + len("/input.json")
	}
	if len(inputs) > maxCount {
		return fmt.Errorf("at most %d Typst inputs are supported", maxCount)
	}
	total := 0
	for key, value := range inputs {
		if !projectInputKeyPattern.MatchString(key) || key == "" || !utf8.ValidString(value) || strings.ContainsRune(value, '\x00') || len(value) > projectProfileInputMaxBytes {
			return fmt.Errorf("Typst input keys and values must be bounded valid strings")
		}
		total += len(key) + len(value)
		if total > maxBytes {
			return fmt.Errorf("Typst inputs exceed the combined size limit")
		}
	}
	return nil
}

func sortedProjectProfileNames(profiles map[string]ProjectProfile) []string {
	keys := make([]string, 0, len(profiles))
	for key := range profiles {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	return keys
}

func sortedProjectStringKeys(values map[string]string) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	return keys
}

func cloneProjectProfile(profile ProjectProfile, name string) ProjectProfile {
	profile.Name = name
	if profile.Inputs != nil {
		inputs := make(map[string]string, len(profile.Inputs))
		for key, value := range profile.Inputs {
			inputs[key] = value
		}
		profile.Inputs = inputs
	}
	return profile
}

func cloneProjectProfiles(profiles map[string]ProjectProfile) map[string]ProjectProfile {
	cloned := make(map[string]ProjectProfile, len(profiles))
	for name, profile := range profiles {
		cloned[name] = cloneProjectProfile(profile, name)
	}
	return cloned
}

func cloneProjectManifest(manifest ProjectManifest) ProjectManifest {
	manifest.Templates = slices.Clone(manifest.Templates)
	manifest.Targets = slices.Clone(manifest.Targets)
	for i := range manifest.Targets {
		manifest.Targets[i].Steps = cloneProjectSteps(manifest.Targets[i].Steps)
		manifest.Targets[i].Order = slices.Clone(manifest.Targets[i].Order)
	}
	manifest.Profiles = cloneProjectProfiles(manifest.Profiles)
	return manifest
}

func cloneProjectSteps(steps []ProjectStep) []ProjectStep {
	cloned := slices.Clone(steps)
	for i := range cloned {
		cloned[i].DependsOn = slices.Clone(steps[i].DependsOn)
	}
	return cloned
}
