package documentcore

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"slices"
)

// TargetStepDescriptor is one already ordered bundle step. Core describes it
// for callers; E07 owns execution of bundles.
type TargetStepDescriptor struct {
	ID           string   `json:"id"`
	TemplateID   string   `json:"templateId"`
	Entrypoint   string   `json:"entrypoint"`
	InputPointer string   `json:"inputPointer"`
	DependsOn    []string `json:"dependsOn"`
}

// ProjectResourceDigest identifies captured project or font bytes without
// exposing native source paths or provenance.
type ProjectResourceDigest struct {
	Kind      string `json:"kind"`
	FontIndex int    `json:"fontIndex,omitempty"`
	Path      string `json:"path"`
	Size      int64  `json:"size"`
	SHA256    string `json:"sha256"`
}

// TargetDescriptor is the immutable Core resolution of a manifest target and
// profile against one verified snapshot content ID.
type TargetDescriptor struct {
	TargetID          string                    `json:"targetId"`
	Kind              ProjectTargetKind         `json:"kind"`
	TemplateID        string                    `json:"templateId,omitempty"`
	Entrypoint        string                    `json:"entrypoint,omitempty"`
	InputSchema       string                    `json:"inputSchema"`
	Output            ProjectOutput             `json:"output"`
	Steps             []TargetStepDescriptor    `json:"steps"`
	Profiles          map[string]ProjectProfile `json:"profiles"`
	DefaultProfile    string                    `json:"defaultProfile"`
	SelectedProfile   string                    `json:"selectedProfile"`
	ResolvedProfile   ProjectProfile            `json:"resolvedProfile"`
	TypstVersion      string                    `json:"typstVersion"`
	SnapshotContentID string                    `json:"snapshotContentId"`
	Resources         []ProjectResourceDigest   `json:"resources"`
	Digest            string                    `json:"digest"`
	ExecutionDigest   string                    `json:"executionDigest"`
	Target            ProjectTarget             `json:"target"`
}

// DescribeTarget validates the supplied manifest and snapshot binding, then
// resolves a target and profile without consulting a live source tree.
// files must be an immutable captured filesystem, such as Snapshot.FS().
func DescribeTarget(ctx context.Context, files fs.FS, manifest ProjectManifest, snapshot SnapshotManifest, targetID, profile string) (TargetDescriptor, error) {
	if err := ctx.Err(); err != nil {
		return TargetDescriptor{}, err
	}
	if files == nil {
		return TargetDescriptor{}, projectError(ProjectManifestFile, "", "filesystem_required", "project filesystem is required")
	}
	if err := ValidateSnapshotManifest(snapshot); err != nil {
		return TargetDescriptor{}, projectError(ProjectManifestFile, "", "invalid_snapshot_manifest", "snapshot manifest is invalid: "+err.Error())
	}
	limits, err := snapshotLimits(SnapshotLimits{})
	if err != nil {
		return TargetDescriptor{}, err
	}
	actual, err := scanSnapshotTree(ctx, files, &snapshotBudget{limits: limits})
	if err != nil {
		return TargetDescriptor{}, projectError(ProjectManifestFile, "", "snapshot_unavailable", "project filesystem could not be verified against its snapshot")
	}
	if !sameManifestTree(manifestSnapshotScan(snapshot.Files, snapshot.Directories), actual) {
		return TargetDescriptor{}, projectError(ProjectManifestFile, "", "snapshot_mismatch", "project filesystem differs from the supplied immutable snapshot")
	}
	verifiedManifest, err := ReadProject(ctx, files)
	if err != nil {
		return TargetDescriptor{}, err
	}
	equal, err := projectManifestsEqual(manifest, verifiedManifest)
	if err != nil {
		return TargetDescriptor{}, fmt.Errorf("compare project manifest: %w", err)
	}
	if !equal {
		return TargetDescriptor{}, projectError(ProjectManifestFile, "", "manifest_mismatch", "supplied manifest differs from the manifest captured in the immutable snapshot")
	}
	manifest = verifiedManifest

	var selected *ProjectTarget
	for index := range manifest.Targets {
		if manifest.Targets[index].ID == targetID {
			selected = &manifest.Targets[index]
			break
		}
	}
	if selected == nil {
		return TargetDescriptor{}, projectError(ProjectManifestFile, "/targets/"+escapeJSONPointer(targetID), "target_not_found", "target does not exist")
	}
	templates := make(map[string]ProjectTemplate, len(manifest.Templates))
	for _, template := range manifest.Templates {
		templates[template.ID] = template
	}
	if profile == "" {
		profile = manifest.DefaultProfile
	}
	resolved, exists := manifest.Profiles[profile]
	if !exists {
		return TargetDescriptor{}, projectError(ProjectManifestFile, "/profiles/"+escapeJSONPointer(profile), "profile_not_found", "profile does not exist")
	}
	resolved = cloneProjectProfile(resolved, profile)

	target := cloneProjectTarget(*selected)
	inputSchema := target.InputSchema
	descriptor := TargetDescriptor{
		TargetID:          target.ID,
		Kind:              target.Kind,
		InputSchema:       inputSchema,
		Output:            target.Output,
		Profiles:          cloneProjectProfiles(manifest.Profiles),
		DefaultProfile:    manifest.DefaultProfile,
		SelectedProfile:   profile,
		ResolvedProfile:   resolved,
		TypstVersion:      manifest.Engine.TypstVersion,
		SnapshotContentID: snapshot.ID,
		Resources:         projectResourceDigests(snapshot),
		Target:            target,
		Steps:             []TargetStepDescriptor{},
	}
	if target.Kind == ProjectTargetDocument {
		template, exists := templates[target.Template]
		if !exists {
			return TargetDescriptor{}, projectError(ProjectManifestFile, "/targets/"+escapeJSONPointer(targetID)+"/template", "template_not_found", "document target must reference an existing template")
		}
		descriptor.TemplateID = template.ID
		descriptor.Entrypoint = template.Entrypoint
		if descriptor.InputSchema == "" {
			descriptor.InputSchema = template.InputSchema
		}
		descriptor.Target.InputSchema = descriptor.InputSchema
	} else {
		orderedSteps := make(map[string]ProjectStep, len(target.Steps))
		for _, step := range target.Steps {
			orderedSteps[step.ID] = step
		}
		descriptor.Steps = make([]TargetStepDescriptor, 0, len(target.Order))
		for _, stepID := range target.Order {
			step := orderedSteps[stepID]
			template, exists := templates[step.Template]
			if !exists {
				return TargetDescriptor{}, projectError(ProjectManifestFile, "/targets/"+escapeJSONPointer(targetID)+"/steps", "template_not_found", "bundle step must reference an existing template")
			}
			descriptor.Steps = append(descriptor.Steps, TargetStepDescriptor{
				ID:           step.ID,
				TemplateID:   template.ID,
				Entrypoint:   template.Entrypoint,
				InputPointer: step.InputPointer,
				DependsOn:    slices.Clone(step.DependsOn),
			})
		}
	}
	base, err := targetDescriptorDigest(descriptor)
	if err != nil {
		return TargetDescriptor{}, err
	}
	descriptor.Digest = base
	descriptor.ExecutionDigest, err = projectExecutionDigest(base, profile, resolved)
	if err != nil {
		return TargetDescriptor{}, err
	}
	if err := ctx.Err(); err != nil {
		return TargetDescriptor{}, err
	}
	return cloneTargetDescriptor(descriptor), nil
}

