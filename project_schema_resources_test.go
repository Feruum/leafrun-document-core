package documentcore

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"testing"
	"testing/fstest"
)

func TestSchemaResourcesReturnsOnlyOwnedReachableClosure(t *testing.T) {
	files := fstest.MapFS{
		"schemas/form.schema.json":   &fstest.MapFile{Data: []byte(`{"$schema":"https://json-schema.org/draft/2020-12/schema","type":"object","properties":{"price":{"$ref":"common.schema.json#/$defs/money"}}}`)},
		"schemas/common.schema.json": &fstest.MapFile{Data: []byte(`{"$schema":"https://json-schema.org/draft/2020-12/schema","$defs":{"money":{"type":"number","default":1.2300e+04}}}`)},
		"data.json":                  &fstest.MapFile{Data: []byte(`{"private":"unrelated project data"}`)},
	}
	resources, err := SchemaResources(context.Background(), files, "schemas/form.schema.json")
	if err != nil {
		t.Fatalf("SchemaResources() error = %v", err)
	}
	if len(resources) != 2 {
		t.Fatalf("schema closure contains %d resources: %v", len(resources), resources)
	}
	if _, exists := resources["data.json"]; exists {
		t.Fatal("schema closure exposed an unrelated project resource")
	}
	if got := resources["schemas/common.schema.json"]; !bytes.Contains(got, []byte("1.2300e+04")) {
		t.Fatalf("schema resource changed numeric lexeme: %s", got)
	}
	resources["schemas/common.schema.json"][0] = '['
	delete(resources, "schemas/form.schema.json")
	second, err := SchemaResources(context.Background(), files, "schemas/form.schema.json")
	if err != nil {
		t.Fatalf("second SchemaResources() error = %v", err)
	}
	if !json.Valid(second["schemas/common.schema.json"]) || !bytes.Contains(second["schemas/common.schema.json"], []byte("1.2300e+04")) {
		t.Fatalf("returned schema resources were not independently owned: %s", second["schemas/common.schema.json"])
	}
}

func TestSchemaResourcesUsesTechnicalDefaultValidation(t *testing.T) {
	files := fstest.MapFS{"schema.json": &fstest.MapFile{Data: []byte(`{"$schema":"https://json-schema.org/draft/2020-12/schema","type":"object","properties":{"count":{"type":"integer","default":"many"}}}`)}}
	_, err := SchemaResources(context.Background(), files, "schema.json")
	var diagnostic *ProjectValidationError
	if !errors.As(err, &diagnostic) || diagnostic.Code != "invalid_default" {
		t.Fatalf("SchemaResources() error = %v, want invalid_default diagnostic", err)
	}
}
