# Portable snapshot bundles

`SnapshotBundle` carries a `SnapshotManifest`, a project tree rooted at `.` in `ProjectFS`, and external font trees rooted at `.` in `ExternalFontFS`. The map keys are indices in `Manifest.Fonts`, including gaps occupied by in-project fonts. Supply every external index exactly once and no in-project or unrelated indices. In-project font entries must exactly describe their project subtrees; callers do not supply duplicate filesystems for them.

```go
bundle := documentcore.SnapshotBundle{
    Manifest:       snapshot.Manifest(),
    ProjectFS:      snapshot.FS(),
    ExternalFontFS: make(map[int]fs.FS),
}
for index, font := range bundle.Manifest.Fonts {
    if font.InProject {
        continue
    }
    fontFS, err := snapshot.FontFS(index)
    if err != nil {
        return err
    }
    bundle.ExternalFontFS[index] = fontFS
}
restored, err := documentcore.RestoreSnapshot(ctx, bundle,
    documentcore.RestoreSnapshotOptions{TempDir: temporaryParent})
if err != nil {
    return err
}
defer restored.Close()
// The original snapshot can now close. Restored owns independent copied bytes.
```

The same API accepts archive, memory, or remotely backed logical filesystems. Keep the borrowed inputs stable until restoration returns. Restore verifies their exact membership and file bytes before copying and checks them again afterward. A file is copied through ordinary reads and writes, without hardlinking the input. Modification times and native identities do not affect content identity. The copied snapshot has no native association with provenance paths; rendering does not reopen those paths.

`FontFS(index)` uses declared search order, including repeated directories. Returned names are relative to the selected font root. Invalid indices return an error matching `fs.ErrInvalid`; a closed or zero snapshot returns `ErrSnapshotClosed`. Open files, directory readers and filesystem views are all invalidated by `Snapshot.Close`. Public file information has read-only permissions and no native `Sys` payload, and readers expose no native path or writing methods.

## Version 1 manifest validation

`ValidateSnapshotManifest` and `SnapshotContentID` perform no filesystem access. Validation requires version 1, strictly sorted unique file paths and directory paths, valid logical `io/fs` names without backslashes, declared parent directories, and no file/directory collisions. Native materialization restrictions apply on the current platform. The root `.` is implicit and is omitted from both lists. Files have nonnegative sizes and lowercase 64-character hexadecimal SHA-256 values. External font paths are `.`; in-project font paths select declared project directories, including `.`. Font trees follow the same structure rules and preserve their declared array order.

File, directory and font lists are canonical JSON arrays, including `[]` for empty lists. `null` lists are rejected. Validation does not normalize, sort, deduplicate or otherwise change a manifest. `Origin` and `SourcePath` remain opaque provenance; they can refer to resources that no longer exist.

The v1 content ID is the lowercase SHA-256 of the existing compact JSON content encoding: `FormatVersion`, `Files`, `Directories`, then `Fonts`. Each ordered font contains `Path`, `Files`, then `Directories`. File records retain the JSON keys `path`, `size`, and `sha256`. The current manifest `ID`, all source provenance, font `SourcePath`, font `InProject`, modification times and native temporary paths are absent from this encoding. This is the unchanged capture identity algorithm.

`SnapshotContentID` validates structure and computes identity regardless of the current `ID`, allowing a producer to attach it afterward. `ValidateSnapshotManifest` additionally requires the attached ID to match. `RestoreSnapshot` uses that validation, then verifies every supplied tree's exact paths, directories, sizes and hashes. A producer computing a matching ID for altered manifest records still must provide matching portable bytes.

## Limits and ownership

`RestoreSnapshotOptions` has `TempDir string` and `Limits SnapshotLimits`. Zero fields retain capture defaults: 10,000 files, 512 MiB total and 128 MiB per file. Negative limits are invalid. Project files and overlapping in-project font files count once; each external tree counts separately in ordered font declarations. Both declared sizes and actual reads are bounded. Errors preserve `ErrSnapshotLimit`, context cancellation and input reader-close causes through `SnapshotError`.

Restore borrows the supplied filesystems and never calls their owner-level `Close` methods. It closes files it opens for scanning, copying and verification, including on cancellation and failure. Failed restoration removes its allocated storage. Successful restoration owns its copied bytes and a deep copy of its manifest; close it when finished. The caller may release input snapshots or other filesystems immediately after restoration succeeds.
