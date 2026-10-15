package documentcore

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"testing/fstest"
)

func TestPrepareParametersAppliesStaticDefaultsWithoutCoercion(t *testing.T) {
	schema := `{
	  "$schema":"https://json-schema.org/draft/2020-12/schema",
	  "$defs":{"shared":{"properties":{"fromRef":{"type":"string","default":"ref"}}}},
	  "type":"object",
	  "allOf":[{"$ref":"#/$defs/shared"}],
	  "properties":{
	    "name":{"type":"string","default":"default"},
	    "enabled":{"type":"boolean","default":true},
	    "count":{"type":"integer","default":3},
	    "nullable":{"type":["null","string"],"default":"fallback"},
	    "amount":{"type":"number"},
	    "nested":{"type":"object","properties":{"label":{"type":"string","default":"nested"}}},
	    "created":{"type":"object","default":{},"properties":{"code":{"type":"string","default":"new"}}},
	    "rows":{"type":"array","items":{"type":"object","properties":{"label":{"type":"string","default":"row"}}}},
	    "tuple":{"type":"array","prefixItems":[{"type":"string","default":"must-not-extend"}]}
	  }
	}`
	files := parameterSchemaFS(schema)
	prepared, err := PrepareParameters(context.Background(), files, "schemas/input.json", []byte(`{"amount":1.2300e+04,"count":0,"enabled":false,"nested":{},"nullable":null,"rows":[{}],"tuple":[]}`), nil)
	if err != nil {
		t.Fatalf("PrepareParameters() error = %v", err)
	}
	want := `{"amount":1.2300e+04,"count":0,"created":{"code":"new"},"enabled":false,"fromRef":"ref","name":"default","nested":{"label":"nested"},"nullable":null,"rows":[{"label":"row"}],"tuple":[]}`
	if string(prepared.JSON) != want {
		t.Fatalf("prepared JSON = %s, want %s", prepared.JSON, want)
	}
	if prepared.Inputs["leafrun-data"] != "/input.json" {
		t.Fatalf("leafrun-data input = %q", prepared.Inputs["leafrun-data"])
	}
	if len(prepared.Files) != 0 {
		t.Fatalf("unexpected prepared files: %+v", prepared.Files)
	}
	if err := VerifyPreparedInput(context.Background(), files, "schemas/input.json", prepared); err != nil {
		t.Fatalf("VerifyPreparedInput() rejected prepared bytes: %v", err)
	}
}

func TestPrepareParametersBindsOnlySchemaFileRefs(t *testing.T) {
	schema := `{"$schema":"https://json-schema.org/draft/2020-12/schema","type":"object","properties":{"avatar":{"type":"string","x-leafrun-file":true},"items":{"type":"array","items":{"type":"object","properties":{"image":{"type":"string","x-leafrun-file":true}}}}}}`
	files := parameterSchemaFS(schema)
	content := []byte("frozen svg bytes")
	digest := sha256.Sum256(content)
	ref := FileRef{Pointer: "/avatar", ResourceID: "resource-avatar", Filename: "Portrait.SVG", SizeBytes: int64(len(content)), SHA256: hex.EncodeToString(digest[:])}
	prepared, err := PrepareParameters(context.Background(), files, "schemas/input.json", []byte(`{}`), []FileRef{ref})
	if err != nil {
		t.Fatalf("PrepareParameters() error = %v", err)
	}
	wantPath := ".leafrun-input/" + ref.SHA256 + ".SVG"
	var values map[string]any
	if err := json.Unmarshal(prepared.JSON, &values); err != nil {
		t.Fatal(err)
	}
	if values["avatar"] != "/"+wantPath {
		t.Fatalf("file parameter = %#v, want %q", values["avatar"], "/"+wantPath)
	}
	if len(prepared.Files) != 1 || prepared.Files[0].Path != wantPath || prepared.Files[0].Pointer != ref.Pointer || prepared.Files[0].ResourceID != ref.ResourceID {
		t.Fatalf("prepared file descriptors = %+v", prepared.Files)
	}
	if err := VerifyPreparedInput(context.Background(), files, "schemas/input.json", prepared); err != nil {
		t.Fatalf("VerifyPreparedInput() rejected file binding: %v", err)
	}

	for _, test := range []struct {
		name string
		raw  []byte
		refs []FileRef
		code string
	}{
		{name: "unbound path", raw: []byte(`{"avatar":"/tmp/secret.svg"}`), code: "file_reference_required"},
		{name: "unknown pointer", raw: []byte(`{}`), refs: []FileRef{{Pointer: "/other", ResourceID: "resource-avatar", Filename: "Portrait.SVG", SizeBytes: int64(len(content)), SHA256: ref.SHA256}}, code: "file_reference_invalid"},
		{name: "unsafe filename", raw: []byte(`{}`), refs: []FileRef{{Pointer: "/avatar", ResourceID: "resource-avatar", Filename: "../secret.svg", SizeBytes: int64(len(content)), SHA256: ref.SHA256}}, code: "invalid_file_reference"},
		{name: "bad digest", raw: []byte(`{}`), refs: []FileRef{{Pointer: "/avatar", ResourceID: "resource-avatar", Filename: "x.svg", SizeBytes: int64(len(content)), SHA256: strings.Repeat("z", 64)}}, code: "file_reference_invalid"},
		{name: "bad pointer escape", raw: []byte(`{}`), refs: []FileRef{{Pointer: "/avatar~2", ResourceID: "resource-avatar", Filename: "x.svg", SizeBytes: int64(len(content)), SHA256: ref.SHA256}}, code: "invalid_file_reference"},
		{name: "duplicate pointer", raw: []byte(`{}`), refs: []FileRef{ref, ref}, code: "duplicate_file_reference"},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := PrepareParameters(context.Background(), files, "schemas/input.json", test.raw, test.refs)
			wantPointer := ""
			if test.name == "unbound path" {
				wantPointer = "/avatar"
			} else if test.name == "bad digest" || test.name == "unsafe filename" || test.name == "duplicate pointer" {
				wantPointer = "/avatar"
			} else if test.name == "bad pointer escape" {
				wantPointer = "/avatar~2"
			} else {
				wantPointer = "/other"
			}
			assertProjectDiagnostic(t, err, test.code, wantPointer)
		})
	}
}

