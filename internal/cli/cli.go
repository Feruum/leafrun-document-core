// Package cli implements the leafrun command without exiting the calling process.
package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	documentcore "github.com/leafrun/leafrun-document-core"
)

const usage = "usage: leafrun render (--project DIR [--snapshot] | --archive FILE | --git URL --ref REF) [--source-root DIR] [--entrypoint main.typ] [--output PDF] [--font-path DIR ...]"

type fontPaths []string

func (p *fontPaths) String() string { return fmt.Sprint([]string(*p)) }
func (p *fontPaths) Set(value string) error {
	if value == "" {
		return errors.New("font path must not be empty")
	}
	*p = append(*p, value)
	return nil
}

// Run returns a process exit code: 0 for success, 1 for render failure, 2 for usage.
func Run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	return RunWithSources(ctx, args, stdout, stderr, nil)
}

// SourceOptions describes an external provider request without coupling the
// common CLI to integrations. Archive and Git are mutually exclusive.
type SourceOptions struct{ Archive, Git, SourceRoot, Ref string }

// SourceFactory explicitly bridges a provider's standalone source contract.
type SourceFactory func(SourceOptions) (documentcore.ProjectSource, error)

// RunWithSources accepts the executable's provider bridge. Process exit and
// signal handling remain the executable's responsibility.
func RunWithSources(ctx context.Context, args []string, stdout, stderr io.Writer, factory SourceFactory) int {
	if len(args) == 1 && (args[0] == "--help" || args[0] == "-h") {
		fmt.Fprintln(stdout, usage)
		return 0
	}
	if len(args) == 0 || args[0] != "render" {
		fmt.Fprintln(stderr, usage)
		return 2
	}
	flags := flag.NewFlagSet("render", flag.ContinueOnError)
	flags.SetOutput(stderr)
	var options documentcore.RenderOptions
	var fonts fontPaths
	var sourceOptions SourceOptions
	var snapshot bool
	flags.StringVar(&options.ProjectDir, "project", "", "local project directory (required)")
	flags.StringVar(&options.Entrypoint, "entrypoint", "main.typ", "entrypoint relative to the project root")
	flags.StringVar(&options.Output, "output", "output/result.pdf", "output path relative to the project root, or an absolute path")
	flags.StringVar(&sourceOptions.Archive, "archive", "", "ZIP or TAR.GZ archive")
	flags.StringVar(&sourceOptions.Git, "git", "", "Git repository URL or local path")
	flags.StringVar(&sourceOptions.Ref, "ref", "", "required branch, tag or full commit for Git")
	flags.StringVar(&sourceOptions.SourceRoot, "source-root", ".", "archive or Git project subdirectory")
	flags.BoolVar(&snapshot, "snapshot", false, "capture a local project before rendering")
	flags.Var(&fonts, "font-path", "additional font directory (repeatable; relative to project root)")
	// flag.Parse calls Usage for help and parse errors. Route help to stdout below.
	flags.Usage = func() {}
	if err := flags.Parse(args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			fmt.Fprintln(stdout, usage)
			return 0
		}
		fmt.Fprintln(stderr, usage)
		return 2
	}
	supplied := map[string]bool{}
	flags.Visit(func(f *flag.Flag) { supplied[f.Name] = true })
	count := 0
	for _, value := range []string{options.ProjectDir, sourceOptions.Archive, sourceOptions.Git} {
		if value != "" {
			count++
		}
	}
	invalid := count != 1 || flags.NArg() != 0
	for _, name := range []string{"project", "archive", "git", "output", "entrypoint"} {
		if supplied[name] && flags.Lookup(name).Value.String() == "" {
			invalid = true
		}
	}
	if options.ProjectDir != "" && (supplied["source-root"] || supplied["ref"]) {
		invalid = true
	}
	if sourceOptions.Git == "" && supplied["ref"] {
		invalid = true
	}
	if options.ProjectDir == "" && (supplied["snapshot"] || !supplied["output"] || options.Output == "") {
		invalid = true
	}
	if sourceOptions.Git != "" && sourceOptions.Ref == "" {
		invalid = true
	}
	if supplied["source-root"] && sourceOptions.SourceRoot == "" {
		invalid = true
	}
	if invalid {
		fmt.Fprintln(stderr, usage)
		return 2
	}
	options.FontPaths = []string(fonts)
	var result documentcore.RenderResult
	var err error
	if options.ProjectDir != "" && !snapshot {
		result, err = documentcore.Render(ctx, options)
	} else if options.ProjectDir != "" {
		var captured documentcore.RenderSourceOptions
		captured, err = folderSnapshotOptions(options)
		if err == nil {
			result, err = documentcore.RenderSource(ctx, documentcore.FolderSource{Root: options.ProjectDir}, captured)
		}
	} else {
		if factory == nil {
			fmt.Fprintln(stderr, "external source provider is unavailable")
			return 1
		}
		var source documentcore.ProjectSource
		source, err = factory(sourceOptions)
		if err == nil {
			result, err = documentcore.RenderSource(ctx, source, documentcore.RenderSourceOptions{Entrypoint: filepath.ToSlash(options.Entrypoint), Output: options.Output, FontPaths: options.FontPaths})
		}
	}
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if result.RawOutput != "" {
		fmt.Fprint(stderr, result.RawOutput)
	}
	if _, err := fmt.Fprintln(stdout, result.PDFPath); err != nil {
		fmt.Fprintf(stderr, "write result path: %v\n", err)
		return 1
	}
	return 0
}

func folderSnapshotOptions(options documentcore.RenderOptions) (documentcore.RenderSourceOptions, error) {
	absolute, err := filepath.Abs(options.ProjectDir)
	if err != nil {
		return documentcore.RenderSourceOptions{}, err
	}
	root, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return documentcore.RenderSourceOptions{}, err
	}
	// os.Stat also rejects a file selected as a project directory.
	info, err := os.Stat(root)
	if err != nil {
		return documentcore.RenderSourceOptions{}, err
	}
	if !info.IsDir() {
		return documentcore.RenderSourceOptions{}, errors.New("project must be a directory")
	}
	logical := func(native string) (string, error) {
		if !filepath.IsAbs(native) {
			native = filepath.Join(root, native)
		}
		canonical, err := filepath.EvalSymlinks(native)
		if err != nil {
			return "", err
		}
		relative, err := filepath.Rel(root, canonical)
		if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) || filepath.IsAbs(relative) {
			return "", errors.New("entrypoint must be inside the project root")
		}
		return filepath.ToSlash(relative), nil
	}
	entry, err := logical(options.Entrypoint)
	if err != nil {
		return documentcore.RenderSourceOptions{}, err
	}
	output := options.Output
	if !filepath.IsAbs(output) {
		output = filepath.Join(root, output)
	}
	fonts := append([]string(nil), options.FontPaths...)
	for i, font := range fonts {
		if !filepath.IsAbs(font) {
			font = filepath.Join(root, font)
		}
		canonical, err := filepath.EvalSymlinks(font)
		if err != nil {
			return documentcore.RenderSourceOptions{}, err
		}
		relative, err := filepath.Rel(root, canonical)
		if err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) && !filepath.IsAbs(relative) {
			fonts[i] = filepath.ToSlash(relative)
		} else {
			fonts[i] = canonical
		}
	}
	return documentcore.RenderSourceOptions{Entrypoint: entry, Output: output, FontPaths: fonts}, nil
}
