# Leafrun document core

Render a Typst project to PDF from Go or the `leafrun` CLI. M01 provides the local renderer; M02 adds immutable snapshots and folder, ZIP, TAR.GZ and Git sources for [LEA-12–LEA-17](https://linear.app/bulnik/issue/LEA-12). Both use the native **Typst CLI 0.15.1** and Go 1.27.0.

This feature is stacked on M01 and awaits review. Its standalone integrations dependency has not been released yet; use the temporary paired workspace below. Release integrations `v0.1.0` and verify core with `GOWORK=off` before releasing core `v0.2.0`.

## Run the feature from clean checkouts

Install [Go 1.27](https://go.dev/dl/) and the appropriate [Typst 0.15.1 release archive](https://github.com/typst/typst/releases/tag/v0.15.1). Extract the archive and put its directory on `PATH`. Check `typst --version` before rendering.

Use the related feature checkouts while upstream `develop` and the reviewed releases are pending. CI tests its exact core commit with integrations commit `a4414f5fe3f12815d305d14edba5396a97e5738b`, documentation over reviewed production `e28c912`; no workspace or local replacement is part of either module.

```sh
mkdir leafrun-m02
cd leafrun-m02
git clone --branch feature/lea-12-project-snapshots https://github.com/Feruum/leafrun-document-core.git core
git clone --branch feature/lea-12-source-adapters https://github.com/Feruum/leafrun-integrations.git integrations
git -C integrations checkout a4414f5fe3f12815d305d14edba5396a97e5738b
mkdir workspace
cd workspace
go work init ../core ../integrations
go work edit -replace=github.com/leafrun/leafrun-integrations@v0.1.0=../integrations
cd ..
export GOWORK="$PWD/workspace/go.work"
cd core
go build ./cmd/leafrun
go run ./cmd/leafrun render --project ./examples/invoice --snapshot
```

In PowerShell, after creating the workspace, set `$env:GOWORK = (Resolve-Path ./workspace/go.work).Path` from the `leafrun-m02` directory, then enter `core` and run the Go commands.

The version-specific override lives only in the temporary external `go.work`. It lets Go load dependency metadata for the planned, unreleased adapter version from the exact feature checkout. No local `replace` is shipped in a module. If the compatibility Generation checkout is also included, add `go work edit -replace=github.com/leafrun/leafrun-document-core@v0.1.0=../core` from this workspace. Go supports these [workspace replacement directives](https://go.dev/ref/mod#go-work-file-replace).

The last command prints the absolute path to `examples/invoice/output/result.pdf`. The example includes a nested Typst file, JSON, an SVG, and a bundled Fira Sans font with its license. Rendering this example needs no remote service or package download.

With PowerShell, the included installer downloads the pinned official archive, verifies its SHA-256 digest, and updates the current shell's `PATH`:

```powershell
. ./scripts/install-typst.ps1
go run ./cmd/leafrun render --project ./examples/invoice
```

The installer supports Linux, Windows, and macOS on x64 and arm64. It also sets `GITHUB_PATH` when running in CI.

## CLI

```sh
leafrun render --project ./project
leafrun render --project ./project --snapshot
leafrun render --project ./project --entrypoint templates/invoice.typ --output output/invoice.pdf
leafrun render --project ./project --font-path fonts/extra --font-path /absolute/font-directory
leafrun render --archive ./invoice.zip --output ./output/invoice.pdf
leafrun render --archive ./invoice.tar.gz --source-root invoice --output ./output/invoice.pdf
leafrun render --git ./repository --ref FULL_COMMIT --source-root examples/invoice --output ./output/invoice.pdf
```

Exactly one of `--project`, `--archive` or `--git` is required. A folder uses the existing live renderer unless `--snapshot` is supplied. Its defaults remain `main.typ` and `<project>/output/result.pdf`; relative entrypoint, output and font paths resolve from its project root. Absolute paths and explicitly supplied font directories are supported. `<project>/fonts` is included automatically when present.

Archives and Git always render an immutable snapshot. `--source-root` selects their project subdirectory; the default is the source root. They require `--output`, resolved from the caller's working directory. Git also requires `--ref`, selecting an unambiguous branch, tag or full commit. Native credential helpers and OpenSSH configuration handle private access without interactive prompts or a new OAuth connection.

Successful stdout contains only the absolute PDF path. Typst warnings and failure details go to stderr. Exit codes are `0` for success, `1` for rendering or writing failure, and `2` for invalid command arguments. Use `leafrun render --help` for usage.

Custom font directories must be supplied through these validated paths. Leafrun ignores inherited `TYPST_FONT_PATHS`; Typst's embedded and system fonts remain available.

## Go API

```go
import (
    "context"
    "encoding/json"
    "errors"
    "os/exec"

    documentcore "github.com/leafrun/leafrun-document-core"
)

result, err := documentcore.Render(context.Background(), documentcore.RenderOptions{
    ProjectDir: "./project",
    Entrypoint: "templates/invoice.typ",
    Output:     "output/invoice.pdf",
    FontPaths:  []string{"fonts/extra"},
})
diagnosticsJSON, _ := json.Marshal(result.Diagnostics)
_ = diagnosticsJSON
if err != nil {
    var renderError *documentcore.RenderError
    var processError *exec.ExitError
    _ = errors.As(err, &renderError)
    _ = errors.As(err, &processError)
    // Handle the error; result.PDFPath is empty on failure.
}
```

`RenderResult` retains `EngineVersion`, complete `RawOutput`, and structured `Diagnostics` even when compilation fails. A diagnostic has severity, message, hints, a nullable primary `location`, ordered trace frames, and its raw text. Locations preserve the path, line, and column emitted by Typst; the parser does not infer ranges or UTF-16 offsets from caret drawings. Unknown messages remain available in raw output.

`RenderError.Kind` distinguishes `validation`, `engine`, `compile`, `storage`, and `canceled` failures. `Unwrap` preserves the original cause, including `*exec.ExitError` and context cancellation. The library never exits the calling program.

## Snapshot APIs

`CreateSnapshot(ctx, source, SnapshotOptions)` captures a `ProjectSource` into privately owned immutable bytes. `FolderSource{Root: "./project"}` is the built-in local provider. The manifest contains sorted paths, sizes and SHA-256 values, separate source provenance, and ordered font trees. `Manifest()` returns a deep copy; `FS()` exposes read-only logical paths. `Close()` is idempotent and releases owned storage.

For a complete capture/render/cleanup operation:

```go
result, err := documentcore.RenderSource(ctx,
    documentcore.FolderSource{Root: "./project"},
    documentcore.RenderSourceOptions{
        Entrypoint: "main.typ",
        Output:     "./output/project.pdf",
        FontPaths:  []string{"fonts/extra"},
    },
)
```

`RenderSourceOptions` also exposes `TempDir` and `Limits` for capture. The entrypoint is a logical `io/fs` path, defaulting to `main.typ`. Output is required and resolves from the caller's working directory. Relative font paths select captured project subdirectories; absolute paths select extra native font directories. The CLI translates native local paths before calling these APIs.

`FontFS(index)` returns a read-only captured font tree relative to the directory at `Manifest().Fonts[index]`, including external fonts. It borrows the snapshot, validates the index and logical paths, and never opens the original font `SourcePath`. Both project and font views, including already open readers, stop working when the snapshot closes.

`RestoreSnapshot(ctx, SnapshotBundle, RestoreSnapshotOptions)` accepts a portable manifest, `ProjectFS`, and `ExternalFontFS` keyed by the exact manifest indices of external fonts. It validates v1 identity and every file's size and SHA-256, rejects missing or extra files and directories, and copies accepted bytes into privately owned storage. The input filesystems stay caller-owned; restoration closes every reader it opens. Provenance and ordered font entries survive unchanged, and their native paths are never used to obtain bytes. Options expose `TempDir` and the same `Limits` as capture. Project files are counted once even when font directories overlap; each external font tree counts separately.

`ValidateSnapshotManifest(manifest)` validates canonical structure and the attached content ID without filesystem access. `SnapshotContentID(manifest)` validates structure and computes v1 identity while ignoring the current ID and provenance. See [the bundle contract](docs/snapshot-bundles.md) for canonical arrays, ordering, ownership, and an in-memory roundtrip example.

To reuse accepted inputs, call `RenderSnapshot(ctx, snapshot, RenderSnapshotOptions{Entrypoint: "main.typ", Output: "./output/project.pdf"})`. It borrows the snapshot and uses only its captured font order and bytes. The caller retains ownership and must close the snapshot when finished. A render's prepared working copy survives closing that snapshot; a new render of a closed or zero snapshot fails.

Snapshot-render diagnostic positions and trace paths are relative to the accepted project. Lines, columns and nullable positions retain the engine's meaning. Raw engine output and raw diagnostic text remain unchanged. Capture errors expose their stage through `SnapshotError` and preserve original causes; `errors.Is` recognizes `ErrSourceChanged`, `ErrSnapshotClosed` and `ErrSnapshotLimit`.

## Files and publication

The adapter invokes Typst directly with an explicit project root, full human diagnostics, and colors disabled. A different engine version is rejected. Typst's relative import and resource semantics remain intact. Entrypoints and project links must stay inside the project; links in font directories must stay inside those explicitly allowed directories. Output paths cannot replace the entrypoint, source/resource dependencies reported by Typst, or fonts discovered in the declared font directories, including hardlink aliases.

Compilation writes to a unique temporary PDF beside the destination. After successful completion and PDF validation, Leafrun publishes it with the platform's atomic file replacement. Windows uses a dedicated replacement implementation. Snapshot rendering releases required owned compiler copies, dependency staging and owned snapshots before publication. Failed preparation, compilation, cancellation, mandatory cleanup or a locked output leaves the previous PDF intact. A borrowed snapshot stays owned by its caller.

The existing live renderer assumes the caller does not modify source files or directory links during a render. Folder capture uses optimistic inventory/content verification before and after copying, including same-size, same-time mutations, and returns `ErrSourceChanged` without retry. It is not an atomic filesystem snapshot. Changes after acceptance do not alter captured bytes. System fonts, package caches and the entire process environment are not frozen. The caller must keep the process working directory unchanged while rendering.

The local renderer does not provide a server isolation boundary or judge layout quality. For example, an oversized page element may compile successfully and still need visual review. Typst package imports follow Typst's own package behavior; the included example uses only local files. The Git adapter requires native Git/OpenSSH; on Linux its process exit barriers require kernel 5.3+ pidfds. Archive defaults are 10,000 files, 512 MiB expanded total and 128 MiB per file; Go APIs expose limits. See [the integrations contract](https://github.com/Feruum/leafrun-integrations/blob/feature/lea-12-source-adapters/docs/source-contract.md) for archive validation, private transports and native process behavior.

## Verification

```sh
go test ./... -count=1 -v
go vet ./...
go build ./cmd/leafrun
go run ./cmd/leafrun render --project ./examples/invoice
# Exercise all source formats and preservation of a previous PDF:
pwsh -File ./scripts/verify-sources.ps1 -CoreDirectory .
# Linux additionally:
go test -race ./... -count=1
```

Tests require the real pinned Typst binary and fail when it is unavailable. Paired feature CI runs on Linux, Windows and macOS with the exact integrations commit, verifies all source formats and a failed render preserving the previous PDF, and runs the Linux race detector. See the [M02 verification record](docs/m02-verification.md) and [M01 verification record](docs/verification.md) for tested versions and scenario coverage.

The compatibility wrapper lives in [leafrun-generation](https://github.com/leafrun/leafrun-generation). WASM, server jobs, S3 and OAuth are later stages. No Leafrun project license has been selected; the bundled font has its own OFL license.

Contributions use English and git flow; see [CONTRIBUTING.md](CONTRIBUTING.md) for feature, integration, and release branches.