func TestPrepareParametersAcceptsEscapedJSONPointerFieldNames(t *testing.T) {
	files := parameterSchemaFS(`{"$schema":"https://json-schema.org/draft/2020-12/schema","type":"object","properties":{"odd/name":{"type":"string","x-leafrun-file":true}}}`)
	content := []byte("escaped pointer content")
	digest := sha256.Sum256(content)
	ref := FileRef{Pointer: "/odd~1name", ResourceID: "resource-escaped", Filename: "file.svg", SizeBytes: int64(len(content)), SHA256: hex.EncodeToString(digest[:])}
	prepared, err := PrepareParameters(context.Background(), files, "schemas/input.json", []byte(`{}`), []FileRef{ref})
	if err != nil {
		t.Fatalf("PrepareParameters() rejected escaped JSON Pointer: %v", err)
	}
	if len(prepared.Files) != 1 || prepared.Files[0].Pointer != "/odd~1name" {
		t.Fatalf("prepared escaped file reference = %+v", prepared.Files)
	}
}

func TestValidateFileRefUsesSharedSafeFilenameRules(t *testing.T) {
	base := FileRef{Pointer: "/image", ResourceID: "resource-image", Filename: "chart final.SVG", SizeBytes: 0, SHA256: strings.Repeat("a", 64)}
	if err := validateFileRef("schema.json", base); err != nil {
		t.Fatalf("accepted shared safe filename: %v", err)
	}
	for _, filename := range []string{
		"", ".", "..", strings.Repeat("x", 256), " chart.svg", "chart.svg ", "C:chart.svg",
		"dir/chart.svg", `dir\chart.svg`, "chart\u007f.svg", "chart.", "chart." + strings.Repeat("x", 17), "chart.é",
	} {
		ref := base
		ref.Filename = filename
		if err := validateFileRef("schema.json", ref); err == nil {
			t.Errorf("accepted unsafe filename %q", filename)
		}
	}
}

