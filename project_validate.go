package documentcore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"path"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	jsonvalue "github.com/leafrun/leafrun-contracts/jsonvalue/v1"
)

func decodeProjectJSON(file string, data []byte) (any, error) {
	value, err := jsonvalue.Decode(data, ProjectManifestLimit)
	if err == nil {
		return value, nil
	}
	code := "invalid_json"
	message := err.Error()
	switch {
	case strings.Contains(message, "duplicate object key"):
		code = "duplicate_key"
	case strings.Contains(message, "invalid UTF-8"), strings.Contains(message, "surrogate"):
		code = "invalid_unicode"
	}
	return nil, projectError(file, "", code, message)
}

func readProjectRegular(ctx context.Context, files fs.FS, name string, limit int64) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !validProjectLogicalPath(name) {
		return nil, projectError(name, "", "invalid_logical_path", "resource path is not a logical project path")
	}
	file, err := files.Open(name)
	if err != nil {
		code := "resource_unavailable"
		if errors.Is(err, fs.ErrNotExist) {
			code = "missing_resource"
		}
		return nil, projectError(name, "", code, "resource cannot be opened as a project file")
	}
	info, statErr := file.Stat()
	if statErr != nil {
		return nil, errors.Join(projectError(name, "", "resource_unavailable", "resource metadata cannot be read"), file.Close(), statErr)
	}
	if !info.Mode().IsRegular() || info.Size() < 0 {
		return nil, errors.Join(projectError(name, "", "invalid_resource", "resource must be a regular file"), file.Close())
	}
	if info.Size() > limit {
		return nil, errors.Join(projectError(name, "", "resource_too_large", "resource exceeds the allowed byte limit"), file.Close())
	}
	data, readErr := io.ReadAll(io.LimitReader(file, limit+1))
	closeErr := file.Close()
	if readErr != nil || closeErr != nil {
		return nil, errors.Join(projectError(name, "", "resource_unavailable", "resource bytes cannot be read"), readErr, closeErr)
	}
	if int64(len(data)) > limit {
		return nil, projectError(name, "", "resource_too_large", "resource exceeds the allowed byte limit")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return data, nil
}

func checkProjectRegular(ctx context.Context, files fs.FS, name string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if !validProjectLogicalPath(name) {
		return projectError(name, "", "invalid_logical_path", "resource path is not a logical project path")
	}
	file, err := files.Open(name)
	if err != nil {
		code := "resource_unavailable"
		if errors.Is(err, fs.ErrNotExist) {
			code = "missing_resource"
		}
		return projectError(name, "", code, "resource cannot be opened as a project file")
	}
	info, statErr := file.Stat()
	closeErr := file.Close()
	if statErr != nil || closeErr != nil {
		return errors.Join(projectError(name, "", "resource_unavailable", "resource metadata cannot be read"), statErr, closeErr)
	}
	if !info.Mode().IsRegular() {
		return projectError(name, "", "invalid_resource", "resource must be a regular file")
	}
	return ctx.Err()
}

func validProjectLogicalPath(value string) bool {
	if value == "" || !utf8.ValidString(value) || strings.ContainsAny(value, "\\:") || path.IsAbs(value) || path.Clean(value) != value || !fs.ValidPath(value) {
		return false
	}
	for _, r := range value {
		if unicode.IsControl(r) {
			return false
		}
	}
	return value != "."
}

