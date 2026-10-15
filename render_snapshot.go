package documentcore

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// RenderSnapshotOptions selects a logical io/fs entrypoint and a published PDF.
// Entrypoint defaults to main.typ. Output is required; relative outputs resolve
// against the caller's current working directory, never private snapshot storage.
// Fonts and their search order come exclusively from the accepted snapshot.
type RenderSnapshotOptions struct {
	Entrypoint  string            `json:"entrypoint"`
	Output      string            `json:"output"`
	Inputs      map[string]string `json:"inputs,omitempty"`
	PDFStandard string            `json:"pdfStandard,omitempty"`
}

// RenderSourceOptions selects capture inputs and a published PDF. Entrypoint is
// logical and defaults to main.typ. Output is required and resolves against the
// caller's current working directory. FontPaths, TempDir and Limits have the
// same semantics as SnapshotOptions. Folder capture excludes the chosen output.
type RenderSourceOptions struct {
	Entrypoint string
	Output     string
	FontPaths  []string
	TempDir    string
	Limits     SnapshotLimits
}

// RenderSnapshot borrows an accepted snapshot, compiles independent frozen
// project/font copies with Typst 0.15.1, and atomically publishes Output only
// after its working copy and dependency staging have been released. It never
// closes the borrowed snapshot. Concurrent Close is safe: once cloned, accepted
// inputs survive closure. The caller must keep cwd stable and use distinct
// output paths for concurrent renders. All failures leave PDFPath empty.
func RenderSnapshot(ctx context.Context, snapshot *Snapshot, options RenderSnapshotOptions) (RenderResult, error) {
	if snapshot == nil {
		return RenderResult{}, failure(ErrorValidation, "snapshot is required", ErrSnapshotClosed, "")
	}
	working, err := snapshot.prepareWorkingCopy(ctx)
	if err != nil {
		return RenderResult{}, snapshotRenderFailure(ctx, "prepare snapshot", err)
	}
	return renderSnapshotWorking(ctx, working, snapshot.root, options, nil)
}

// RenderSource captures and owns one snapshot, releases the source before
// compilation, then releases the snapshot and compiler copy before publication.
// It never compiles a live source or deletes caller-owned input trees.
func RenderSource(ctx context.Context, source ProjectSource, options RenderSourceOptions) (RenderResult, error) {
	output, err := snapshotOutput(options.Output)
	if err != nil {
		return RenderResult{}, err
	}
	snapshot, err := CreateSnapshot(ctx, source, SnapshotOptions{FontPaths: options.FontPaths, Output: output, TempDir: options.TempDir, Limits: options.Limits})
	if err != nil {
		return RenderResult{}, snapshotRenderFailure(ctx, "capture source", err)
	}
	working, err := snapshot.prepareWorkingCopy(ctx)
	if err != nil {
		return RenderResult{}, snapshotRenderFailure(ctx, "prepare snapshot", errors.Join(err, snapshot.Close()))
	}
	return renderSnapshotWorking(ctx, working, snapshot.root, RenderSnapshotOptions{Entrypoint: options.Entrypoint, Output: output}, snapshot.Close)
}

func snapshotRenderFailure(ctx context.Context, message string, err error) error {
	if ctx.Err() != nil {
		return canceled(ctx, err, "")
	}
	return failure(ErrorValidation, message, err, "")
}

func snapshotOutput(output string) (string, error) {
	if output == "" {
		return "", failure(ErrorValidation, "snapshot output path is required", nil, "")
	}
	absolute, err := filepath.Abs(output)
	if err != nil {
		return "", failure(ErrorValidation, "invalid output path", err, "")
	}
	return absolute, nil
}

func renderSnapshotWorking(ctx context.Context, working *snapshotWorkingCopy, snapshotRoot string, options RenderSnapshotOptions, ownedClose func() error) (result RenderResult, err error) {
	release := func() error {
		var err error
		err = errors.Join(err, working.Close())
		if ownedClose != nil {
			err = errors.Join(err, ownedClose())
		}
		return err
	}
	paths, err := prepareSnapshotPaths(ctx, working, snapshotRoot, options)
	if err != nil {
		return result, errors.Join(err, release())
	}
	return renderPrepared(ctx, paths, func(deps, base string) error { return protectSnapshotDependencies(working, paths, deps, base) }, release, true)
}