func TestReadProjectRejectsInvalidAndAmbiguousDefaults(t *testing.T) {
	for _, test := range []struct {
		name   string
		schema string
		code   string
	}{
		{
			name:   "invalid literal",
			schema: `{"$schema":"https://json-schema.org/draft/2020-12/schema","type":"object","properties":{"count":{"type":"integer","default":"many"}}}`,
			code:   "invalid_default",
		},
		{
			name:   "conflicting allOf defaults",
			schema: `{"$schema":"https://json-schema.org/draft/2020-12/schema","type":"object","allOf":[{"properties":{"value":{"default":1}}},{"properties":{"value":{"default":2}}}]}`,
			code:   "conflicting_defaults",
		},
		{
			name:   "union default",
			schema: `{"$schema":"https://json-schema.org/draft/2020-12/schema","type":"object","properties":{"value":{"oneOf":[{"type":"string","default":"a"},{"type":"integer"}]}}}`,
			code:   "ambiguous_default",
		},
		{
			name:   "conditional default",
			schema: `{"$schema":"https://json-schema.org/draft/2020-12/schema","type":"object","properties":{"value":{"if":{"type":"object"},"then":{"properties":{"label":{"default":"conditional"}}}}}}`,
			code:   "ambiguous_default",
		},
		{
			name:   "file default",
			schema: `{"$schema":"https://json-schema.org/draft/2020-12/schema","type":"object","properties":{"image":{"type":"string","x-leafrun-file":true,"default":"photo.svg"}}}`,
			code:   "file_default_forbidden",
		},
		{
			name:   "invalid file annotation",
			schema: `{"$schema":"https://json-schema.org/draft/2020-12/schema","type":"object","properties":{"image":{"type":"integer","x-leafrun-file":true}}}`,
			code:   "invalid_file_schema",
		},
		{
			name:   "recursive default expansion",
			schema: `{"$schema":"https://json-schema.org/draft/2020-12/schema","$defs":{"node":{"type":"object","default":{},"properties":{"next":{"$ref":"#/$defs/node"}}}},"type":"object","properties":{"tree":{"$ref":"#/$defs/node"}}}`,
			code:   "recursive_default_expansion",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			files := mutableProjectFixtureFS(t)
			(*files)["schemas/sales.schema.json"] = &fstest.MapFile{Data: []byte(test.schema)}
			_, err := ReadProject(context.Background(), files)
			if err == nil {
				t.Fatal("ReadProject() accepted invalid default annotations")
			}
			var diagnostic *ProjectValidationError
			if !errors.As(err, &diagnostic) {
				t.Fatalf("error does not carry a project diagnostic: %v", err)
			}
			if diagnostic.File != "schemas/sales.schema.json" || diagnostic.Code != test.code {
				t.Fatalf("diagnostic = %+v, want file schemas/sales.schema.json and code %q", diagnostic, test.code)
			}
		})
	}
}

func TestPrepareParametersAppliesDefaultsToPatternPropertiesAndRespectsTupleItems(t *testing.T) {
	files := parameterSchemaFS(`{"$schema":"https://json-schema.org/draft/2020-12/schema","type":"object","properties":{"metadata":{"type":"object","patternProperties":{"^x-":{"type":"object","properties":{"label":{"type":"string","default":"pattern"}}}}},"tuple":{"type":"array","prefixItems":[{"type":"object"}],"items":{"type":"object","properties":{"label":{"type":"string","default":"after-prefix"}}}}}}`)
	prepared, err := PrepareParameters(context.Background(), files, "schemas/input.json", []byte(`{"metadata":{"x-extra":{}},"tuple":[{}]}`), nil)
	if err != nil {
		t.Fatalf("PrepareParameters() error = %v", err)
	}
	want := `{"metadata":{"x-extra":{"label":"pattern"}},"tuple":[{}]}`
	if string(prepared.JSON) != want {
		t.Fatalf("prepared JSON = %s, want %s", prepared.JSON, want)
	}
	if err := VerifyPreparedInput(context.Background(), files, "schemas/input.json", prepared); err != nil {
		t.Fatalf("VerifyPreparedInput() rejected prepared pattern properties: %v", err)
	}
}

func TestPrepareParametersRejectsRecursiveDefaultExpansion(t *testing.T) {
	files := parameterSchemaFS(`{"$schema":"https://json-schema.org/draft/2020-12/schema","$defs":{"node":{"type":"object","default":{},"properties":{"next":{"$ref":"#/$defs/node"}}}},"type":"object","properties":{"tree":{"$ref":"#/$defs/node"}}}`)
	_, err := PrepareParameters(context.Background(), files, "schemas/input.json", []byte(`{}`), nil)
	assertProjectDiagnostic(t, err, "recursive_default_expansion", "/$defs/node")
}

