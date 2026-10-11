# Leafrun document core

Render a local Typst project to PDF from Go or the `leafrun` CLI. M01 implements [LEA-5–LEA-11](https://linear.app/bulnik/issue/LEA-5) using the native **Typst CLI 0.15.1** and Go 1.27.0.

## Run from a clean checkout

Install [Go 1.27](https://go.dev/dl/) and the appropriate [Typst 0.15.1 release archive](https://github.com/typst/typst/releases/tag/v0.15.1). Extract the archive and put its directory on `PATH`. Check `typst --version` before rendering.

```sh
git clone https://github.com/leafrun/leafrun-document-core.git
cd leafrun-document-core
go build ./cmd/leafrun
go run ./cmd/leafrun render --project ./examples/invoice
```

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
leafrun render --project ./project --entrypoint templates/invoice.typ --output output/invoice.pdf
leafrun render --project ./project --font-path fonts/extra --font-path /absolute/font-directory
```

`--project` is required. The defaults are `main.typ` and `<project>/output/result.pdf`. Relative entrypoint, output, and font paths resolve from the project root. Absolute output paths and explicitly supplied font directories are supported. `<project>/fonts` is included automatically when present.

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

## Files and publication

The adapter invokes Typst directly with an explicit project root, full human diagnostics, and colors disabled. A different engine version is rejected. Typst's relative import and resource semantics remain intact. Entrypoints and project links must stay inside the project; links in font directories must stay inside those explicitly allowed directories. Output paths cannot replace the entrypoint, source/resource dependencies reported by Typst, or fonts discovered in the declared font directories, including hardlink aliases.

Compilation writes to a unique temporary PDF beside the destination. After successful completion and PDF validation, Leafrun publishes it with the platform's atomic file replacement. Windows uses a dedicated replacement implementation. Failed compilation, cancellation, or a locked output leaves the previous PDF intact. Temporary files are cleaned after the process finishes.

M01 assumes the caller does not modify source files or directory links during a render. Snapshotting mutable projects belongs to M02. This local renderer does not provide a server isolation boundary or judge layout quality. For example, an oversized page element may compile successfully and still need visual review. Typst package imports follow Typst's own package behavior; the included example uses only local files.

## Verification

```sh
go test ./... -count=1 -v
go vet ./...
go build ./cmd/leafrun
go run ./cmd/leafrun render --project ./examples/invoice
# Linux additionally:
go test -race ./... -count=1
```

Tests require the real pinned Typst binary and fail when it is unavailable. CI runs on Linux, Windows, and macOS and verifies the example as well as library and CLI behavior. See [the verification record](docs/verification.md) for the tested versions and scenario coverage.

The compatibility wrapper lives in [leafrun-generation](https://github.com/leafrun/leafrun-generation). WASM, server jobs, Git import, S3, and authorization are later stages. No Leafrun project license has been selected; the bundled font has its own OFL license.

Contributions use English and git flow; see [CONTRIBUTING.md](CONTRIBUTING.md) for feature, integration, and release branches.
