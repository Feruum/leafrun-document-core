package documentcore

import (
	"context"
	"strings"
	"testing"
)

func TestMixedObjectArrayFilePointerUsesActualInstanceShape(t *testing.T) {
	ref := FileRef{Pointer: "/field/0", ResourceID: "resource-file", Filename: "logo.svg", SHA256: strings.Repeat("a", 64), SizeBytes: 1}
	objectFile := parameterSchemaFS(`{"type":"object","properties":{"field":{"type":["object","array"],"properties":{"0":{"type":"string","x-leafrun-file":true}},"items":{"type":"string"}}}}`)
	if _, err := PrepareParameters(context.Background(), objectFile, "schemas/input.json", []byte(`{"field":["ordinary-value"]}`), []FileRef{ref}); err == nil {
		t.Fatal("fileRef selected an object schema for an actual array item")
	}
	arrayFile := parameterSchemaFS(`{"type":"object","properties":{"field":{"type":["object","array"],"properties":{"0":{"type":"string"}},"items":{"type":"string","x-leafrun-file":true}}}}`)
	prepared, err := PrepareParameters(context.Background(), arrayFile, "schemas/input.json", []byte(`{"field":[null]}`), []FileRef{ref})
	if err != nil {
		t.Fatalf("actual file array item rejected: %v", err)
	}
	if err := VerifyPreparedInput(context.Background(), arrayFile, "schemas/input.json", prepared); err != nil {
		t.Fatalf("actual array file binding rejected by child: %v", err)
	}
	if err := VerifyPreparedInput(context.Background(), objectFile, "schemas/input.json", prepared); err == nil {
		t.Fatal("child accepted a non-file array binding")
	}
}

func TestArrayFileFieldsRejectUnboundPaths(t *testing.T) {
	files := parameterSchemaFS(`{"type":"object","properties":{"files":{"type":"array","items":{"type":"string","x-leafrun-file":true}}}}`)
	_, err := PrepareParameters(context.Background(), files, "schemas/input.json", []byte(`{"files":["/some/path"]}`), nil)
	assertProjectDiagnostic(t, err, "file_reference_required", "/files/0")
}

func TestArrayFileRefReplacesOnlyExplicitNullPlaceholder(t *testing.T) {
	files := parameterSchemaFS(`{"type":"object","properties":{"files":{"type":"array","items":{"type":"string","x-leafrun-file":true}}}}`)
	ref := FileRef{Pointer: "/files/0", ResourceID: "resource", Filename: "logo.svg", SHA256: strings.Repeat("a", 64), SizeBytes: 0}
	prepared, err := PrepareParameters(context.Background(), files, "schemas/input.json", []byte(`{"files":[null]}`), []FileRef{ref})
	if err != nil {
		t.Fatal(err)
	}
	if err := VerifyPreparedInput(context.Background(), files, "schemas/input.json", prepared); err != nil {
		t.Fatal(err)
	}
	if _, err := PrepareParameters(context.Background(), files, "schemas/input.json", []byte(`{"files":["/some/path"]}`), []FileRef{ref}); err == nil {
		t.Fatal("caller path replaced silently by fileRef")
	}
}
