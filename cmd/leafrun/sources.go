package main

import (
	"context"
	"errors"
	"slices"

	core "github.com/leafrun/leafrun-document-core"
	"github.com/leafrun/leafrun-document-core/internal/cli"
	integrations "github.com/leafrun/leafrun-integrations"
)

// providerBridge is intentionally in the executable: source adapters remain
// engine-independent and compilation only receives accepted snapshot bytes.
type providerBridge struct{ source integrations.Source }

func (bridge providerBridge) Open(ctx context.Context) (core.SourceTree, error) {
	tree, err := bridge.source.Open(ctx)
	guards := make([]core.NativeProtection, len(tree.NativeProtection))
	for i, guard := range tree.NativeProtection {
		guards[i] = core.NativeProtection{Path: guard.Path, Info: guard.Info}
	}
	origin := tree.Origin
	return core.SourceTree{FS: tree.FS, Close: tree.Close, ProtectedPaths: slices.Clone(tree.ProtectedPaths), NativeProtection: guards, Origin: core.SourceOrigin{Kind: origin.Kind, Location: origin.Location, SourceRoot: origin.SourceRoot, Ref: origin.Ref, Revision: origin.Revision, ArchiveSHA256: origin.ArchiveSHA256}}, err
}

func providerSource(options cli.SourceOptions) (core.ProjectSource, error) {
	if options.Archive != "" && options.Git == "" {
		return providerBridge{source: integrations.ArchiveSource{Path: options.Archive, SourceRoot: options.SourceRoot}}, nil
	}
	if options.Git != "" && options.Archive == "" {
		return providerBridge{source: integrations.GitSource{URL: options.Git, Ref: options.Ref, SourceRoot: options.SourceRoot}}, nil
	}
	return nil, errors.New("exactly one external source is required")
}