func TestPrepareParametersEnforcesRequestPreparedAndReferenceBounds(t *testing.T) {
	files := parameterSchemaFS(`{"$schema":"https://json-schema.org/draft/2020-12/schema","type":"object"}`)
	request := []byte(`{"padding":"` + strings.Repeat("x", projectRequestJSONLimit) + `"}`)
	_, err := PrepareParameters(context.Background(), files, "schemas/input.json", request, nil)
	assertProjectDiagnostic(t, err, "parameters_too_large", "")

	prepared := PreparedInput{
		JSON:   json.RawMessage(`{"padding":"` + strings.Repeat("x", projectPreparedJSONLimit) + `"}`),
		Files:  []PreparedFile{},
		Inputs: map[string]string{"leafrun-data": "/input.json"},
	}
	if err := VerifyPreparedInput(context.Background(), files, "schemas/input.json", prepared); err == nil {
		t.Fatal("VerifyPreparedInput() accepted prepared bytes over 1 MiB")
	} else {
		assertProjectDiagnostic(t, err, "prepared_parameters_too_large", "")
	}
	_, err = PrepareParameters(context.Background(), files, "schemas/input.json", []byte(`{}`), make([]FileRef, projectInputFileCountLimit+1))
	assertProjectDiagnostic(t, err, "file_reference_limit", "")
}

func TestPrepareParametersValidatesFileAnnotationUnderLocalID(t *testing.T) {
	files := parameterSchemaFS(`{"$schema":"https://json-schema.org/draft/2020-12/schema","$id":"local-input","type":"object","properties":{"image":{"type":"integer","x-leafrun-file":true}}}`)
	_, err := PrepareParameters(context.Background(), files, "schemas/input.json", []byte(`{}`), nil)
	assertProjectDiagnostic(t, err, "invalid_file_schema", "/properties/image")
}

func TestPrepareParametersCountsBytesPerGeneratedFilePath(t *testing.T) {
	files := parameterSchemaFS(`{"$schema":"https://json-schema.org/draft/2020-12/schema","type":"object","properties":{"first":{"type":"string","x-leafrun-file":true},"second":{"type":"string","x-leafrun-file":true}}}`)
	refs := []FileRef{
		{Pointer: "/first", ResourceID: "resource-first", Filename: "one.svg", SizeBytes: 13 << 20, SHA256: strings.Repeat("a", 64)},
		{Pointer: "/second", ResourceID: "resource-second", Filename: "two.png", SizeBytes: 13 << 20, SHA256: strings.Repeat("a", 64)},
	}
	_, err := PrepareParameters(context.Background(), files, "schemas/input.json", []byte(`{}`), refs)
	assertProjectDiagnostic(t, err, "input_file_bytes_limit", "")
}

func TestPrepareParametersReturnsOwnedCopies(t *testing.T) {
	files := parameterSchemaFS(`{"$schema":"https://json-schema.org/draft/2020-12/schema","type":"object","properties":{"label":{"type":"string","default":"default"}}}`)
	raw := []byte(`{}`)
	prepared, err := PrepareParameters(context.Background(), files, "schemas/input.json", raw, nil)
	if err != nil {
		t.Fatal(err)
	}
	prepared.JSON[0] = '['
	prepared.Inputs["leafrun-data"] = "changed"
	prepared.Inputs["new"] = "value"
	if string(raw) != `{}` {
		t.Fatalf("PrepareParameters() modified input bytes: %q", raw)
	}
	second, err := PrepareParameters(context.Background(), files, "schemas/input.json", raw, nil)
	if err != nil {
		t.Fatal(err)
	}
	if string(second.JSON) != `{"label":"default"}` || second.Inputs["leafrun-data"] != "/input.json" || len(second.Inputs) != 1 {
		t.Fatalf("second preparation shared caller mutations: %+v", second)
	}
}

func TestVerifyPreparedInputDoesNotApplyMissingDefaults(t *testing.T) {
	files := parameterSchemaFS(`{"$schema":"https://json-schema.org/draft/2020-12/schema","type":"object","properties":{"label":{"type":"string","default":"added-once"}}}`)
	prepared := PreparedInput{JSON: json.RawMessage(`{}`), Inputs: map[string]string{"leafrun-data": "/input.json"}, Files: []PreparedFile{}}
	if err := VerifyPreparedInput(context.Background(), files, "schemas/input.json", prepared); err == nil {
		t.Fatal("VerifyPreparedInput() accepted bytes that still require default preparation")
	}
}

func parameterSchemaFS(schema string) fstest.MapFS {
	return fstest.MapFS{"schemas/input.json": &fstest.MapFile{Data: []byte(schema)}}
}
