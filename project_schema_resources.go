package documentcore

import (
	"bytes"
	"context"
	"encoding/json"
	"io/fs"
)

// SchemaResources returns the validated schema closure rooted at schemaPath.
// The map contains only logical schema resource paths reachable through
// snapshot-local references. Returned messages are independent copies and
// retain the exact validated bytes, including numeric lexemes and string escapes.
func SchemaResources(ctx context.Context, files fs.FS, schemaPath string) (map[string]json.RawMessage, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if files == nil {
		return nil, projectError(schemaPath, "", "filesystem_required", "project filesystem is required")
	}
	if !validProjectLogicalPath(schemaPath) {
		return nil, projectError(schemaPath, "", "invalid_logical_path", "schema path is not a logical project path")
	}
	validator := newProjectSchemaValidator(ctx, files)
	if _, err := validator.Compile(schemaPath); err != nil {
		return nil, err
	}
	resources := make(map[string]json.RawMessage, len(validator.loader.resources))
	for name, value := range validator.loader.resources {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if _, ok := value.(map[string]any); !ok {
			if _, boolean := value.(bool); !boolean {
				return nil, projectError(name, "", "invalid_schema", "schema resource must be an object or boolean")
			}
		}
		resources[name] = json.RawMessage(bytes.Clone(validator.loader.rawResources[name]))
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if _, exists := resources[schemaPath]; !exists {
		return nil, projectError(schemaPath, "", "missing_resource", "root schema resource was not loaded")
	}
	return resources, nil
}
