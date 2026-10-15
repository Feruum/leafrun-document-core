package documentcore

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/url"
	"path"
	"strings"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

const projectSchemaResourceLimit int64 = 4 << 20

const projectDraft2020Schema = "https://json-schema.org/draft/2020-12/schema"

type projectSchemaLoader struct {
	ctx       context.Context
	files     fs.FS
	resources map[string]any
	total     int64
}

type projectSchemaValidator struct {
	ctx       context.Context
	loader    *projectSchemaLoader
	compiler  *jsonschema.Compiler
	validated map[string]struct{}
}

type projectSchemaSpaceError struct {
	Pointer string
	Code    string
	Message string
}

func (e *projectSchemaSpaceError) Error() string { return e.Message }

func validateProjectSchema(ctx context.Context, files fs.FS, schemaPath string) error {
	return newProjectSchemaValidator(ctx, files).Validate(schemaPath)
}

func newProjectSchemaValidator(ctx context.Context, files fs.FS) *projectSchemaValidator {
	loader := &projectSchemaLoader{ctx: ctx, files: files, resources: make(map[string]any)}
	compiler := jsonschema.NewCompiler()
	compiler.DefaultDraft(jsonschema.Draft2020)
	compiler.UseLoader(loader)
	return &projectSchemaValidator{ctx: ctx, loader: loader, compiler: compiler, validated: make(map[string]struct{})}
}

func (v *projectSchemaValidator) Validate(schemaPath string) error {
	if err := v.ctx.Err(); err != nil {
		return err
	}
	if _, ok := v.validated[schemaPath]; ok {
		return nil
	}
	rootURL := projectSchemaURL(schemaPath)
	if _, err := v.loader.Load(rootURL); err != nil {
		return projectSchemaLoadError(schemaPath, err)
	}
	compiled, err := v.compiler.Compile(rootURL)
	if err != nil {
		var spaceErr *projectSchemaSpaceError
		if errors.As(err, &spaceErr) {
			return projectError(schemaPath, spaceErr.Pointer, spaceErr.Code, spaceErr.Message)
		}
		var diagnostic *ProjectValidationError
		if errors.As(err, &diagnostic) {
			return diagnostic
		}
		return projectError(schemaPath, "", "invalid_schema", err.Error())
	}
	if err := validateProjectSchemaAnnotations(schemaPath, v.loader, compiled); err != nil {
		return err
	}
	v.validated[schemaPath] = struct{}{}
	return v.ctx.Err()
}

// Compile validates the complete snapshot-local schema graph, including
// supported default/file annotations, and returns its compiled root.
func (v *projectSchemaValidator) Compile(schemaPath string) (*jsonschema.Schema, error) {
	if err := v.Validate(schemaPath); err != nil {
		return nil, err
	}
	return v.compiler.Compile(projectSchemaURL(schemaPath))
}

func (l *projectSchemaLoader) Load(location string) (any, error) {
	if err := l.ctx.Err(); err != nil {
		return nil, err
	}
	name, err := projectSchemaResourceName(location)
	if err != nil {
		return nil, err
	}
	if cached, ok := l.resources[name]; ok {
		return cached, nil
	}
	file, err := l.files.Open(name)
	if err != nil {
		code := "resource_unavailable"
		if errors.Is(err, fs.ErrNotExist) {
			code = "missing_resource"
		}
		return nil, projectError(name, "", code, "schema resource cannot be opened")
	}
	info, statErr := file.Stat()
	if statErr != nil {
		return nil, errors.Join(projectError(name, "", "resource_unavailable", "schema resource metadata cannot be read"), file.Close(), statErr)
	}
	if !info.Mode().IsRegular() || info.Size() < 0 {
		return nil, errors.Join(projectError(name, "", "invalid_resource", "schema resource must be a regular file"), file.Close())
	}
	if info.Size() > projectSchemaResourceLimit-l.total {
		return nil, errors.Join(projectError(name, "", "schema_resource_limit", "total schema resources exceed 4 MiB"), file.Close())
	}
	data, readErr := io.ReadAll(io.LimitReader(file, projectSchemaResourceLimit-l.total+1))
	closeErr := file.Close()
	if readErr != nil || closeErr != nil {
		return nil, errors.Join(projectError(name, "", "resource_unavailable", "schema resource bytes cannot be read"), readErr, closeErr)
	}
	if int64(len(data)) > projectSchemaResourceLimit-l.total {
		return nil, projectError(name, "", "schema_resource_limit", "total schema resources exceed 4 MiB")
	}
	value, err := decodeProjectJSON(name, data)
	if err != nil {
		return nil, err
	}
	base, err := url.Parse(projectSchemaURL(name))
	if err != nil {
		return nil, err
	}
	if err := checkProjectSchemaSpace(value, base, ""); err != nil {
		var spaceErr *projectSchemaSpaceError
		if errors.As(err, &spaceErr) {
			return nil, projectError(name, spaceErr.Pointer, spaceErr.Code, spaceErr.Message)
		}
		return nil, projectError(name, "", "invalid_schema", err.Error())
	}
	l.total += int64(len(data))
	l.resources[name] = value
	return value, l.ctx.Err()
}

func projectSchemaURL(name string) string {
	return (&url.URL{Scheme: "leafrun", Host: "snapshot", Path: "/" + name}).String()
}

