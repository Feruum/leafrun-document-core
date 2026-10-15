package documentcore

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"reflect"
	"slices"
	"strconv"
	"strings"

	jsonvalue "github.com/leafrun/leafrun-contracts/jsonvalue/v1"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

const (
	projectRequestJSONLimit  = 1 << 20
	projectPreparedJSONLimit = 1 << 20
	projectDefaultDepthLimit = 64
)

// PreparedInput is the exact JSON object and Typst input map that will be
// persisted for one execution. JSON is canonical Leafrun JSON; callers own all
// slices and maps returned by Core.
type PreparedInput struct {
	JSON   json.RawMessage   `json:"json"`
	Files  []PreparedFile    `json:"files"`
	Inputs map[string]string `json:"inputs"`
}

// PrepareParameters applies valid static defaults to absent values, binds
// authorized file references to stable snapshot paths, then validates the
// complete object against the captured Draft 2020-12 schema.
func PrepareParameters(ctx context.Context, files fs.FS, schemaPath string, raw []byte, refs []FileRef) (PreparedInput, error) {
	if err := ctx.Err(); err != nil {
		return PreparedInput{}, err
	}
	if files == nil {
		return PreparedInput{}, projectError(schemaPath, "", "filesystem_required", "project filesystem is required")
	}
	if !validProjectLogicalPath(schemaPath) {
		return PreparedInput{}, projectError(schemaPath, "", "invalid_logical_path", "schema path is not a logical project path")
	}
	if int64(len(raw)) > projectRequestJSONLimit {
		return PreparedInput{}, projectError(schemaPath, "", "parameters_too_large", "parameters exceed 1 MiB")
	}
	value, err := jsonvalue.Decode(raw, projectRequestJSONLimit)
	if err != nil {
		return PreparedInput{}, projectParametersJSONError(schemaPath, err)
	}
	parameters, ok := value.(map[string]any)
	if !ok {
		return PreparedInput{}, projectError(schemaPath, "", "invalid_parameters", "parameters must be a JSON object")
	}
	validator := newProjectSchemaValidator(ctx, files)
	compiled, err := validator.Compile(schemaPath)
	if err != nil {
		return PreparedInput{}, err
	}
	if err := applyProjectDefaults(ctx, schemaPath, parameters, compiled); err != nil {
		return PreparedInput{}, err
	}
	preparedFiles, err := prepareProjectFileRefs(schemaPath, validator.loader, compiled, parameters, refs, false)
	if err != nil {
		return PreparedInput{}, err
	}
	preparedJSON, err := canonicalProjectObject(parameters)
	if err != nil {
		if errors.Is(err, errPreparedParametersTooLarge) {
			return PreparedInput{}, projectError(schemaPath, "", "prepared_parameters_too_large", "prepared parameters exceed 1 MiB")
		}
		return PreparedInput{}, projectError(schemaPath, "", "invalid_parameters", "prepared parameters could not be encoded")
	}
	if len(preparedJSON) > projectPreparedJSONLimit {
		return PreparedInput{}, projectError(schemaPath, "", "prepared_parameters_too_large", "prepared parameters exceed 1 MiB")
	}
	if err := validatePreparedSchemaValue(schemaPath, compiled, parameters); err != nil {
		return PreparedInput{}, err
	}
	inputs := map[string]string{"leafrun-data": "/input.json"}
	prepared := PreparedInput{JSON: json.RawMessage(bytes.Clone(preparedJSON)), Files: preparedFiles, Inputs: inputs}
	return clonePreparedInput(prepared), ctx.Err()
}

