// Package documentcore renders a local Typst project to PDF using Typst 0.15.1.
package documentcore

import "strings"

// RenderOptions identifies a project, its entrypoint, and the output PDF.
// Relative paths are resolved against ProjectDir. Entrypoint defaults to
// main.typ and Output defaults to output/result.pdf.
type RenderOptions struct {
	ProjectDir string   `json:"projectDir"`
	Entrypoint string   `json:"entrypoint"`
	Output     string   `json:"output"`
	FontPaths  []string `json:"fontPaths"`
}

// RenderResult contains the published path and the engine's diagnostics.
// PDFPath is empty on failure; RawOutput preserves all compiler output.
type RenderResult struct {
	PDFPath       string       `json:"pdfPath"`
	EngineVersion string       `json:"engineVersion"`
	RawOutput     string       `json:"rawOutput"`
	Diagnostics   []Diagnostic `json:"diagnostics"`
}

// Diagnostic is a human diagnostic emitted by Typst. Raw preserves its text,
// including any lines that this version of the parser does not understand.
type Diagnostic struct {
	Severity string    `json:"severity"`
	Message  string    `json:"message"`
	Location *Location `json:"location"`
	Hints    []string  `json:"hints"`
	Trace    []Trace   `json:"trace"`
	Raw      string    `json:"raw"`
}

// Trace is an ordered calling or evaluation context from a diagnostic.
type Trace struct {
	Message  string    `json:"message"`
	Location *Location `json:"location"`
}

// Location preserves Typst's emitted path and one-based line and column.
// A nil location means Typst did not emit a position for that diagnostic/frame.
type Location struct {
	Path   string `json:"path"`
	Line   int    `json:"line"`
	Column int    `json:"column"`
}

// ErrorKind classifies the stage at which a render failed.
type ErrorKind string

const (
	ErrorValidation ErrorKind = "validation"
	ErrorEngine     ErrorKind = "engine"
	ErrorCompile    ErrorKind = "compile"
	ErrorStorage    ErrorKind = "storage"
	ErrorCanceled   ErrorKind = "canceled"
)

// RenderError retains the original cause and all raw engine diagnostics.
type RenderError struct {
	Kind      ErrorKind
	Message   string
	RawOutput string
	Cause     error
}

func (e *RenderError) Error() string {
	message := e.Message
	if message == "" {
		message = "render failed (" + string(e.Kind) + ")"
	}
	if e.Cause != nil {
		message += ": " + e.Cause.Error()
	}
	if e.RawOutput != "" {
		if !strings.HasSuffix(message, "\n") {
			message += "\n"
		}
		message += e.RawOutput
	}
	return message
}

func (e *RenderError) Unwrap() error { return e.Cause }