func targetDescriptorDigest(descriptor TargetDescriptor) (string, error) {
	material := struct {
		Target            ProjectTarget             `json:"target"`
		TemplateID        string                    `json:"templateId,omitempty"`
		Entrypoint        string                    `json:"entrypoint,omitempty"`
		InputSchema       string                    `json:"inputSchema"`
		Steps             []TargetStepDescriptor    `json:"steps"`
		Profiles          map[string]ProjectProfile `json:"profiles"`
		DefaultProfile    string                    `json:"defaultProfile"`
		TypstVersion      string                    `json:"typstVersion"`
		SnapshotContentID string                    `json:"snapshotContentId"`
		Resources         []ProjectResourceDigest   `json:"resources"`
	}{
		Target: descriptor.Target, TemplateID: descriptor.TemplateID, Entrypoint: descriptor.Entrypoint,
		InputSchema: descriptor.InputSchema, Steps: descriptor.Steps, Profiles: descriptor.Profiles,
		DefaultProfile: descriptor.DefaultProfile, TypstVersion: descriptor.TypstVersion,
		SnapshotContentID: descriptor.SnapshotContentID, Resources: descriptor.Resources,
	}
	data, err := json.Marshal(material)
	if err != nil {
		return "", fmt.Errorf("encode target descriptor: %w", err)
	}
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:]), nil
}

func projectManifestsEqual(left, right ProjectManifest) (bool, error) {
	leftJSON, err := json.Marshal(left)
	if err != nil {
		return false, err
	}
	rightJSON, err := json.Marshal(right)
	if err != nil {
		return false, err
	}
	return bytes.Equal(leftJSON, rightJSON), nil
}

func projectExecutionDigest(baseDigest, profile string, resolved ProjectProfile) (string, error) {
	material := struct {
		DescriptorDigest string         `json:"descriptorDigest"`
		Profile          string         `json:"profile"`
		Resolved         ProjectProfile `json:"resolved"`
	}{DescriptorDigest: baseDigest, Profile: profile, Resolved: cloneProjectProfile(resolved, profile)}
	data, err := json.Marshal(material)
	if err != nil {
		return "", fmt.Errorf("encode profile execution digest: %w", err)
	}
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:]), nil
}

func projectResourceDigests(snapshot SnapshotManifest) []ProjectResourceDigest {
	resources := make([]ProjectResourceDigest, 0, len(snapshot.Files))
	for _, file := range snapshot.Files {
		resources = append(resources, ProjectResourceDigest{Kind: "project", Path: file.Path, Size: file.Size, SHA256: file.SHA256})
	}
	for index, font := range snapshot.Fonts {
		for _, file := range font.Files {
			resources = append(resources, ProjectResourceDigest{Kind: "font", FontIndex: index, Path: file.Path, Size: file.Size, SHA256: file.SHA256})
		}
	}
	return resources
}

func cloneProjectTarget(target ProjectTarget) ProjectTarget {
	target.Steps = cloneProjectSteps(target.Steps)
	target.Order = slices.Clone(target.Order)
	return target
}

func cloneTargetDescriptor(descriptor TargetDescriptor) TargetDescriptor {
	descriptor.Steps = slices.Clone(descriptor.Steps)
	for index := range descriptor.Steps {
		descriptor.Steps[index].DependsOn = slices.Clone(descriptor.Steps[index].DependsOn)
	}
	descriptor.Profiles = cloneProjectProfiles(descriptor.Profiles)
	descriptor.ResolvedProfile = cloneProjectProfile(descriptor.ResolvedProfile, descriptor.SelectedProfile)
	descriptor.Resources = slices.Clone(descriptor.Resources)
	descriptor.Target = cloneProjectTarget(descriptor.Target)
	return descriptor
}