// VerifyPreparedInput validates persisted prepared bytes and file bindings. It
// never inserts or changes defaults, so a child can verify one exact parent
// preparation without repeating the preparation step.
func VerifyPreparedInput(ctx context.Context, files fs.FS, schemaPath string, prepared PreparedInput) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if files == nil {
		return projectError(schemaPath, "", "filesystem_required", "project filesystem is required")
	}
	if int64(len(prepared.JSON)) > projectPreparedJSONLimit {
		return projectError(schemaPath, "", "prepared_parameters_too_large", "prepared parameters exceed 1 MiB")
	}
	normalized, err := jsonvalue.NormalizeObject(prepared.JSON)
	if err != nil {
		return projectParametersJSONError(schemaPath, err)
	}
	if !bytes.Equal(normalized, prepared.JSON) {
		return projectError(schemaPath, "", "prepared_json_not_canonical", "prepared JSON must use canonical Leafrun JSON encoding")
	}
	value, err := jsonvalue.Decode(prepared.JSON, projectPreparedJSONLimit)
	if err != nil {
		return projectParametersJSONError(schemaPath, err)
	}
	parameters, ok := value.(map[string]any)
	if !ok {
		return projectError(schemaPath, "", "invalid_parameters", "prepared parameters must be a JSON object")
	}
	if err := validatePreparedInputStructure(schemaPath, prepared, parameters); err != nil {
		return err
	}
	validator := newProjectSchemaValidator(ctx, files)
	compiled, err := validator.Compile(schemaPath)
	if err != nil {
		return err
	}
	preparedFiles, err := prepareProjectFileRefs(schemaPath, validator.loader, compiled, parameters, prepared.FileRefs(), true)
	if err != nil {
		return err
	}
	if !equalPreparedFiles(preparedFiles, prepared.Files) {
		return projectError(schemaPath, "", "prepared_file_binding_mismatch", "prepared file descriptors do not match schema bindings")
	}
	if err := verifyProjectDefaultsPresent(ctx, schemaPath, parameters, compiled); err != nil {
		return err
	}
	return validatePreparedSchemaValue(schemaPath, compiled, parameters)
}

// FileRefs returns independent request-shaped descriptors for prepared files.
// It is exported to let trusted adapters assemble an explicit resolver request.
func (prepared PreparedInput) FileRefs() []FileRef {
	refs := make([]FileRef, len(prepared.Files))
	for i, file := range prepared.Files {
		refs[i] = FileRef{Pointer: file.Pointer, ResourceID: file.ResourceID, Filename: file.Filename, SHA256: file.SHA256, SizeBytes: file.SizeBytes}
	}
	return refs
}

func clonePreparedInput(prepared PreparedInput) PreparedInput {
	prepared.JSON = bytes.Clone(prepared.JSON)
	prepared.Files = slices.Clone(prepared.Files)
	if prepared.Inputs != nil {
		inputs := make(map[string]string, len(prepared.Inputs))
		for key, value := range prepared.Inputs {
			inputs[key] = value
		}
		prepared.Inputs = inputs
	}
	return prepared
}

func projectParametersJSONError(file string, err error) error {
	code := "invalid_json"
	message := err.Error()
	switch {
	case strings.Contains(message, "duplicate object key"):
		code = "duplicate_key"
	case strings.Contains(message, "invalid UTF-8"), strings.Contains(message, "surrogate"):
		code = "invalid_unicode"
	case strings.Contains(message, "maximum depth"):
		code = "json_depth_exceeded"
	case strings.Contains(message, "1 MiB"), strings.Contains(message, "exceeds limit"):
		code = "parameters_too_large"
	}
	return projectError(file, "", code, message)
}

