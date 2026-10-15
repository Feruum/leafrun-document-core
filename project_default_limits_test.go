package documentcore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	jsonvalue "github.com/leafrun/leafrun-contracts/jsonvalue/v1"
)

func TestExpandedDefaultsStopAtPreparedByteLimit(t *testing.T) {
	value, _ := json.Marshal(strings.Repeat("x", 8192))
	schema := `{"type":"object","properties":{"rows":{"type":"array","items":{"type":"object","properties":{"value":{"type":"string","default":` + string(value) + `}}}}}}`
	raw := []byte(`{"rows":[` + strings.Repeat(`{},`, 128) + `{}` + `]}`)
	_, err := PrepareParameters(context.Background(), parameterSchemaFS(schema), "schemas/input.json", raw, nil)
	var diagnostic *ProjectValidationError
	if !errors.As(err, &diagnostic) || diagnostic.Code != "prepared_parameters_too_large" {
		t.Fatalf("expanded output must have its own bounded diagnosis: %v", err)
	}
	// Observe the internal expansion after failure: the tree must never have
	// grown beyond the limit, rather than merely rejecting its final encoding.
	parameters, err := jsonvalue.Decode(raw, projectRequestJSONLimit)
	if err != nil {
		t.Fatal(err)
	}
	compiled, err := newProjectSchemaValidator(context.Background(), parameterSchemaFS(schema)).Compile("schemas/input.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := applyProjectDefaults(context.Background(), "schemas/input.json", parameters, compiled); err == nil {
		t.Fatal("oversize expansion accepted")
	}
	encoded, err := json.Marshal(parameters)
	if err != nil || len(encoded) > projectPreparedJSONLimit {
		t.Fatalf("expansion exceeded its allocation budget before failing: %d bytes, %v", len(encoded), err)
	}
}

func TestPreparedBudgetAccountsForJSONEscapes(t *testing.T) {
	value := map[string]any{"<key>": []any{"\"\\\n\u0001<>&\u2028\u2029é🙂", json.Number("1.2300e+04"), nil, false, true}}
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if size, ok := projectJSONSize(value, len(encoded)); !ok || size != len(encoded) {
		t.Fatalf("exact JSON encoding budget: %d, %v; want %d", size, ok, len(encoded))
	}
	if _, ok := projectJSONSize(value, len(encoded)-1); ok {
		t.Fatal("escaped JSON exceeded byte budget")
	}
}

func TestTechnicalValidationRejectsFileValuesInsideAncestorDefaults(t *testing.T) {
	for _, schema := range []string{
		`{"type":"object","properties":{"options":{"type":"object","default":{"image":"/secret.svg"},"properties":{"image":{"type":"string","x-leafrun-file":true}}}}}`,
		`{"type":"object","properties":{"images":{"type":"array","default":["/secret.svg"],"items":{"type":"string","x-leafrun-file":true}}}}`,
		`{"$defs":{"file":{"type":"string","x-leafrun-file":true}},"type":"object","properties":{"options":{"allOf":[{"default":{"image":"/secret.svg"}},{"type":"object","properties":{"image":{"$ref":"#/$defs/file"}}}]}}}`,
	} {
		files := mutableProjectFixtureFS(t)
		(*files)["schemas/sales.schema.json"] = &fstest.MapFile{Data: []byte(schema)}
		_, err := ReadProject(context.Background(), files)
		var diagnostic *ProjectValidationError
		if !errors.As(err, &diagnostic) || diagnostic.Code != "file_default_forbidden" {
			t.Errorf("ancestor default contains a file path: %v", err)
		}
		if _, err := SchemaResources(context.Background(), files, "schemas/sales.schema.json"); err == nil {
			t.Error("form schema accepted a forbidden default path")
		}
	}
}

func TestTechnicalValidationSharedSchemaDAGIsBounded(t *testing.T) {
	var schema strings.Builder
	schema.WriteString(`{"type":"object","$defs":{`)
	for level := 0; level < 23; level++ {
		if level > 0 {
			schema.WriteByte(',')
		}
		fmt.Fprintf(&schema, `"n%d":{"type":"object","properties":{"left":{"$ref":"#/$defs/n%d"},"right":{"$ref":"#/$defs/n%d"}}}`, level, level+1, level+1)
	}
	schema.WriteString(`,"n23":{"type":"object"}},"properties":{"tree":{"$ref":"#/$defs/n0"}}}`)
	files := mutableProjectFixtureFS(t)
	(*files)["schemas/sales.schema.json"] = &fstest.MapFile{Data: []byte(schema.String())}
	started := time.Now()
	if _, err := ReadProject(context.Background(), files); err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(started); elapsed > 5*time.Second {
		t.Fatalf("shared schema DAG required exponential traversal: %s", elapsed)
	}
}
