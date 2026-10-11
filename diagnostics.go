package documentcore

import (
	"regexp"
	"strconv"
	"strings"
)

var humanLocation = regexp.MustCompile(`^(.+):([0-9]+):([0-9]+)$`)

// parseDiagnostics deliberately recognizes a small set of stable human-format
// markers. RawOutput remains the source of truth for everything else.
func parseDiagnostics(raw string) []Diagnostic {
	var diagnostics []Diagnostic
	for _, chunk := range strings.SplitAfter(raw, "\n") {
		line := strings.TrimSuffix(strings.TrimSuffix(chunk, "\n"), "\r")
		severity, message, found := strings.Cut(line, ": ")
		if found && (severity == "error" || severity == "warning") {
			diagnostics = append(diagnostics, Diagnostic{Severity: severity, Message: message, Raw: chunk})
			continue
		}
		if len(diagnostics) == 0 {
			continue
		}
		diagnostic := &diagnostics[len(diagnostics)-1]
		diagnostic.Raw += chunk
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "┌─ ") || strings.HasPrefix(trimmed, "╭─ ") {
			if diagnostic.Location == nil {
				diagnostic.Location = parseLocation(strings.TrimSpace(trimmed[len("┌─ "):]))
			}
			continue
		}
		if strings.HasPrefix(trimmed, "= hint: ") {
			trimmed = strings.TrimPrefix(trimmed, "= ")
		}
		if hint, ok := strings.CutPrefix(trimmed, "hint: "); ok {
			diagnostic.Hints = append(diagnostic.Hints, hint)
			continue
		}
		if strings.HasPrefix(trimmed, "while ") {
			frame := Trace{Message: trimmed}
			if index := strings.LastIndex(trimmed, " at "); index >= 0 {
				if location := parseLocation(trimmed[index+len(" at "):]); location != nil {
					frame.Message = trimmed[:index]
					frame.Location = location
				}
			}
			diagnostic.Trace = append(diagnostic.Trace, frame)
		}
	}
	return diagnostics
}

func parseLocation(value string) *Location {
	match := humanLocation.FindStringSubmatch(value)
	if match == nil {
		return nil
	}
	line, lineErr := strconv.Atoi(match[2])
	column, columnErr := strconv.Atoi(match[3])
	if lineErr != nil || columnErr != nil || line < 1 || column < 1 {
		return nil
	}
	return &Location{Path: match[1], Line: line, Column: column}
}