func canonicalProjectObject(value map[string]any) ([]byte, error) {
	if _, ok := projectJSONSize(value, projectPreparedJSONLimit); !ok {
		return nil, errPreparedParametersTooLarge
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	return jsonvalue.NormalizeObject(encoded)
}

func validatePreparedSchemaValue(schemaPath string, schema *jsonschema.Schema, value any) error {
	if err := schema.Validate(value); err != nil {
		pointer := ""
		var validation *jsonschema.ValidationError
		if errors.As(err, &validation) {
			pointer, message := projectParameterDiagnostic(validation)
			return projectError(schemaPath, pointer, "invalid_parameters", message)
		}
		return projectError(schemaPath, pointer, "invalid_parameters", err.Error())
	}
	return nil
}

func projectJSONPointer(tokens []string) string {
	var builder strings.Builder
	for _, token := range tokens {
		builder.WriteByte('/')
		builder.WriteString(escapeJSONPointer(token))
	}
	return builder.String()
}

func applyProjectDefaults(ctx context.Context, schemaPath string, value any, schema *jsonschema.Schema) error {
	size, ok := projectJSONSize(value, projectPreparedJSONLimit)
	if !ok {
		return projectError(schemaPath, "", "prepared_parameters_too_large", "prepared parameters exceed 1 MiB")
	}
	budget := projectDefaultBudget{remaining: projectPreparedJSONLimit - size}
	return applyDefaultsAt(ctx, schemaPath, value, []*jsonschema.Schema{schema}, "", 0, &budget)
}

func validateProjectSchemaAnnotations(file string, loader *projectSchemaLoader, root *jsonschema.Schema) error {
	type graphVisit struct {
		schema    *jsonschema.Schema
		ambiguous bool
	}
	visited := make(map[graphVisit]bool)
	hasAnnotations := false
	dynamicLocation := ""
	var inspect func(*jsonschema.Schema, bool) error
	inspect = func(schema *jsonschema.Schema, ambiguous bool) error {
		if schema == nil || visited[graphVisit{schema: schema, ambiguous: ambiguous}] {
			return nil
		}
		visited[graphVisit{schema: schema, ambiguous: ambiguous}] = true
		if raw, ok := projectSchemaRawAtLocation(loader, schema.Location); ok {
			if marker, exists := raw["x-leafrun-file"]; exists {
				hasAnnotations = true
				if ambiguous {
					return projectSchemaDiagnostic(file, schema.Location, "ambiguous_file_schema", "file fields beneath conditional or unevaluated branches are unsupported")
				}
				if marker != true {
					return projectSchemaDiagnostic(file, schema.Location, "invalid_file_schema", "x-leafrun-file must be true on a string field")
				}
				if raw["type"] != "string" {
					return projectSchemaDiagnostic(file, schema.Location, "invalid_file_schema", "x-leafrun-file is supported only on fields with type string")
				}
			}
		}
		if schema.Default != nil {
			hasAnnotations = true
			if ambiguous {
				return projectSchemaDiagnostic(file, schema.Location, "ambiguous_default", "defaults beneath conditional or union branches are unsupported")
			}
			if projectSchemaIsFile(loader, []*jsonschema.Schema{schema}) {
				return projectSchemaDiagnostic(file, schema.Location, "file_default_forbidden", "file fields cannot define default paths")
			}
			if err := schema.Validate(*schema.Default); err != nil {
				return projectSchemaDiagnostic(file, schema.Location, "invalid_default", "default value does not satisfy its schema")
			}
		}
		if schema.Ref != nil {
			if err := inspect(schema.Ref, ambiguous); err != nil {
				return err
			}
		}
		if schema.DynamicRef != nil {
			if dynamicLocation == "" {
				dynamicLocation = schema.Location
			}
			if err := inspect(schema.DynamicRef.Ref, true); err != nil {
				return err
			}
		}
		if schema.RecursiveRef != nil && dynamicLocation == "" {
			dynamicLocation = schema.Location
		}
		if err := inspect(schema.RecursiveRef, true); err != nil {
			return err
		}
		for _, branch := range schema.AllOf {
			if err := inspect(branch, ambiguous); err != nil {
				return err
			}
		}
		for _, branch := range schema.AnyOf {
			if err := inspect(branch, true); err != nil {
				return err
			}
		}
		for _, branch := range schema.OneOf {
			if err := inspect(branch, true); err != nil {
				return err
			}
		}
		for _, branch := range []*jsonschema.Schema{schema.If, schema.Then, schema.Else, schema.Not, schema.Contains, schema.UnevaluatedItems, schema.UnevaluatedProperties, schema.PropertyNames, schema.ContentSchema} {
			if err := inspect(branch, true); err != nil {
				return err
			}
		}
		for _, child := range schema.Properties {
			if err := inspect(child, ambiguous); err != nil {
				return err
			}
		}
		for _, child := range schema.PatternProperties {
			if err := inspect(child, ambiguous); err != nil {
				return err
			}
		}
		for _, child := range schema.DependentSchemas {
			if err := inspect(child, true); err != nil {
				return err
			}
		}
		for _, dependency := range schema.Dependencies {
			if child, ok := dependency.(*jsonschema.Schema); ok {
				if err := inspect(child, true); err != nil {
					return err
				}
			}
		}
		if child, ok := schema.AdditionalProperties.(*jsonschema.Schema); ok {
			if err := inspect(child, ambiguous); err != nil {
				return err
			}
		}
		if child, ok := schema.Items.(*jsonschema.Schema); ok {
			if err := inspect(child, ambiguous); err != nil {
				return err
			}
		}
		if schema.Items2020 != nil {
			if err := inspect(schema.Items2020, ambiguous); err != nil {
				return err
			}
		}
		for _, child := range schema.PrefixItems {
			if err := inspect(child, ambiguous); err != nil {
				return err
			}
		}
		return nil
	}
	if err := inspect(root, false); err != nil {
		return err
	}
	// A dynamic reference can select an outer anchor instead of its compiled
	// fallback. Version 1 prepares only static local refs; accepting annotations
	// in a dynamic scope could leave file fields/defaults unenforced at runtime.
	if dynamicLocation != "" && hasAnnotations {
		return projectSchemaDiagnostic(file, dynamicLocation, "unsupported_dynamic_annotations", "dynamic or recursive references cannot be combined with reachable defaults or file fields")
	}
	return validateProjectDefaultCombinations(file, loader, []*jsonschema.Schema{root}, "", make(map[string]bool), make(map[string]bool), 0)
}

func validateProjectDefaultCombinations(file string, loader *projectSchemaLoader, schemas []*jsonschema.Schema, pointer string, active, completed map[string]bool, depth int) error {
	if depth > projectDefaultDepthLimit {
		return projectError(file, pointer, "recursive_default_expansion", "schema default expansion exceeds the nesting limit")
	}
	expanded := expandProjectUnconditionalSchemas(schemas)
	key := projectSchemaGroupKey(expanded)
	if completed[key] {
		return nil
	}
	if active[key] {
		for _, schema := range expanded {
			if schema.Default != nil {
				switch (*schema.Default).(type) {
				case map[string]any, []any:
					return projectSchemaDiagnostic(file, schema.Location, "recursive_default_expansion", "recursive object or array defaults cannot be expanded without a bound")
				}
			}
		}
		return nil
	}
	active[key] = true
	defer delete(active, key)
	defaultValue, hasDefault, err := projectDefaultValue(file, pointer, expanded)
	if err != nil {
		return err
	}
	if hasDefault && projectDefaultContainsFile(loader, defaultValue, expanded) {
		return projectSchemaDiagnostic(file, firstProjectSchemaLocation(expanded), "file_default_forbidden", "defaults cannot contain values for file fields")
	}
	properties := make(map[string][]*jsonschema.Schema)
	for _, schema := range expanded {
		for name, child := range schema.Properties {
			properties[name] = append(properties[name], child)
		}
	}
	for _, name := range sortedSchemaPropertyNames(properties) {
		children := projectPropertySchemas(expanded, name)
		childPointer := pointer + "/" + escapeJSONPointer(name)
		_, hasDefault, err := projectDefaultValue(file, childPointer, children)
		if err != nil {
			return err
		}
		if projectSchemaIsFile(loader, children) && hasDefault {
			return projectSchemaDiagnostic(file, firstProjectSchemaLocation(children), "file_default_forbidden", "file fields cannot define default paths")
		}
		if err := validateProjectDefaultCombinations(file, loader, children, childPointer, active, completed, depth+1); err != nil {
			return err
		}
	}
	patternNames := make([]string, 0)
	for _, schema := range expanded {
		for pattern := range schema.PatternProperties {
			patternNames = append(patternNames, pattern.String())
		}
	}
	slices.Sort(patternNames)
	patternNames = slices.Compact(patternNames)
	for _, pattern := range patternNames {
		children := []*jsonschema.Schema{}
		for _, schema := range expanded {
			for compiledPattern, child := range schema.PatternProperties {
				if compiledPattern.String() == pattern {
					children = append(children, child)
				}
			}
		}
		if err := validateProjectDefaultCombinations(file, loader, dedupeProjectSchemas(children), pointer+"/*", active, completed, depth+1); err != nil {
			return err
		}
	}
	additional := []*jsonschema.Schema{}
	for _, schema := range expanded {
		if child, ok := schema.AdditionalProperties.(*jsonschema.Schema); ok {
			additional = append(additional, child)
		}
	}
	if len(additional) > 0 {
		if err := validateProjectDefaultCombinations(file, loader, dedupeProjectSchemas(additional), pointer+"/*", active, completed, depth+1); err != nil {
			return err
		}
	}
	for index, children := range projectPrefixItemSchemas(expanded) {
		if err := validateProjectDefaultCombinations(file, loader, children, pointer+"/"+strconv.Itoa(index), active, completed, depth+1); err != nil {
			return err
		}
	}
	items := projectAnyItemSchemas(expanded)
	if len(items) > 0 {
		if err := validateProjectDefaultCombinations(file, loader, items, pointer+"/*", active, completed, depth+1); err != nil {
			return err
		}
	}
	completed[key] = true
	return nil
}

// The literal bounds this walk even when the schema contains recursive refs.
// Check combined unconditional branches so a default in one allOf branch cannot
// hide a file marker in another branch.
func projectDefaultContainsFile(loader *projectSchemaLoader, value any, schemas []*jsonschema.Schema) bool {
	if projectSchemaIsFile(loader, schemas) {
		return true
	}
	switch value := value.(type) {
	case map[string]any:
		for name, child := range value {
			if projectDefaultContainsFile(loader, child, projectPropertySchemas(schemas, name)) {
				return true
			}
		}
	case []any:
		for index, child := range value {
			if projectDefaultContainsFile(loader, child, projectItemSchemas(schemas, index)) {
				return true
			}
		}
	}
	return false
}

func projectSchemaGroupKey(schemas []*jsonschema.Schema) string {
	locations := make([]string, 0, len(schemas))
	for _, schema := range schemas {
		locations = append(locations, schema.Location)
	}
	slices.Sort(locations)
	return strings.Join(locations, "\x00")
}

func projectPrefixItemSchemas(schemas []*jsonschema.Schema) [][]*jsonschema.Schema {
	max := 0
	for _, schema := range expandProjectUnconditionalSchemas(schemas) {
		if len(schema.PrefixItems) > max {
			max = len(schema.PrefixItems)
		}
	}
	result := make([][]*jsonschema.Schema, max)
	for _, schema := range expandProjectUnconditionalSchemas(schemas) {
		for index, child := range schema.PrefixItems {
			result[index] = append(result[index], child)
		}
	}
	for index := range result {
		result[index] = dedupeProjectSchemas(result[index])
	}
	return result
}

func projectAnyItemSchemas(schemas []*jsonschema.Schema) []*jsonschema.Schema {
	result := []*jsonschema.Schema{}
	for _, schema := range expandProjectUnconditionalSchemas(schemas) {
		if schema.Items2020 != nil {
			result = append(result, schema.Items2020)
		}
		if child, ok := schema.Items.(*jsonschema.Schema); ok {
			result = append(result, child)
		}
	}
	return dedupeProjectSchemas(result)
}

func firstProjectSchemaLocation(schemas []*jsonschema.Schema) string {
	for _, schema := range schemas {
		return schema.Location
	}
	return ""
}

func applyDefaultsAt(ctx context.Context, schemaPath string, value any, schemas []*jsonschema.Schema, pointer string, depth int, budget *projectDefaultBudget) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if depth > projectDefaultDepthLimit {
		return projectError(schemaPath, pointer, "recursive_default_expansion", "default expansion exceeds the nesting limit")
	}
	schemas = expandProjectUnconditionalSchemas(schemas)
	switch instance := value.(type) {
	case map[string]any:
		// Apply defaults beneath every existing object member, including names
		// covered by patternProperties or additionalProperties.
		for _, name := range sortedAnyMapKeys(instance) {
			childSchemas := projectPropertySchemas(schemas, name)
			if len(childSchemas) == 0 {
				continue
			}
			childPointer := pointer + "/" + escapeJSONPointer(name)
			if err := applyDefaultsAt(ctx, schemaPath, instance[name], childSchemas, childPointer, depth+1, budget); err != nil {
				return err
			}
		}
		properties := make(map[string][]*jsonschema.Schema)
		for _, schema := range schemas {
			for name, child := range schema.Properties {
				properties[name] = append(properties[name], child)
			}
		}
		for _, name := range sortedSchemaPropertyNames(properties) {
			childSchemas := projectPropertySchemas(schemas, name)
			childPointer := pointer + "/" + escapeJSONPointer(name)
			childValue, exists := instance[name]
			if !exists {
				defaultValue, hasDefault, err := projectDefaultValue(schemaPath, childPointer, childSchemas)
				if err != nil {
					return err
				}
				if hasDefault {
					if !budget.reserveProperty(name, defaultValue, len(instance) > 0) {
						return projectError(schemaPath, childPointer, "prepared_parameters_too_large", "prepared parameters exceed 1 MiB")
					}
					childValue = cloneJSONValue(defaultValue)
					instance[name] = childValue
					if err := applyDefaultsAt(ctx, schemaPath, childValue, childSchemas, childPointer, depth+1, budget); err != nil {
						return err
					}
				}
			}
		}
	case []any:
		for index := range instance {
			indexSchemas := projectItemSchemas(schemas, index)
			if len(indexSchemas) == 0 {
				continue
			}
			if err := applyDefaultsAt(ctx, schemaPath, instance[index], indexSchemas, pointer+"/"+strconv.Itoa(index), depth+1, budget); err != nil {
				return err
			}
		}
	}
	return nil
}

