package documentcore

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"reflect"
)

const (
	ProjectManifestFile        = "leafrun.json"
	ProjectFormatVersion       = 1
	ProjectEngineVersion       = "0.15.1"
	ProjectManifestLimit int64 = 4 << 20
)

// ProjectManifest is the strict, versioned description of a Leafrun project.
// It contains source selection and rendering metadata, never credentials or ACLs.
type ProjectManifest struct {
	FormatVersion  int                       `json:"formatVersion"`
	Name           string                    `json:"name"`
	Engine         ProjectEngine             `json:"engine"`
	Templates      []ProjectTemplate         `json:"templates"`
	Targets        []ProjectTarget           `json:"targets"`
	Profiles       map[string]ProjectProfile `json:"profiles"`
	DefaultProfile string                    `json:"defaultProfile"`
}

// ProjectEngine pins the exact renderer version used by project descriptors.
type ProjectEngine struct {
	TypstVersion string `json:"typstVersion"`
}

// ProjectTemplate selects an entrypoint and the schema for its input data.
type ProjectTemplate struct {
	ID          string `json:"id"`
	Entrypoint  string `json:"entrypoint"`
	InputSchema string `json:"inputSchema"`
}

// ProjectTargetKind distinguishes one-document outputs from E07 bundles.
type ProjectTargetKind string

const (
	ProjectTargetDocument ProjectTargetKind = "document"
	ProjectTargetBundle   ProjectTargetKind = "bundle"
)

// ProjectTarget selects one template or an ordered bundle of template steps.
type ProjectTarget struct {
	ID          string            `json:"id"`
	Kind        ProjectTargetKind `json:"kind"`
	Template    string            `json:"template,omitempty"`
	InputSchema string            `json:"inputSchema,omitempty"`
	Steps       []ProjectStep     `json:"steps,omitempty"`
	Order       []string          `json:"order,omitempty"`
	Output      ProjectOutput     `json:"output"`
}

// ProjectStep is one bundle stage. DependsOn defines a DAG edge from each
// dependency to this step; Order must already be a topological order.
type ProjectStep struct {
	ID           string   `json:"id"`
	Template     string   `json:"template"`
	InputPointer string   `json:"inputPointer"`
	DependsOn    []string `json:"dependsOn,omitempty"`
}

// ProjectOutput describes a logical result path, never a native filesystem path.
type ProjectOutput struct {
	Format string `json:"format"`
	Path   string `json:"path"`
}

// ProjectProfile contains Leafrun template inputs and supported PDF export
// settings. The profile's map key is its immutable name.
type ProjectProfile struct {
	Name        string            `json:"-"`
	Inputs      map[string]string `json:"inputs,omitempty"`
	PDFStandard string            `json:"pdfStandard,omitempty"`
}

// ProjectValidationError identifies one stable contract failure. Pointer is an
// RFC 6901 JSON Pointer into File; the empty pointer identifies the whole file.
type ProjectValidationError struct {
	File    string
	Pointer string
	Code    string
	Message string
}

func (e *ProjectValidationError) Error() string {
	if e == nil {
		return "project validation failed"
	}
	location := e.File
	if e.Pointer != "" {
		location += "#" + e.Pointer
	}
	if e.Message == "" {
		return fmt.Sprintf("project %s: %s", location, e.Code)
	}
	return fmt.Sprintf("project %s: %s: %s", location, e.Code, e.Message)
}

func projectError(file, pointer, code, message string) error {
	return &ProjectValidationError{File: file, Pointer: pointer, Code: code, Message: message}
}

// ReadProject reads and technically validates leafrun.json and every resource
// it references. It does not compile Typst or execute bundle steps.
func ReadProject(ctx context.Context, files fs.FS) (ProjectManifest, error) {
	if err := ctx.Err(); err != nil {
		return ProjectManifest{}, err
	}
	if files == nil {
		return ProjectManifest{}, projectError(ProjectManifestFile, "", "filesystem_required", "project filesystem is required")
	}
	data, err := readProjectRegular(ctx, files, ProjectManifestFile, ProjectManifestLimit)
	if err != nil {
		return ProjectManifest{}, err
	}
	value, err := decodeProjectJSON(ProjectManifestFile, data)
	if err != nil {
		return ProjectManifest{}, err
	}
	if err := checkProjectJSONShape(value, projectManifestType, ""); err != nil {
		return ProjectManifest{}, err
	}
	var manifest ProjectManifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		return ProjectManifest{}, projectError(ProjectManifestFile, "", "invalid_value", err.Error())
	}
	if err := validateProject(ctx, files, manifest); err != nil {
		return ProjectManifest{}, err
	}
	return cloneProjectManifest(manifest), nil
}

var projectManifestType = reflect.TypeFor[ProjectManifest]()
