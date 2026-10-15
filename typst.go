package documentcore

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"
)

const requiredEngineVersion = "0.15.1"

var typstVersionLine = regexp.MustCompile(`^typst\s+([0-9]+\.[0-9]+\.[0-9]+(?:[-+][0-9A-Za-z.-]+)?)(?:\s+\([^\r\n]*\))?$`)

func verifyEngine(ctx context.Context) (engine, version, raw string, err error) {
	engine, err = exec.LookPath("typst")
	if err != nil {
		return "", "", "", failure(ErrorEngine, "find Typst 0.15.1 on PATH", err, "")
	}
	command := exec.CommandContext(ctx, engine, "--version")
	var stdout, stderr bytes.Buffer
	command.Stdout, command.Stderr = &stdout, &stderr
	if err := command.Run(); err != nil {
		raw = stdout.String() + stderr.String()
		if ctx.Err() != nil {
			return engine, "", raw, canceled(ctx, err, raw)
		}
		return engine, "", raw, failure(ErrorEngine, "verify Typst version", err, raw)
	}
	match := typstVersionLine.FindStringSubmatch(strings.TrimSpace(stdout.String()))
	if len(match) != 2 || match[1] != requiredEngineVersion {
		raw = stdout.String() + stderr.String()
		return engine, "", raw, failure(ErrorEngine, "Typst version must be exactly 0.15.1", fmt.Errorf("unexpected engine version"), raw)
	}
	return engine, requiredEngineVersion, stderr.String(), nil
}

func runCompiler(ctx context.Context, engine string, paths projectPaths, pdfTemp, depsTemp string) (raw, dependencyBase string, err error) {
	// Typst 0.15.1's zero dependency format is relative to the compiler cwd,
	// rather than --root. Resolve aliases such as macOS /var -> /private/var
	// to match the physical directory reported by Rust's current_dir().
	dependencyBase, err = canonicalDirectory(".")
	if err != nil {
		return "", "", fmt.Errorf("resolve compiler working directory: %w", err)
	}
	args := []string{"--color", "never", "compile", "--format", "pdf", "--root", paths.root, "--diagnostic-format", "human", "--deps", depsTemp, "--deps-format", "zero"}
	for _, fontPath := range paths.fonts {
		args = append(args, "--font-path", fontPath)
	}
	for _, key := range sortedProjectStringKeys(paths.inputs) {
		args = append(args, "--input", key+"="+paths.inputs[key])
	}
	if paths.pdfStandard != "" {
		args = append(args, "--pdf-standard", paths.pdfStandard)
	}
	args = append(args, paths.entry, pdfTemp)
	command := exec.CommandContext(ctx, engine, args...)
	// Custom font roots must come from validated RenderOptions or project/fonts.
	// Environment names are case insensitive on Windows. Other Typst defaults,
	// including embedded and system fonts, keep their ordinary environment.
	environment := os.Environ()
	command.Env = make([]string, 0, len(environment))
	for _, variable := range environment {
		name, _, _ := strings.Cut(variable, "=")
		if !strings.EqualFold(name, "TYPST_FONT_PATHS") {
			command.Env = append(command.Env, variable)
		}
	}
	// Every path is absolute. Inheriting the caller's cwd also avoids Windows
	// CreateProcess limitations on long working-directory paths.
	output, err := command.CombinedOutput()
	return string(output), dependencyBase, err
}