func validateProject(ctx context.Context, files fs.FS, manifest ProjectManifest) error {
	// Check the root itself so files, implicit/nonempty directories and empty
	// directories all fail before a source can become an immutable release.
	if _, err := fs.Stat(files, ".leafrun-input"); err == nil {
		return projectError(".leafrun-input", "", "reserved_input_root", "source project owns the reserved .leafrun-input root")
	} else if !errors.Is(err, fs.ErrNotExist) {
		return projectError(".leafrun-input", "", "resource_unavailable", "reserved input root ownership cannot be checked")
	}
	if manifest.FormatVersion != ProjectFormatVersion {
		return projectError(ProjectManifestFile, "/formatVersion", "unsupported_version", "only project format version 1 is supported")
	}
	if manifest.Name == "" || len(manifest.Name) > 128 || !utf8.ValidString(manifest.Name) {
		return projectError(ProjectManifestFile, "/name", "invalid_project_name", "name must be a nonempty UTF-8 string of at most 128 bytes")
	}
	for _, r := range manifest.Name {
		if unicode.IsControl(r) {
			return projectError(ProjectManifestFile, "/name", "invalid_project_name", "name cannot contain control characters")
		}
	}
	if manifest.Engine.TypstVersion != ProjectEngineVersion {
		return projectError(ProjectManifestFile, "/engine/typstVersion", "unsupported_engine_version", "only Typst 0.15.1 is supported")
	}
	if len(manifest.Templates) == 0 {
		return projectError(ProjectManifestFile, "/templates", "templates_required", "at least one template is required")
	}
	if len(manifest.Targets) == 0 {
		return projectError(ProjectManifestFile, "/targets", "targets_required", "at least one target is required")
	}
	if err := validateProjectProfiles(manifest.Profiles, manifest.DefaultProfile); err != nil {
		return err
	}

	templates := make(map[string]ProjectTemplate, len(manifest.Templates))
	schemas := make(map[string]struct{})
	schemaValidator := newProjectSchemaValidator(ctx, files)
	for index, template := range manifest.Templates {
		pointer := fmt.Sprintf("/templates/%d", index)
		if !validProjectID(template.ID) {
			return projectError(ProjectManifestFile, pointer+"/id", "invalid_template_id", "template ID must be a bounded identifier")
		}
		if _, exists := templates[template.ID]; exists {
			return projectError(ProjectManifestFile, pointer+"/id", "duplicate_id", "template IDs must be unique")
		}
		templates[template.ID] = template
		if !validProjectLogicalPath(template.Entrypoint) {
			return projectError(ProjectManifestFile, pointer+"/entrypoint", "invalid_logical_path", "entrypoint must be a logical project path")
		}
		if err := checkProjectRegular(ctx, files, template.Entrypoint); err != nil {
			return projectResourceAt(err, pointer+"/entrypoint")
		}
		if !validProjectLogicalPath(template.InputSchema) {
			return projectError(ProjectManifestFile, pointer+"/inputSchema", "invalid_logical_path", "input schema must be a logical project path")
		}
		if err := schemaValidator.Validate(template.InputSchema); err != nil {
			return projectSchemaAt(err, template.InputSchema, pointer+"/inputSchema")
		}
		schemas[template.InputSchema] = struct{}{}
	}

	targetIDs := make(map[string]struct{}, len(manifest.Targets))
	for index, target := range manifest.Targets {
		pointer := fmt.Sprintf("/targets/%d", index)
		if !validProjectID(target.ID) {
			return projectError(ProjectManifestFile, pointer+"/id", "invalid_target_id", "target ID must be a bounded identifier")
		}
		if _, exists := targetIDs[target.ID]; exists {
			return projectError(ProjectManifestFile, pointer+"/id", "duplicate_id", "target IDs must be unique")
		}
		targetIDs[target.ID] = struct{}{}
		if !validProjectLogicalPath(target.Output.Path) {
			return projectError(ProjectManifestFile, pointer+"/output/path", "invalid_logical_path", "output must be a logical result path")
		}
		if err := validateProjectOutputPath(files, target.Output.Path, pointer+"/output/path"); err != nil {
			return err
		}

		switch target.Kind {
		case ProjectTargetDocument:
			template, ok := templates[target.Template]
			if !ok {
				return projectError(ProjectManifestFile, pointer+"/template", "template_not_found", "document target must reference an existing template")
			}
			if target.InputSchema != "" && target.InputSchema != template.InputSchema {
				return projectError(ProjectManifestFile, pointer+"/inputSchema", "schema_conflict", "document target schema must match its template schema")
			}
			if target.Output.Format != "pdf" {
				return projectError(ProjectManifestFile, pointer+"/output/format", "unsupported_output_format", "document targets produce PDF")
			}
			if len(target.Steps) != 0 || len(target.Order) != 0 {
				return projectError(ProjectManifestFile, pointer, "invalid_target_shape", "document targets cannot define bundle steps or order")
			}
		case ProjectTargetBundle:
			if target.Template != "" {
				return projectError(ProjectManifestFile, pointer+"/template", "invalid_target_shape", "bundle targets select templates through steps")
			}
			if target.InputSchema == "" {
				return projectError(ProjectManifestFile, pointer+"/inputSchema", "schema_required", "bundle target requires an input schema")
			}
			if !validProjectLogicalPath(target.InputSchema) {
				return projectError(ProjectManifestFile, pointer+"/inputSchema", "invalid_logical_path", "input schema must be a logical project path")
			}
			if _, alreadyValidated := schemas[target.InputSchema]; !alreadyValidated {
				if err := schemaValidator.Validate(target.InputSchema); err != nil {
					return projectSchemaAt(err, target.InputSchema, pointer+"/inputSchema")
				}
				schemas[target.InputSchema] = struct{}{}
			}
			if target.Output.Format != "zip" {
				return projectError(ProjectManifestFile, pointer+"/output/format", "unsupported_output_format", "bundle targets produce ZIP archives")
			}
			if err := validateProjectBundle(target, index, templates); err != nil {
				return err
			}
		default:
			return projectError(ProjectManifestFile, pointer+"/kind", "unsupported_target_kind", "target kind must be document or bundle")
		}
	}
	return ctx.Err()
}

