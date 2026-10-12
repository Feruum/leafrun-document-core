# M02: project sources and immutable snapshots

This feature is stacked on verified M01 commit `d7709509dfe1f9ab0484b5dc342b09a15c8da45a`. M01 acceptance and its `v0.1.0` release remain prerequisites for releasing this core feature as `v0.2.0`.

## Scope

One pipeline accepts a folder, ZIP, TAR.GZ or fixed Git revision, captures an immutable snapshot, then renders it through Typst 0.15.1. Go 1.27.0 remains the supported toolchain. The existing `Render(context.Context, RenderOptions)` and Generation `Render(string)` APIs retain M01 behavior.

The integrations module supplies independent `io/fs` trees and source metadata. It does not import core. Core owns capture, snapshot lifetime, original-input protection, diagnostics and PDF publication. Its CLI bridges the two contracts.

## Implemented capture contract

`ProjectSource.Open(context.Context)` returns `SourceTree`: an `fs.FS` rooted at `.`, `SourceOrigin`, a release callback and original native `ProtectedPaths`. `CreateSnapshot` opens the source once and releases its owned resources before returning, including acquisition failures that provide a release callback. It never retries a changing source.

`Snapshot.Manifest()` returns a deep copy. `Snapshot.FS()` exposes read-only logical paths and no native storage path; `Close` is idempotent. Internal compiler working copies have an independent lifetime. Capture errors expose their stage and preserve their cause through `Unwrap`; `ErrSourceChanged`, `ErrSnapshotClosed` and `ErrSnapshotLimit` can be checked with `errors.Is`.

`SnapshotOptions` controls private temporary storage, limits, ordered font paths and a selected generated result. Relative font paths are clean `io/fs` subdirectories; absolute font paths select native directories. The CLI later resolves native local path forms before invoking capture.

`FolderSource{Root: directory}` canonicalizes the explicitly selected root, then pins bounded native reads. The snapshot mode excludes `.git` components, the root `output` directory, ordinary `.leafrun-*` staging files and the selected regular result. Nested ordinary `output` directories remain resources. Explicit fonts inside the reserved project output tree are excluded in snapshot mode; the M01 live-folder renderer retains its existing behavior. Excluded output activity does not affect the content digest or require stable directory timestamps.

## Tasks and acceptance

1. **LEA-13: snapshot contract.** Private owned storage; deterministic content digest; ordered file size/SHA-256 manifests; separate provenance; deep manifest copies; read-only access; idempotent close; source release and cancellation; independent compiler copies that survive closing their source snapshot.
2. **LEA-14: folder source.** Canonical selected root; regular files only; reject internal links, junctions and special files; exclude Git metadata, generated output and Leafrun staging. Compare full inventories and hashes before/after capture, including same-size/same-time changes. Return `ErrSourceChanged` without retries.
3. **LEA-15: archive sources.** Separate history-preserving integrations repository. ZIP/TAR.GZ isolated extraction; portable path validation; no links, traversal, duplicates or case collisions; full checksum/stream verification; actual-read quotas and cancellation; complete owned cleanup.
4. **LEA-16: Git source.** Native Git and OpenSSH; HTTPS/SSH/local repositories; required unambiguous ref resolved to a full commit once; raw `ls-tree`/`cat-file` objects without checkout, hooks or filters. Explicitly reject submodules and LFS. Existing credential helpers and SSH configuration only; no interactive prompts, embedded HTTPS credentials or trace output. Cancel process trees and wait before cleanup.
5. **LEA-17: renderer and CLI.** A shared snapshot renderer; captured font order; logical diagnostic positions and unchanged raw engine output. Retain original input/archive/font hardlink protection. Clean owned sources and compiler copies before atomic PDF publication; any required cleanup failure preserves the previous PDF.

Archive defaults are 10,000 files, 512 MiB expanded total and 128 MiB per file; Go APIs expose limits. The snapshot content digest excludes timestamps, traversal order, temporary paths and source provenance. Git provenance retains requested ref and full commit; archive provenance retains SHA-256 of the original archive; folder revision uses captured content digest.

## CLI contract

Exactly one source is required:

```sh
leafrun render --project ./examples/invoice
leafrun render --project ./examples/invoice --snapshot
leafrun render --archive ./invoice.zip --output ./output/invoice.pdf
leafrun render --archive ./invoice.tar.gz --source-root invoice --output ./output/invoice.pdf
leafrun render --git https://github.com/leafrun/leafrun-document-core.git --ref FULL_COMMIT --source-root examples/invoice --output ./output/invoice.pdf
```

Folders retain M01 path semantics. Archive/Git require an explicit output resolved from the caller's working directory. `--source-root` selects an archive/Git subdirectory. Repeated `--font-path` values remain ordered. Successful stdout contains only the absolute PDF path; diagnostics go to stderr. Exit codes remain 0/1/2.

## Verification and delivery

Use one nested Typst/JSON/SVG/font example across every source. Verify immutable accepted bytes, capture mutations, corrupt/malicious archives, pinned Git refs, authentication failures, cancellation, descendants, cleanup, manifest copies, closed snapshots, locked outputs and original-input/hardlink guards. Successful PDFs are checked by format and behavior; byte equality is not a contract. Keep all M01 and Generation tests passing.

CI runs native Linux, Windows and macOS with Go 1.27.0, Typst 0.15.1, Git and OpenSSH. Missing mandatory tools fail. Run tests, vet, CLI build and source demos; Linux also runs the race detector. Local HTTPS/SSH fixtures use ephemeral credentials only. Pin `golang.org/x/crypto v0.58.0` for SSH fixtures and `golang.org/x/sys v0.49.0` for Windows process control.

Development and CI use a transient Go workspace with exact feature commits. Do not ship `go.work` or local `replace` directives. After review, release integrations `v0.1.0` first, pin that version in core and verify with `GOWORK=off`.

Keep own `feature/` branches and prepare related draft pull requests into `develop`. If upstream `develop` is missing, retain the feature branches; do not merge or release. Keep Linear tasks In Progress until their delivery and acceptance criteria are met. All repository text, commits and pull requests use English.

Folder consistency is optimistic verification, not an atomic filesystem snapshot. Accepted inputs are immutable; system fonts, the entire environment and PDF determinism are outside this milestone. S3, jobs, OAuth, synchronization, editors and document quality evaluation remain later work.