func verifyProjectDefaultsPresent(ctx context.Context, schemaPath string, value any, schema *jsonschema.Schema) error {
	return verifyDefaultsAt(ctx, schemaPath, value, []*jsonschema.Schema{schema}, "", 0)
}

func verifyDefaultsAt(ctx context.Context, schemaPath string, value any, schemas []*jsonschema.Schema, pointer string, depth int) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if depth > projectDefaultDepthLimit {
		return projectError(schemaPath, pointer, "recursive_default_expansion", "default expansion exceeds the nesting limit")
	}
	schemas = expandProjectUnconditionalSchemas(schemas)
	instance, object := value.(map[string]any)
	if object {
		for _, name := range sortedAnyMapKeys(instance) {
			childSchemas := projectPropertySchemas(schemas, name)
			if len(childSchemas) == 0 {
				continue
			}
			childPointer := pointer + "/" + escapeJSONPointer(name)
			if err := verifyDefaultsAt(ctx, schemaPath, instance[name], childSchemas, childPointer, depth+1); err != nil {
				return err
			}
		}
		properties := make(map[string][]*jsonschema.Schema)
		for _, schema := range schemas {
			for name, child := range schema.Properties {
				properties[name] = append(properties[name], child)
			}
		}
		for _, name := range sortedSchemaPropertyNames(properties) {
			childSchemas := projectPropertySchemas(schemas, name)
			childPointer := pointer + "/" + escapeJSONPointer(name)
			_, exists := instance[name]
			_, hasDefault, err := projectDefaultValue(schemaPath, childPointer, childSchemas)
			if err != nil {
				return err
			}
			if !exists && hasDefault {
				return projectError(schemaPath, childPointer, "prepared_default_missing", "prepared parameters omit a static default")
			}
		}
	}
	if array, ok := value.([]any); ok {
		for index, item := range array {
			if err := verifyDefaultsAt(ctx, schemaPath, item, projectItemSchemas(schemas, index), pointer+"/"+strconv.Itoa(index), depth+1); err != nil {
				return err
			}
		}
	}
	return nil
}