func validProjectID(value string) bool {
	return len(value) <= projectProfileNameMaxBytes && projectNamePattern.MatchString(value)
}

func validateProjectOutputPath(files fs.FS, output, pointer string) error {
	info, err := fs.Stat(files, output)
	if err == nil {
		return projectError(ProjectManifestFile, pointer, "output_overwrites_source", fmt.Sprintf("output path already names a project resource (%s)", info.Mode().Type()))
	}
	if !errors.Is(err, fs.ErrNotExist) {
		return projectError(ProjectManifestFile, pointer, "resource_unavailable", "output path cannot be checked against project resources")
	}
	for parent := path.Dir(output); parent != "."; parent = path.Dir(parent) {
		info, err = fs.Stat(files, parent)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return projectError(ProjectManifestFile, pointer, "resource_unavailable", "output path parent cannot be checked against project resources")
		}
		if !info.IsDir() {
			return projectError(ProjectManifestFile, pointer, "output_overwrites_source", "output path descends from a source file")
		}
	}
	return nil
}

func validateProjectBundle(target ProjectTarget, targetIndex int, templates map[string]ProjectTemplate) error {
	base := fmt.Sprintf("/targets/%d", targetIndex)
	if len(target.Steps) == 0 {
		return projectError(ProjectManifestFile, base+"/steps", "steps_required", "bundle target requires at least one step")
	}
	steps := make(map[string]ProjectStep, len(target.Steps))
	stepIndices := make(map[string]int, len(target.Steps))
	for index, step := range target.Steps {
		pointer := fmt.Sprintf("%s/steps/%d", base, index)
		if !validProjectID(step.ID) {
			return projectError(ProjectManifestFile, pointer+"/id", "invalid_step_id", "step ID must be a bounded identifier")
		}
		if _, exists := steps[step.ID]; exists {
			return projectError(ProjectManifestFile, pointer+"/id", "duplicate_id", "step IDs must be unique within a bundle")
		}
		steps[step.ID] = step
		stepIndices[step.ID] = index
		if _, exists := templates[step.Template]; !exists {
			return projectError(ProjectManifestFile, pointer+"/template", "template_not_found", "bundle step must reference an existing template")
		}
		if !validJSONPointer(step.InputPointer) {
			return projectError(ProjectManifestFile, pointer+"/inputPointer", "invalid_json_pointer", "inputPointer must be an RFC 6901 JSON Pointer")
		}
	}
	for index, step := range target.Steps {
		seen := make(map[string]struct{}, len(step.DependsOn))
		for depIndex, dependency := range step.DependsOn {
			pointer := fmt.Sprintf("%s/steps/%d/dependsOn/%d", base, index, depIndex)
			if _, duplicate := seen[dependency]; duplicate {
				return projectError(ProjectManifestFile, pointer, "duplicate_dependency", "step dependency IDs must be unique")
			}
			seen[dependency] = struct{}{}
			if _, exists := steps[dependency]; !exists {
				return projectError(ProjectManifestFile, pointer, "step_not_found", "dependency must reference an existing step")
			}
			if dependency == step.ID {
				return projectError(ProjectManifestFile, pointer, "bundle_cycle", "step cannot depend on itself")
			}
		}
	}
	if err := validateBundleAcyclic(target.Steps); err != nil {
		return projectError(ProjectManifestFile, base+"/steps", "bundle_cycle", "bundle dependencies must be acyclic")
	}
	positions := make(map[string]int, len(target.Order))
	if len(target.Order) != len(target.Steps) {
		return projectError(ProjectManifestFile, base+"/order", "invalid_bundle_order", "order must contain every step exactly once")
	}
	for index, id := range target.Order {
		if _, exists := stepIndices[id]; !exists {
			return projectError(ProjectManifestFile, fmt.Sprintf("%s/order/%d", base, index), "step_not_found", "order must reference an existing step")
		}
		if _, duplicate := positions[id]; duplicate {
			return projectError(ProjectManifestFile, fmt.Sprintf("%s/order/%d", base, index), "invalid_bundle_order", "order cannot repeat a step")
		}
		positions[id] = index
	}
	for _, step := range target.Steps {
		for _, dependency := range step.DependsOn {
			if positions[dependency] >= positions[step.ID] {
				return projectError(ProjectManifestFile, base+"/order", "invalid_bundle_order", "order must place dependencies before dependent steps")
			}
		}
	}
	return nil
}

