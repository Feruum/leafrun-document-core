package documentcore

import (
	"reflect"
	"testing"
)

func TestParseHumanDiagnosticsPreservesLocationsAndOrderedTrace(t *testing.T) {
	raw := "error: unknown variable: leafrun_missing\n" +
		"  ┌─ \\\\?\\C:\\project é\\nested\\main.typ:1:11\n" +
		"  │\n1 │ #let g() = leafrun_missing\n  │            ^^^^^^^^^^^^^^^\n\n" +
		"  while calling `g` at \\\\?\\C:\\project é\\nested\\main.typ:2:11\n    g()\n" +
		"  while calling `f` at \\\\?\\C:\\project é\\main.typ:3:1\n    f()\n\n"
	got := parseDiagnostics(raw)
	if len(got) != 1 {
		t.Fatalf("got %d diagnostics, want 1", len(got))
	}
	d := got[0]
	if d.Severity != "error" || d.Message != "unknown variable: leafrun_missing" || d.Raw != raw {
		t.Fatalf("unexpected diagnostic: %#v", d)
	}
	if d.Location == nil || *d.Location != (Location{Path: `\\?\C:\project é\nested\main.typ`, Line: 1, Column: 11}) {
		t.Fatalf("primary location = %#v", d.Location)
	}
	want := []Trace{
		{Message: "while calling `g`", Location: &Location{Path: `\\?\C:\project é\nested\main.typ`, Line: 2, Column: 11}},
		{Message: "while calling `f`", Location: &Location{Path: `\\?\C:\project é\main.typ`, Line: 3, Column: 1}},
	}
	if !reflect.DeepEqual(d.Trace, want) {
		t.Fatalf("trace = %#v, want %#v", d.Trace, want)
	}
}

func TestParseHumanDiagnosticsHintsMultipleMessagesAndUnknownText(t *testing.T) {
	first := "warning: unknown font family: absent\r\n  ┌─ /project/文档.typ:4:8\r\n  hint: choose an installed font\r\n  hint: check spelling\r\n  while calling `render`\r\n  future CLI line retained verbatim\r\n\r\n"
	second := "error: failed to export PDF\r\n  an unknown exporter detail\r\n"
	got := parseDiagnostics("unparsed engine preamble\r\n" + first + second)
	if len(got) != 2 {
		t.Fatalf("got %d diagnostics, want 2", len(got))
	}
	if got[0].Raw != first || got[1].Raw != second {
		t.Fatalf("raw text changed: %#v", got)
	}
	if !reflect.DeepEqual(got[0].Hints, []string{"choose an installed font", "check spelling"}) {
		t.Fatalf("hints = %#v", got[0].Hints)
	}
	if len(got[0].Trace) != 1 || got[0].Trace[0].Message != "while calling `render`" || got[0].Trace[0].Location != nil {
		t.Fatalf("unlocated trace = %#v", got[0].Trace)
	}
	if got[1].Location != nil || got[1].Severity != "error" || got[1].Message != "failed to export PDF" {
		t.Fatalf("unlocated diagnostic = %#v", got[1])
	}
}

func TestParseHumanDiagnosticsDoesNotInventDiagnostics(t *testing.T) {
	for _, raw := range []string{"", "runtime output in an unknown format\n", "1 │ error: source text, not a diagnostic\n"} {
		if got := parseDiagnostics(raw); len(got) != 0 {
			t.Fatalf("invented diagnostics for %q: %#v", raw, got)
		}
	}
}
