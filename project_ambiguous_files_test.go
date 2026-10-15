package documentcore

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"testing/fstest"
)

func TestReadProjectRejectsAmbiguousFileFieldsAndDefaults(t *testing.T) {
	for _, test := range []struct{ name, schema, code string }{
		{"anyOf", `{"type":"object","properties":{"avatar":{"anyOf":[{"type":"string","x-leafrun-file":true}]}}}`, "ambiguous_file_schema"},
		{"oneOf", `{"type":"object","properties":{"avatar":{"oneOf":[{"type":"string","x-leafrun-file":true}]}}}`, "ambiguous_file_schema"},
		{"conditional", `{"type":"object","if":{},"then":{"properties":{"avatar":{"type":"string","x-leafrun-file":true}}}}`, "ambiguous_file_schema"},
		{"contains", `{"type":"object","properties":{"images":{"type":"array","contains":{"type":"string","x-leafrun-file":true}}}}`, "ambiguous_file_schema"},
		{"unevaluatedProperties", `{"type":"object","unevaluatedProperties":{"type":"string","x-leafrun-file":true}}`, "ambiguous_file_schema"},
		{"unevaluatedItems", `{"type":"object","properties":{"images":{"type":"array","unevaluatedItems":{"type":"string","x-leafrun-file":true}}}}`, "ambiguous_file_schema"},
		{"dependentSchemas", `{"type":"object","dependentSchemas":{"enabled":{"properties":{"avatar":{"type":"string","x-leafrun-file":true}}}}}`, "ambiguous_file_schema"},
		{"unionRef", `{"$defs":{"file":{"type":"string","x-leafrun-file":true}},"type":"object","properties":{"avatar":{"anyOf":[{"$ref":"#/$defs/file"}]}}}`, "ambiguous_file_schema"},
		{"containsDefault", `{"type":"object","properties":{"rows":{"type":"array","contains":{"type":"string","default":1}}}}`, "ambiguous_default"},
		{"unevaluatedDefault", `{"type":"object","unevaluatedProperties":{"type":"string","default":"x"}}`, "ambiguous_default"},
	} {
		t.Run(test.name, func(t *testing.T) {
			files := mutableProjectFixtureFS(t)
			(*files)["schemas/sales.schema.json"] = &fstest.MapFile{Data: []byte(test.schema)}
			_, err := ReadProject(context.Background(), files)
			var diagnostic *ProjectValidationError
			if !errors.As(err, &diagnostic) || diagnostic.Code != test.code {
				t.Fatalf("technical schema accepted or wrong diagnosis: %v", err)
			}
			if !strings.Contains(diagnostic.File, "schema") || diagnostic.Pointer == "" {
				t.Fatalf("diagnostic lost location: %+v", diagnostic)
			}
		})
	}
}

func TestAmbiguousFileSchemaCannotAcceptPathsOrBoundRefs(t *testing.T) {
	schema := `{"type":"object","properties":{"avatar":{"anyOf":[{"type":"string","x-leafrun-file":true}]}}}`
	files := parameterSchemaFS(schema)
	for _, input := range []struct {
		raw  string
		refs []FileRef
	}{
		{raw: `{"avatar":"/some/path"}`},
		{raw: `{}`, refs: []FileRef{{Pointer: "/avatar", ResourceID: "resource", Filename: "logo.svg", SHA256: strings.Repeat("a", 64), SizeBytes: 1}}},
	} {
		if _, err := PrepareParameters(context.Background(), files, "schemas/input.json", []byte(input.raw), input.refs); err == nil {
			t.Fatal("ambiguous schema accepted for preparation")
		}
		prepared := PreparedInput{JSON: json.RawMessage(input.raw), Files: []PreparedFile{}, Inputs: map[string]string{"leafrun-data": "/input.json"}}
		if err := VerifyPreparedInput(context.Background(), files, "schemas/input.json", prepared); err == nil {
			t.Fatal("ambiguous schema accepted by child verification")
		}
	}
}

func TestDynamicSchemaCannotBypassFileBinding(t *testing.T) {
	root := `{"$dynamicAnchor":"node","type":"object","properties":{"avatar":{"type":"string","x-leafrun-file":true},"child":{"$ref":"generic.json"}}}`
	generic := `{"$dynamicAnchor":"node","type":"object","properties":{"child":{"$dynamicRef":"#node"}}}`
	files := parameterSchemaFS(root)
	files["schemas/generic.json"] = &fstest.MapFile{Data: []byte(generic)}
	if _, err := PrepareParameters(context.Background(), files, "schemas/input.json", []byte(`{"child":{"child":{"avatar":"/some/path"}}}`), nil); err == nil {
		t.Fatal("dynamic override accepted unbound file path")
	}
	project := mutableProjectFixtureFS(t)
	(*project)["schemas/sales.schema.json"] = &fstest.MapFile{Data: []byte(root)}
	(*project)["schemas/generic.json"] = &fstest.MapFile{Data: []byte(generic)}
	_, err := ReadProject(context.Background(), project)
	var diagnostic *ProjectValidationError
	if !errors.As(err, &diagnostic) || diagnostic.Code != "unsupported_dynamic_annotations" {
		t.Fatalf("dynamic annotation accepted at publication: %v", err)
	}
}

func TestDynamicValidationWithoutReachableAnnotationsRemainsSupported(t *testing.T) {
	root := `{"$dynamicAnchor":"node","type":"object","$defs":{"unused":{"type":"string","x-leafrun-file":true}},"properties":{"child":{"$ref":"generic.json"}}}`
	generic := `{"$dynamicAnchor":"node","type":"object","properties":{"child":{"$dynamicRef":"#node"}}}`
	project := mutableProjectFixtureFS(t)
	(*project)["schemas/sales.schema.json"] = &fstest.MapFile{Data: []byte(root)}
	(*project)["schemas/generic.json"] = &fstest.MapFile{Data: []byte(generic)}
	if _, err := ReadProject(context.Background(), project); err != nil {
		t.Fatalf("pure dynamic validation/unreferenced definition rejected: %v", err)
	}
}