func validateBundleAcyclic(steps []ProjectStep) error {
	byID := make(map[string]ProjectStep, len(steps))
	for _, step := range steps {
		byID[step.ID] = step
	}
	state := make(map[string]uint8, len(steps))
	var visit func(string) error
	visit = func(id string) error {
		if state[id] == 1 {
			return fmt.Errorf("dependency cycle at %q", id)
		}
		if state[id] == 2 {
			return nil
		}
		state[id] = 1
		for _, dependency := range byID[id].DependsOn {
			if err := visit(dependency); err != nil {
				return err
			}
		}
		state[id] = 2
		return nil
	}
	for _, step := range steps {
		if err := visit(step.ID); err != nil {
			return err
		}
	}
	return nil
}

func validJSONPointer(value string) bool {
	if value == "" {
		return true
	}
	if value[0] != '/' {
		return false
	}
	for index := 0; index < len(value); index++ {
		if value[index] != '~' {
			continue
		}
		index++
		if index >= len(value) || value[index] != '0' && value[index] != '1' {
			return false
		}
	}
	return utf8.ValidString(value)
}

func projectResourceAt(err error, pointer string) error {
	var diagnostic *ProjectValidationError
	if errors.As(err, &diagnostic) {
		return projectError(ProjectManifestFile, pointer, diagnostic.Code, diagnostic.Message)
	}
	return err
}

func projectSchemaAt(err error, file, pointer string) error {
	var diagnostic *ProjectValidationError
	if errors.As(err, &diagnostic) {
		if diagnostic.File == file && diagnostic.Code == "missing_resource" {
			return projectError(ProjectManifestFile, pointer, diagnostic.Code, diagnostic.Message)
		}
		return diagnostic
	}
	return projectError(file, pointer, "invalid_schema", err.Error())
}

func checkProjectJSONShape(value any, typ reflect.Type, pointer string) error {
	for typ.Kind() == reflect.Pointer {
		typ = typ.Elem()
	}
	if value == nil {
		return projectError(ProjectManifestFile, pointer, "invalid_type", "null is not allowed here")
	}
	switch typ.Kind() {
	case reflect.Struct:
		object, ok := value.(map[string]any)
		if !ok {
			return projectError(ProjectManifestFile, pointer, "invalid_type", "value must be an object")
		}
		fields := make(map[string]reflect.Type, typ.NumField())
		for index := 0; index < typ.NumField(); index++ {
			field := typ.Field(index)
			if !field.IsExported() {
				continue
			}
			tag := strings.Split(field.Tag.Get("json"), ",")[0]
			if tag == "-" {
				continue
			}
			if tag == "" {
				tag = field.Name
			}
			fields[tag] = field.Type
		}
		for _, key := range sortedAnyMapKeys(object) {
			fieldType, exists := fields[key]
			childPointer := pointer + "/" + escapeJSONPointer(key)
			if !exists {
				return projectError(ProjectManifestFile, childPointer, "unknown_field", "manifest contains an unsupported field")
			}
			if err := checkProjectJSONShape(object[key], fieldType, childPointer); err != nil {
				return err
			}
		}
	case reflect.Map:
		object, ok := value.(map[string]any)
		if !ok || typ.Key().Kind() != reflect.String {
			return projectError(ProjectManifestFile, pointer, "invalid_type", "value must be an object")
		}
		for _, key := range sortedAnyMapKeys(object) {
			if err := checkProjectJSONShape(object[key], typ.Elem(), pointer+"/"+escapeJSONPointer(key)); err != nil {
				return err
			}
		}
	case reflect.Slice, reflect.Array:
		array, ok := value.([]any)
		if !ok {
			return projectError(ProjectManifestFile, pointer, "invalid_type", "value must be an array")
		}
		for index, item := range array {
			if err := checkProjectJSONShape(item, typ.Elem(), pointer+"/"+strconv.Itoa(index)); err != nil {
				return err
			}
		}
	case reflect.String:
		if _, ok := value.(string); !ok {
			return projectError(ProjectManifestFile, pointer, "invalid_type", "value must be a string")
		}
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		number, ok := value.(json.Number)
		if !ok {
			return projectError(ProjectManifestFile, pointer, "invalid_type", "value must be an integer")
		}
		if _, err := strconv.ParseInt(string(number), 10, typ.Bits()); err != nil {
			return projectError(ProjectManifestFile, pointer, "invalid_value", "integer is outside the supported range")
		}
	default:
		return projectError(ProjectManifestFile, pointer, "invalid_type", "manifest field has an unsupported type")
	}
	return nil
}

func sortedAnyMapKeys(object map[string]any) []string {
	keys := make([]string, 0, len(object))
	for key := range object {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	return keys
}

func escapeJSONPointer(value string) string {
	return strings.ReplaceAll(strings.ReplaceAll(value, "~", "~0"), "/", "~1")
}
