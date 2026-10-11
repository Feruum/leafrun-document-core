# M01 verification record

Local verification on **2026-10-11**, Windows amd64, **Go 1.27.0**, **Typst 0.15.1 (9dfd3a08)**.

## Commands and observed results

- Official Typst installer: archive SHA-256 matched; installed binary reported the pinned version.
- `go test ./... -count=1 -v`: passed, including root and CLI packages; no tests skipped in the local run.
- `go vet ./...`: passed.
- `go build ./cmd/leafrun`: passed.
- Invoice CLI render: passed; output began with `%PDF-` and the absolute path was printed.
- Compiled CLI demonstration: successful invoice exit `0`, malformed source exit `1`, invalid argument exit `2`. Failed stdout was empty, the old PDF's SHA-256 was unchanged, and no `.leafrun-*` staging files remained.
- Clean checkout of the published contributor branch, with `GOWORK=off`: the official pinned installer, CLI build, and bundled invoice render passed. Replacing the source with an invalid document returned exit `1`, retained the previous PDF's SHA-256, and left no staging files; the fixture was restored and the checkout remained clean.
- Negative integration check with Typst removed from `PATH`: the real-render test failed with exit `1` and an installation message, rather than skipping.
- Linux amd64 and Darwin arm64 test binaries were cross-compiled on Windows; they were not executed locally. CI supplies native Linux/macOS checks and Linux `-race`.

## Scenario coverage

| Scenario | Test / observation |
|---|---|
| Nested imports, JSON, SVG | `TestRenderDefaultProjectWithNestedResources`; bundled invoice |
| Spaces, Unicode, custom entry/output | `TestRenderSpaceUnicodePathsAndCustomOutputReplacesOldPDF`; CLI test |
| Absolute output, repeated replacement | `TestRenderAbsoluteOutput`; custom-output test |
| Project and explicit fonts | `TestRenderBundledAndExplicitFonts` |
| Syntax, unknown variable, missing image, assert, panic | `TestRenderCompileErrorsPreserveOldPDFAndExitCause` |
| Font warning with successful PDF | `TestRenderMissingFontWarningStillPublishesPDF`; CLI warning test |
| Ordered nested call trace | `TestRenderRealNestedTrace`; parser fixtures |
| Unlocated / unknown-format diagnostics, hints, Windows and Unicode paths | Parser fixtures; nullable-location JSON test; raw-output subprocess fixtures |
| Source traversal and escaping links | `TestRenderProjectTraversalRemainsCompileDiagnostic`; file symlink and directory junction tests |
| Font/resource/entry output collisions, hardlinks and aliases | Input collision tests; independent font guard regression |
| Unavailable directory and Windows locked destination | Output directory failure test; `TestRenderWindowsLockedOutputLeavesOldPDF` |
| Canceled context, child process termination, temporary cleanup | `TestRenderCanceledContext`; version/compile subprocess cancellation fixtures |
| Missing / wrong engine, missing / invalid PDF or deps | `TestRenderMissingEngine`; `TestRenderEngineAndOutputFaults` |
| Parallel renders with separate outputs | `TestRenderParallelDifferentOutputs` |
| Oversized layout remains a successful render | `TestRenderOversizedLayoutIsAllowed` |
| Windows long paths | `TestRenderWindowsLongPaths` |
| Dependency paths relative to compiler cwd | `TestProtectDependenciesUsesCompilerWorkingDirectory`; real Unicode replacement and resource collision tests from an isolated cwd |
| Inherited custom fonts ignored; explicit paths accepted | `TestRenderIgnoresAmbientCustomFontPathsAndAcceptsExplicitFonts` |

Ordinary documents and compiler failures use the real pinned Typst. A child-process fixture supplies deterministic process cancellation, version faults, and output faults. An unlocated diagnostic is a parser fixture; it is not a fabricated source position. Windows file-symlink tests may skip when the host lacks symlink privileges; junction tests remain mandatory and did run locally.

Compilation success establishes a PDF artifact, not layout/content quality. PDF sizes and byte-for-byte equality across successful builds are not a contract. The integrity demonstration compares the *previous* output only to prove failure did not replace it.

## Delivery boundary

The current GitHub account can publish contributor branches and PRs but cannot write directly to the Leafrun repositories. A maintainer must accept the core PR and create `v0.1.0` before the Generation dependency can be checked from that released version. The Generation compatibility tests currently use an uncommitted development workspace joining the two repositories; no `replace` or workspace file is shipped.

Delivery follows [git flow](../CONTRIBUTING.md): feature PRs target `develop`; `release/0.1.0` carries the accepted core to `main` and the `v0.1.0` tag, with a merge back into `develop`. Upstream repositories currently have only `main`, so their integration branches must be initialized before feature acceptance.

M01 requires project/font inputs and directory links to stay unchanged during a render; the caller must not invoke process-wide `os.Chdir` concurrently. System font files are outside declared-font collision enumeration. Snapshotting mutable projects and server isolation belong to later stages.
