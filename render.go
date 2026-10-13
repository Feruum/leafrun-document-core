package documentcore

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// Render renders a local project with Typst and atomically publishes the PDF.
// The project and explicit font directories must remain unchanged until Render
// returns. The caller must not change the process working directory during a
// render. Concurrent renders must use different output paths.
func Render(ctx context.Context, options RenderOptions) (RenderResult, error) {
	var result RenderResult
	if err := ctx.Err(); err != nil {
		return result, failure(ErrorCanceled, "render canceled", err, "")
	}
	paths, err := preparePaths(ctx, options)
	if err != nil {
		if ctx.Err() != nil {
			return result, canceled(ctx, err, "")
		}
		return result, err
	}
	return renderPrepared(ctx, paths, func(deps, base string) error { return protectDependencies(paths, deps, base) }, nil, false)
}

// renderPrepared releases operation-owned inputs before replacing the PDF.
func renderPrepared(ctx context.Context, paths projectPaths, protect func(string, string) error, release func() error, normalize bool) (result RenderResult, err error) {
	var pdfTemp, depsTemp string
	released := false
	cleanup := func() error {
		var cleanupErr error
		if depsTemp != "" {
			cleanupErr = errors.Join(cleanupErr, os.Remove(depsTemp))
			depsTemp = ""
		}
		if !released && release != nil {
			released = true
			cleanupErr = errors.Join(cleanupErr, release())
		}
		return cleanupErr
	}
	defer func() {
		cleanupErr := cleanup()
		if pdfTemp != "" {
			cleanupErr = errors.Join(cleanupErr, os.Remove(pdfTemp))
		}
		if cleanupErr != nil {
			err = errors.Join(err, failure(ErrorStorage, "release render resources", cleanupErr, result.RawOutput))
		}
	}()
	engine, version, raw, err := verifyEngine(ctx)
	result.RawOutput = raw
	result.Diagnostics = parseDiagnostics(raw)
	if err != nil {
		return result, err
	}
	result.EngineVersion = version
	if err := os.MkdirAll(filepath.Dir(paths.output), 0o755); err != nil {
		return result, failure(ErrorStorage, "create output directory", err, result.RawOutput)
	}
	// Resolve existing parent aliases before creating staging files. Staging and
	// publication therefore use the same directory and filesystem.
	parent, err := resolvePath(filepath.Dir(paths.output))
	if err != nil {
		return result, failure(ErrorStorage, "resolve output directory", err, result.RawOutput)
	}
	paths.output = filepath.Join(parent, filepath.Base(paths.output))
	pdfTemp, err = createStaging(parent, ".leafrun-*.pdf")
	if err != nil {
		return result, failure(ErrorStorage, "create temporary PDF", err, result.RawOutput)
	}
	depsTemp, err = createStaging(parent, ".leafrun-*.deps")
	if err != nil {
		return result, failure(ErrorStorage, "create temporary dependency list", err, result.RawOutput)
	}
	// runCompiler does not return until CommandContext has killed/waited for a
	// canceled child. Deferred cleanup cannot race a still-running compiler.
	compileRaw, dependencyBase, err := runCompiler(ctx, engine, paths, pdfTemp, depsTemp)
	result.RawOutput += compileRaw
	result.Diagnostics = parseDiagnostics(result.RawOutput)
	if normalize {
		normalizeSnapshotDiagnostics(result.Diagnostics, paths.root, dependencyBase)
	}
	if err != nil {
		if ctx.Err() != nil {
			return result, canceled(ctx, err, result.RawOutput)
		}
		return result, failure(ErrorCompile, "Typst compilation failed", err, result.RawOutput)
	}
	if err := ctx.Err(); err != nil {
		return result, canceled(ctx, err, result.RawOutput)
	}
	if err := verifyPDF(pdfTemp); err != nil {
		return result, failure(ErrorStorage, "verify temporary PDF", err, result.RawOutput)
	}
	if err := protect(depsTemp, dependencyBase); err != nil {
		var renderErr *RenderError
		if errors.As(err, &renderErr) {
			renderErr.RawOutput = result.RawOutput
			return result, renderErr
		}
		return result, failure(ErrorStorage, "verify compilation dependencies", err, result.RawOutput)
	}
	if err := ctx.Err(); err != nil {
		return result, canceled(ctx, err, result.RawOutput)
	}
	if err := cleanup(); err != nil {
		return result, failure(ErrorStorage, "release render resources before publication", err, result.RawOutput)
	}
	if err := ctx.Err(); err != nil {
		return result, canceled(ctx, err, result.RawOutput)
	}
	if err := publishPDF(pdfTemp, paths.output); err != nil {
		return result, failure(ErrorStorage, "publish PDF", err, result.RawOutput)
	}
	pdfTemp = ""
	result.PDFPath = paths.output
	return result, nil
}

func failure(kind ErrorKind, message string, cause error, raw string) *RenderError {
	return &RenderError{Kind: kind, Message: message, Cause: cause, RawOutput: raw}
}

func canceled(ctx context.Context, cause error, raw string) *RenderError {
	return failure(ErrorCanceled, "render canceled", errors.Join(ctx.Err(), cause), raw)
}

func createStaging(parent, pattern string) (string, error) {
	file, err := os.CreateTemp(parent, pattern)
	if err != nil {
		return "", err
	}
	name := file.Name()
	if err := file.Close(); err != nil {
		os.Remove(name)
		return "", err
	}
	return name, nil
}

func verifyPDF(path string) error {
	file, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		return err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Size() <= int64(len("%PDF-")) {
		return fmt.Errorf("Typst did not produce a nonempty PDF")
	}
	var header [5]byte
	if _, err := io.ReadFull(file, header[:]); err != nil {
		return err
	}
	if !bytes.Equal(header[:], []byte("%PDF-")) {
		return fmt.Errorf("Typst output does not start with %%PDF-")
	}
	if err := file.Sync(); err != nil {
		return err
	}
	return file.Close()
}

func readDependencies(depsTemp, dependencyBase string) ([]string, error) {
	data, err := os.ReadFile(depsTemp)
	if err != nil {
		return nil, err
	}
	if len(data) == 0 || data[len(data)-1] != 0 {
		return nil, fmt.Errorf("Typst did not report a complete dependency list")
	}
	dependencies := []string{}
	for _, encoded := range bytes.Split(data, []byte{0}) {
		if len(encoded) == 0 {
			continue
		}
		dependency := compilerNativePath(string(encoded))
		if !filepath.IsAbs(dependency) {
			dependency = filepath.Join(dependencyBase, dependency)
		}
		if _, err := os.Stat(dependency); err != nil {
			return nil, fmt.Errorf("inspect dependency %q: %w", dependency, err)
		}
		dependencies = append(dependencies, dependency)
	}
	return dependencies, nil
}

func protectDependencies(paths projectPaths, depsTemp, dependencyBase string) error {
	dependencies, err := readDependencies(depsTemp, dependencyBase)
	if err != nil {
		return err
	}
	outputInfo, err := os.Stat(paths.output)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, dependency := range dependencies {
		info, err := os.Stat(dependency)
		if err != nil {
			return err
		}
		if os.SameFile(outputInfo, info) {
			return failure(ErrorValidation, "output would overwrite a project source or resource", nil, "")
		}
	}
	return nil
}
