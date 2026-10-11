// Package cli implements the leafrun command without exiting the calling process.
package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"

	documentcore "github.com/leafrun/leafrun-document-core"
)

const usage = "usage: leafrun render --project DIR [--entrypoint main.typ] [--output output/result.pdf] [--font-path DIR ...]"

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
	flags.StringVar(&options.ProjectDir, "project", "", "local project directory (required)")
	flags.StringVar(&options.Entrypoint, "entrypoint", "main.typ", "entrypoint relative to the project root")
	flags.StringVar(&options.Output, "output", "output/result.pdf", "output path relative to the project root, or an absolute path")
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
	if options.ProjectDir == "" || flags.NArg() != 0 {
		fmt.Fprintln(stderr, usage)
		return 2
	}
	options.FontPaths = []string(fonts)
	result, err := documentcore.Render(ctx, options)
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
