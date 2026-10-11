package documentcore

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestRenderErrorRetainsCauseAndRawDiagnostic(t *testing.T) {
	cause := errors.New("process failed")
	err := &RenderError{Kind: ErrorCompile, Message: "Typst compilation failed", RawOutput: "error: unknown variable: missing\n", Cause: cause}
	if !errors.Is(err, cause) {
		t.Fatal("RenderError must unwrap its original cause")
	}
	if !strings.Contains(err.Error(), "error: unknown variable: missing") || !strings.Contains(err.Error(), "Typst compilation failed") {
		t.Fatalf("error omitted raw diagnostic or context: %q", err.Error())
	}
	if err.Kind != ErrorKind("compile") {
		t.Fatalf("kind = %q", err.Kind)
	}
}

func TestDiagnosticJSONHasExplicitNullableLocations(t *testing.T) {
	data, err := json.Marshal(RenderResult{Diagnostics: []Diagnostic{{Severity: "error", Message: "export failed", Trace: []Trace{{Message: "while calling `export`"}}, Raw: "error: export failed\n"}}})
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"pdfPath", "engineVersion", "rawOutput", "diagnostics"} {
		if _, ok := got[key]; !ok {
			t.Fatalf("missing JSON key %q in %s", key, data)
		}
	}
	d := got["diagnostics"].([]any)[0].(map[string]any)
	if location, ok := d["location"]; !ok || location != nil {
		t.Fatalf("expected explicit nullable diagnostic location: %s", data)
	}
	frame := d["trace"].([]any)[0].(map[string]any)
	if location, ok := frame["location"]; !ok || location != nil {
		t.Fatalf("expected explicit nullable trace location: %s", data)
	}
}