func projectDefaultValue(schemaPath, pointer string, schemas []*jsonschema.Schema) (any, bool, error) {
	var selected any
	found := false
	for _, schema := range expandProjectUnconditionalSchemas(schemas) {
		if schema.Default == nil {
			continue
		}
		value := *schema.Default
		if found && !reflect.DeepEqual(selected, value) {
			return nil, false, projectSchemaDiagnostic(schemaPath, schema.Location, "conflicting_defaults", "unconditional schema branches define conflicting defaults")
		}
		if !found {
			selected = value
			found = true
		}
	}
	if !found {
		return nil, false, nil
	}
	for _, schema := range dedupeProjectSchemas(schemas) {
		if err := schema.Validate(selected); err != nil {
			return nil, false, projectSchemaDiagnostic(schemaPath, schema.Location, "invalid_default", "default value does not satisfy its schema")
		}
	}
	return selected, true, nil
}

func cloneJSONValue(value any) any {
	switch value := value.(type) {
	case map[string]any:
		cloned := make(map[string]any, len(value))
		for key, child := range value {
			cloned[key] = cloneJSONValue(child)
		}
		return cloned
	case []any:
		cloned := make([]any, len(value))
		for index, child := range value {
			cloned[index] = cloneJSONValue(child)
		}
		return cloned
	default:
		return value
	}
}