func closedProjectSchemaURL(location string) (*url.URL, error) {
	u, err := url.Parse(location)
	if err != nil {
		return nil, err
	}
	if u.Scheme != "leafrun" || u.Host != "snapshot" || u.User != nil || u.Opaque != "" || u.RawQuery != "" || u.Fragment != "" || !strings.HasPrefix(u.Path, "/") {
		return nil, &projectSchemaSpaceError{Code: "schema_reference_escape", Message: "schema URI leaves the immutable snapshot namespace"}
	}
	return u, nil
}

func checkProjectSchemaSpace(value any, base *url.URL, pointer string) error {
	object, ok := value.(map[string]any)
	if !ok {
		if _, boolean := value.(bool); boolean {
			return nil
		}
		return &projectSchemaSpaceError{Pointer: pointer, Code: "invalid_schema", Message: "schema must be an object or boolean"}
	}
	if raw, exists := object["$schema"]; exists {
		schema, ok := raw.(string)
		if !ok || schema != projectDraft2020Schema {
			return &projectSchemaSpaceError{Pointer: pointer + "/$schema", Code: "unsupported_schema_dialect", Message: "only standard Draft 2020-12 is supported"}
		}
	}
	if raw, exists := object["$id"]; exists {
		id, ok := raw.(string)
		if !ok {
			return &projectSchemaSpaceError{Pointer: pointer + "/$id", Code: "invalid_schema", Message: "schema $id must be a URI string"}
		}
		ref, err := url.Parse(id)
		if err != nil {
			return &projectSchemaSpaceError{Pointer: pointer + "/$id", Code: "invalid_schema", Message: "schema $id is invalid"}
		}
		resolved := base.ResolveReference(ref)
		if resolved.Fragment != "" || resolved.RawQuery != "" {
			return &projectSchemaSpaceError{Pointer: pointer + "/$id", Code: "schema_reference_escape", Message: "schema $id cannot contain a fragment or query"}
		}
		if _, err := projectSchemaResourceName(resolved.String()); err != nil {
			return &projectSchemaSpaceError{Pointer: pointer + "/$id", Code: "schema_reference_escape", Message: "schema $id leaves the immutable snapshot namespace"}
		}
		base = resolved
	}
	for _, key := range []string{"$ref", "$dynamicRef", "$recursiveRef"} {
		raw, exists := object[key]
		if !exists {
			continue
		}
		refText, ok := raw.(string)
		if !ok {
			return &projectSchemaSpaceError{Pointer: pointer + "/" + key, Code: "invalid_schema", Message: "schema reference must be a URI string"}
		}
		ref, err := url.Parse(refText)
		if err != nil {
			return &projectSchemaSpaceError{Pointer: pointer + "/" + key, Code: "invalid_schema", Message: "schema reference is invalid"}
		}
		resolved := base.ResolveReference(ref)
		if err := validateProjectSchemaReferenceURL(resolved); err != nil {
			return &projectSchemaSpaceError{Pointer: pointer + "/" + key, Code: "schema_reference_escape", Message: "schema reference leaves the immutable snapshot namespace"}
		}
	}
	for _, keyword := range []string{"additionalProperties", "unevaluatedProperties", "propertyNames", "contains", "not", "if", "then", "else", "items", "unevaluatedItems", "contentSchema"} {
		child, exists := object[keyword]
		if !exists {
			continue
		}
		if err := checkProjectSchemaSpace(child, base, pointer+"/"+keyword); err != nil {
			return err
		}
	}
	for _, keyword := range []string{"$defs", "definitions", "properties", "patternProperties", "dependentSchemas"} {
		children, ok := object[keyword].(map[string]any)
		if !ok {
			continue
		}
		for _, name := range sortedAnyMapKeys(children) {
			if err := checkProjectSchemaSpace(children[name], base, pointer+"/"+keyword+"/"+escapeJSONPointer(name)); err != nil {
				return err
			}
		}
	}
	for _, keyword := range []string{"allOf", "anyOf", "oneOf", "prefixItems"} {
		children, ok := object[keyword].([]any)
		if !ok {
			continue
		}
		for index, child := range children {
			if err := checkProjectSchemaSpace(child, base, fmt.Sprintf("%s/%s/%d", pointer, keyword, index)); err != nil {
				return err
			}
		}
	}
	return nil
}

func projectSchemaLoadError(root string, err error) error {
	var diagnostic *ProjectValidationError
	if errors.As(err, &diagnostic) {
		return diagnostic
	}
	return projectError(root, "", "invalid_schema", err.Error())
}

func projectSchemaResourceName(location string) (string, error) {
	u, err := closedProjectSchemaURL(location)
	if err != nil {
		return "", err
	}
	name := strings.TrimPrefix(u.Path, "/")
	if !validProjectLogicalPath(name) || path.Clean(name) != name {
		return "", &projectSchemaSpaceError{Code: "schema_reference_escape", Message: "schema reference is not a logical snapshot file"}
	}
	return name, nil
}

func validateProjectSchemaReferenceURL(u *url.URL) error {
	if u.Scheme != "leafrun" || u.Host != "snapshot" || u.User != nil || u.Opaque != "" || u.RawQuery != "" || !strings.HasPrefix(u.Path, "/") {
		return &projectSchemaSpaceError{Code: "schema_reference_escape", Message: "schema URI leaves the immutable snapshot namespace"}
	}
	name := strings.TrimPrefix(u.Path, "/")
	if !validProjectLogicalPath(name) || path.Clean(name) != name {
		return &projectSchemaSpaceError{Code: "schema_reference_escape", Message: "schema reference is not a logical snapshot file"}
	}
	return nil
}