func prepareSnapshotPaths(ctx context.Context, working *snapshotWorkingCopy, snapshotRoot string, options RenderSnapshotOptions) (projectPaths, error) {
	paths := projectPaths{root: working.root, fonts: append([]string(nil), working.fonts...)}
	if err := ctx.Err(); err != nil {
		return paths, canceled(ctx, err, "")
	}
	inputs, pdfStandard, err := validateSnapshotRenderOptions(options)
	if err != nil {
		return paths, err
	}
	paths.inputs = inputs
	paths.pdfStandard = pdfStandard
	entry := options.Entrypoint
	if entry == "" {
		entry = "main.typ"
	}
	if !validSnapshotPath(entry) || entry == "." {
		return paths, failure(ErrorValidation, "entrypoint must be a logical snapshot file", nil, "")
	}
	paths.entry = filepath.Join(working.root, filepath.FromSlash(entry))
	info, err := os.Lstat(paths.entry)
	if err != nil || !info.Mode().IsRegular() {
		return paths, failure(ErrorValidation, "entrypoint must be a captured regular file", err, "")
	}
	paths.output, err = snapshotOutput(options.Output)
	if err != nil {
		return paths, err
	}
	// Native Windows extended spelling denotes the same original association.
	paths.requestedOutput = compilerNativePath(paths.output)
	if err := validateOutput(paths.output); err != nil {
		return paths, err
	}
	resolved, err := resolveFolderResult(paths.output)
	if err != nil {
		return paths, failure(ErrorStorage, "resolve output path", err, "")
	}
	paths.output = resolved
	if isContained(working.storage, paths.output) || isContained(snapshotRoot, paths.output) || isContained(working.storage, paths.requestedOutput) || isContained(snapshotRoot, paths.requestedOutput) {
		return paths, failure(ErrorValidation, "output must be outside private snapshot storage", nil, "")
	}
	if err := validateOutput(paths.output); err != nil {
		return paths, err
	}
	// Entrypoint and fonts are protected even if a compiler later fails or omits
	// them from its dependency report. Other project resources use that report.
	if err := protectSnapshotInputs(working, paths, map[string]bool{entry: true}); err != nil {
		return paths, err
	}
	return paths, nil
}

func validateSnapshotRenderOptions(options RenderSnapshotOptions) (map[string]string, string, error) {
	inputs := make(map[string]string, len(options.Inputs))
	for key, value := range options.Inputs {
		inputs[key] = value
	}
	if err := validateTypstInputs(inputs, false); err != nil {
		return nil, "", failure(ErrorValidation, err.Error(), nil, "")
	}
	if options.PDFStandard != "" && !validPDFStandardList(options.PDFStandard) {
		return nil, "", failure(ErrorValidation, "PDF standard is not supported by Typst 0.15.1", nil, "")
	}
	return inputs, options.PDFStandard, nil
}

func validateOutput(output string) error {
	info, err := os.Lstat(output)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return failure(ErrorStorage, "inspect output path", err, "")
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return failure(ErrorValidation, "output must not be a symbolic link or junction", nil, "")
	}
	if !info.Mode().IsRegular() {
		return failure(ErrorStorage, "output must be a regular file path", nil, "")
	}
	return nil
}

func protectSnapshotDependencies(working *snapshotWorkingCopy, paths projectPaths, depsTemp, base string) error {
	deps, err := readDependencies(depsTemp, base)
	if err != nil {
		return err
	}
	used := map[string]bool{}
	for _, dependency := range deps {
		if isContained(working.root, dependency) {
			relative, err := filepath.Rel(working.root, dependency)
			if err != nil {
				return err
			}
			used[filepath.ToSlash(relative)] = true
		}
	}
	return protectSnapshotInputs(working, paths, used)
}

func protectSnapshotInputs(working *snapshotWorkingCopy, paths projectPaths, used map[string]bool) error {
	outputInfo, err := os.Stat(paths.output)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	for _, input := range working.inputs {
		font := isFontFile(input.path) && (input.font || capturedFontInput(working, input.projectPath))
		required := input.guardOnly || font || used[input.projectPath] || (input.projectPath == "" && !input.excluded)
		if !required {
			continue
		}
		samePath := folderSamePath(input.path, paths.output) || folderSamePath(input.path, paths.requestedOutput)
		if input.guardOnly && input.info.IsDir() {
			samePath = isContained(input.path, paths.output) || isContained(input.path, paths.requestedOutput)
		}
		if samePath || (outputInfo != nil && input.info.Mode().IsRegular() && os.SameFile(outputInfo, input.info)) {
			return failure(ErrorValidation, "output would overwrite an original source or font input", fmt.Errorf("protected input %q", input.path), "")
		}
	}
	return nil
}

func capturedFontInput(working *snapshotWorkingCopy, logical string) bool {
	if logical == "" {
		return false
	}
	native := filepath.Join(working.root, filepath.FromSlash(logical))
	for _, font := range working.fonts {
		if isContained(font, native) {
			return true
		}
	}
	return false
}

func isFontFile(name string) bool {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".ttf", ".ttc", ".otf", ".otc":
		return true
	}
	return false
}

func normalizeSnapshotDiagnostics(diagnostics []Diagnostic, root, cwd string) {
	normalize := func(location *Location) {
		if location == nil {
			return
		}
		native := filepath.FromSlash(location.Path)
		native = compilerNativePath(native)
		if !filepath.IsAbs(native) {
			native = filepath.Join(cwd, native)
		}
		if isContained(root, native) {
			relative, err := filepath.Rel(root, native)
			if err == nil {
				location.Path = filepath.ToSlash(relative)
			}
		}
	}
	for i := range diagnostics {
		normalize(diagnostics[i].Location)
		for j := range diagnostics[i].Trace {
			normalize(diagnostics[i].Trace[j].Location)
		}
	}
}

func compilerNativePath(name string) string {
	if strings.HasPrefix(name, `\\?\UNC\`) {
		return `\\` + strings.TrimPrefix(name, `\\?\UNC\`)
	}
	return strings.TrimPrefix(name, `\\?\`)
}