func expandProjectUnconditionalSchemas(schemas []*jsonschema.Schema) []*jsonschema.Schema {
	result := make([]*jsonschema.Schema, 0, len(schemas))
	seen := make(map[*jsonschema.Schema]bool)
	var visit func(*jsonschema.Schema, int)
	visit = func(schema *jsonschema.Schema, depth int) {
		if schema == nil || seen[schema] || depth > projectDefaultDepthLimit {
			return
		}
		seen[schema] = true
		result = append(result, schema)
		visit(schema.Ref, depth+1)
		for _, branch := range schema.AllOf {
			visit(branch, depth+1)
		}
	}
	for _, schema := range schemas {
		visit(schema, 0)
	}
	return result
}

func dedupeProjectSchemas(schemas []*jsonschema.Schema) []*jsonschema.Schema {
	result := make([]*jsonschema.Schema, 0, len(schemas))
	seen := make(map[*jsonschema.Schema]bool, len(schemas))
	for _, schema := range schemas {
		if schema != nil && !seen[schema] {
			seen[schema] = true
			result = append(result, schema)
		}
	}
	return result
}

func projectItemSchemas(schemas []*jsonschema.Schema, index int) []*jsonschema.Schema {
	result := []*jsonschema.Schema{}
	for _, schema := range expandProjectUnconditionalSchemas(schemas) {
		if index < len(schema.PrefixItems) {
			result = append(result, schema.PrefixItems[index])
		}
		if schema.Items2020 != nil && index >= len(schema.PrefixItems) {
			result = append(result, schema.Items2020)
		}
		switch item := schema.Items.(type) {
		case *jsonschema.Schema:
			result = append(result, item)
		case []*jsonschema.Schema:
			if index < len(item) {
				result = append(result, item[index])
			}
		}
	}
	return dedupeProjectSchemas(result)
}

