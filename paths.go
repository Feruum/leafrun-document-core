package documentcore

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

type projectPaths struct {
	root   string
	entry  string
	output string
	// Snapshot publication checks retain the requested original association as
	// well as the resolved destination, even if an original parent is retargeted.
	requestedOutput string
	fonts           []string
}

func preparePaths(ctx context.Context, options RenderOptions) (projectPaths, error) {
	var paths projectPaths
	if options.ProjectDir == "" {
		return paths, failure(ErrorValidation, "project directory is required", nil, "")
	}
	root, err := canonicalDirectory(options.ProjectDir)
	if err != nil {
		return paths, failure(ErrorValidation, "invalid project directory", err, "")
	}
	paths.root = root
	entry := options.Entrypoint
	if entry == "" {
		entry = "main.typ"
	}
	entry, err = absoluteFrom(root, entry)
	if err != nil || !isContained(root, entry) {
		return paths, failure(ErrorValidation, "entrypoint must be inside the project root", err, "")
	}
	entry, err = resolvePath(entry)
	if err != nil || !isContained(root, entry) {
		return paths, failure(ErrorValidation, "entrypoint must resolve inside the project root", err, "")
	}
	entryInfo, err := os.Stat(entry)
	if err != nil || !entryInfo.Mode().IsRegular() {
		return paths, failure(ErrorValidation, "entrypoint must be a regular file", err, "")
	}
	paths.entry = entry
	if err := validateTree(ctx, root); err != nil {
		return paths, failure(ErrorValidation, "unsafe project resource tree", err, "")
	}
	output := options.Output
	if output == "" {
		output = filepath.Join("output", "result.pdf")
	}
	paths.output, err = absoluteFrom(root, output)
	if err != nil {
		return paths, failure(ErrorValidation, "invalid output path", err, "")
	}
	outputInfo, err := os.Lstat(paths.output)
	if err == nil {
		if outputInfo.Mode()&os.ModeSymlink != 0 {
			return paths, failure(ErrorValidation, "output must not be a symbolic link or junction", nil, "")
		}
		if !outputInfo.Mode().IsRegular() {
			return paths, failure(ErrorStorage, "output must be a regular file path", nil, "")
		}
		if os.SameFile(entryInfo, outputInfo) {
			return paths, failure(ErrorValidation, "output would overwrite the entrypoint", nil, "")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return paths, failure(ErrorStorage, "inspect output path", err, "")
	}
	fontDirs := append([]string(nil), options.FontPaths...)
	defaultFonts := filepath.Join(root, "fonts")
	if _, err := os.Stat(defaultFonts); err == nil {
		fontDirs = append([]string{defaultFonts}, fontDirs...)
	} else if !errors.Is(err, os.ErrNotExist) {
		return paths, failure(ErrorValidation, "inspect project fonts directory", err, "")
	}
	for _, fontDir := range fontDirs {
		if fontDir == "" {
			return paths, failure(ErrorValidation, "font directory must not be empty", nil, "")
		}
		fontDir, err = absoluteFrom(root, fontDir)
		if err != nil {
			return paths, failure(ErrorValidation, "invalid font directory", err, "")
		}
		fontDir, err = canonicalDirectory(fontDir)
		if err != nil {
			return paths, failure(ErrorValidation, "font path must be a directory", err, "")
		}
		if err := validateTree(ctx, fontDir); err != nil {
			return paths, failure(ErrorValidation, "unsafe font directory tree", err, "")
		}
		if outputInfo != nil {
			if err := protectFontInputs(ctx, fontDir, outputInfo); err != nil {
				return paths, failure(ErrorValidation, "output conflicts with a font input", err, "")
			}
		}
		paths.fonts = append(paths.fonts, fontDir)
	}
	return paths, nil
}

// Typst's --deps list excludes font sources. Its pinned fontdb 0.23 scanner
// searches these four file extensions, so guard them independently, including
// file identities shared through hardlinks and directory aliases. Ordinary PDF
// outputs remain usable even when the project root is also a font directory.
func protectFontInputs(ctx context.Context, root string, output os.FileInfo) error {
	return filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if walkErr != nil {
			return walkErr
		}
		switch strings.ToLower(filepath.Ext(path)) {
		case ".ttf", ".ttc", ".otf", ".otc":
		default:
			return nil
		}
		info, err := os.Stat(path)
		if err != nil {
			return fmt.Errorf("inspect font input %q: %w", path, err)
		}
		if info.Mode().IsRegular() && os.SameFile(output, info) {
			return fmt.Errorf("output would overwrite font input %q", path)
		}
		return nil
	})
}

func canonicalDirectory(path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	canonical, err := resolvePath(abs)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(canonical)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", fmt.Errorf("%q is not a directory", path)
	}
	return canonical, nil
}

func absoluteFrom(root, path string) (string, error) {
	if !filepath.IsAbs(path) {
		path = filepath.Join(root, path)
	}
	return filepath.Abs(path)
}

func isContained(root, path string) bool {
	relative, err := filepath.Rel(root, path)
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) && !filepath.IsAbs(relative)
}

// WalkDir does not follow directory links. Any link resolving inside root has
// its target checked through that target's ordinary path in the same tree.
// Escaping links, broken links, and unresolved cycles are rejected.
func validateTree(ctx context.Context, root string) error {
	return filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if walkErr != nil {
			return walkErr
		}
		// Recent Go versions report Windows junctions as ModeIrregular rather
		// than ModeSymlink. Resolve both so directory junctions cannot escape.
		if entry.Type()&(os.ModeSymlink|os.ModeIrregular) != 0 {
			target, err := resolvePath(path)
			if err != nil {
				return fmt.Errorf("resolve link %q: %w", path, err)
			}
			if !isContained(root, target) {
				return fmt.Errorf("link %q escapes declared root %q", path, root)
			}
		}
		return nil
	})
}