func projectSchemaDiagnostic(defaultSchemaPath, location, code, message string) error {
	file, pointer := projectSchemaLocation(defaultSchemaPath, location)
	return projectError(file, pointer, code, message)
}

func projectSchemaLocation(defaultFile, location string) (string, string) {
	u, err := url.Parse(location)
	if err != nil {
		return defaultFile, ""
	}
	resource := *u
	resource.Fragment, resource.RawFragment = "", ""
	resourceName, err := projectSchemaResourceName(resource.String())
	if err != nil {
		resourceName = defaultFile
	}
	fragment := u.Fragment
	if fragment == "" {
		return resourceName, ""
	}
	if !strings.HasPrefix(fragment, "/") {
		return resourceName, ""
	}
	if _, err := decodeProjectJSONPointer(fragment); err != nil {
		return resourceName, ""
	}
	return resourceName, fragment
}

func decodeProjectJSONPointer(pointer string) ([]string, error) {
	if pointer == "" {
		return []string{}, nil
	}
	if !strings.HasPrefix(pointer, "/") {
		return nil, errors.New("JSON pointer must start with slash")
	}
	parts := strings.Split(pointer[1:], "/")
	for index, part := range parts {
		var decoded strings.Builder
		for i := 0; i < len(part); i++ {
			if part[i] != '~' {
				decoded.WriteByte(part[i])
				continue
			}
			if i+1 >= len(part) || (part[i+1] != '0' && part[i+1] != '1') {
				return nil, errors.New("invalid JSON pointer escape")
			}
			i++
			if part[i] == '0' {
				decoded.WriteByte('~')
			} else {
				decoded.WriteByte('/')
			}
		}
		parts[index] = decoded.String()
	}
	return parts, nil
}

func lookupProjectPointer(value any, pointer string) (any, bool) {
	tokens, err := decodeProjectJSONPointer(pointer)
	if err != nil {
		return nil, false
	}
	current := value
	for _, token := range tokens {
		switch node := current.(type) {
		case map[string]any:
			child, ok := node[token]
			if !ok {
				return nil, false
			}
			current = child
		case []any:
			index, err := strconv.Atoi(token)
			if err != nil || index < 0 || index >= len(node) || strconv.Itoa(index) != token {
				return nil, false
			}
			current = node[index]
		default:
			return nil, false
		}
	}
	return current, true
}

func sortedSchemaPropertyNames(values map[string][]*jsonschema.Schema) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	return keys
}

func setProjectPointer(value map[string]any, pointer string, replacement any) error {
	tokens, err := decodeProjectJSONPointer(pointer)
	if err != nil || len(tokens) == 0 {
		return errors.New("file reference pointer must identify a non-root field")
	}
	current := any(value)
	for _, token := range tokens[:len(tokens)-1] {
		switch node := current.(type) {
		case map[string]any:
			child, ok := node[token]
			if !ok {
				return fmt.Errorf("file reference parent %q is missing", token)
			}
			current = child
		case []any:
			index, err := strconv.Atoi(token)
			if err != nil || index < 0 || index >= len(node) || strconv.Itoa(index) != token {
				return errors.New("file reference pointer does not resolve to an existing array item")
			}
			current = node[index]
		default:
			return errors.New("file reference pointer does not resolve through an object or array")
		}
	}
	last := tokens[len(tokens)-1]
	switch node := current.(type) {
	case map[string]any:
		node[last] = replacement
		return nil
	case []any:
		index, err := strconv.Atoi(last)
		if err != nil || index < 0 || index >= len(node) || strconv.Itoa(index) != last {
			return errors.New("file reference pointer does not resolve to an existing array item")
		}
		node[index] = replacement
		return nil
	default:
		return errors.New("file reference pointer does not identify a field")
	}
}
