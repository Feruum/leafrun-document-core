# M02 verification

Status: implementation in progress. This file records evidence after checks run; pending rows are not acceptance claims.

## Environment and baseline

On 2026-10-12, the isolated core feature worktree started at M01 `d7709509dfe1f9ab0484b5dc342b09a15c8da45a`. Its unchanged baseline `go test ./... -count=1` passed with Go 1.27.0 and Typst 0.15.1 on Windows. The integrations checkout retains all three upstream commits through `a6492ffb81166d2515ac78e2cf04e9425022aebb`.

## Acceptance evidence

| Area | Required behavior | Evidence |
| --- | --- | --- |
| Manifest | Sorted file hashes, deep copies, origin-independent content ID, ordered fonts | Snapshot tests passed at `95d6993`; independent spec and quality reviews approved |
| Snapshot lifetime | Immutable accepted bytes, read-only access, idempotent Close, closed-snapshot rejection | Accepted bytes, tracked reader closure and independent compiler copies tested; temporary alias and Windows path regressions fixed and reviewed |
| Capture consistency | Changed contents, inventory changes, disappearance and incomplete reads rejected without retry | Folder regressions pass at `3e72f27`; full hash checks plus final native directory identity verification independently reviewed |
| Native files | Canonical root; internal symlinks, junctions and special files rejected | Link/output transition checks reviewed at `3e72f27`; FIFO/socket acquisition transitions fixed and independently reviewed at `535bb6c` |
| Exclusions | Git metadata, generated output, selected result and Leafrun staging excluded | Capture exclusions reviewed; frozen publication guards and replaceable generated-result tests pass at `a864a64` |
| ZIP | Safe names, CRC, duplicates/collisions, stream completion, actual quotas | Standalone adapter passed both reviews and native CI; real core CLI example passes at `a864a64` |
| TAR.GZ | Safe names, links/special entries, tar checksums and complete gzip footer/stream | Standalone effective PAX/gzip checks reviewed; real core CLI example passes at `a864a64` |
| Git refs | Branch/tag/full commit, ambiguous or inaccessible refs, moving ref after capture | Standalone adapter reviewed at `e28c912`; fixed-commit core CLI example passes at `a864a64` |
| Git objects | No checkout/hooks/filters; submodule and LFS rejection | Standalone native object cases reviewed; core bridge captures accepted committed bytes |
| Private HTTPS | Local TLS server, ephemeral credentials, configured helper, auth failure and redaction | Real standalone native transport fixtures passed and were independently reviewed at `e28c912` |
| Private SSH | Native OpenSSH, ephemeral server/client keys and known hosts, auth/host-key failure | Real standalone OpenSSH fixtures passed and were independently reviewed at `e28c912` |
| Cancellation | Capture, Git descendants and compiler stop before owned cleanup | Capture and Git boundary fixes reviewed; real snapshot compiler cancellation passes at `a864a64`; render review pending |
| Shared renderer | Same nested Typst/JSON/SVG/font example from all four sources | Initial real Windows demonstration passed live folder, folder snapshot, ZIP, TAR.GZ and Git; final commit/review/native CI pending |
| Diagnostics | Snapshot-relative positions and traces; raw Typst output unchanged | New real diagnostic/trace tests pass at `a864a64`; independent review pending |
| Publication | Original sources/archives/fonts and hardlinks protected; failed cleanup/locked output preserves previous PDF | Native guard, archive replacement, local/bare/linked Git, cleanup-before-publication, cancellation and locked-output regressions pass at `a864a64`; independent review pending |
| Compatibility | Full M01 matrix and existing Generation API/tests | Full Core/CLI and unchanged Generation suites pass with pinned Windows tools and Linux race detector at `a864a64` |
| Native CI | Linux/Windows/macOS; mandatory tools, test/vet/build/demos; Linux race detector | Pending |

The full core tests and `go vet ./...` passed on Windows with the pinned tools. Independent review reproduced temporary-path alias redirection and Windows trailing-dot name normalization; `432e426` fixes both with permanent regression coverage. A subsequent review found inconsistent case aliases in the read-only Windows filesystem; `95d6993` rejects them consistently across read and stat operations. Both review stages approved the resulting contract. Focused snapshot tests and the CLI build were independently rerun successfully at `95d6993`.

The reviewed contract is published on [feature/lea-12-project-snapshots](https://github.com/Feruum/leafrun-document-core/tree/feature/lea-12-project-snapshots). [Initial native CI](https://github.com/Feruum/leafrun-document-core/actions/runs/38299654485) passed Linux, including the race detector, but failed Windows/macOS. [Diagnostic CI](https://github.com/Feruum/leafrun-document-core/actions/runs/38300157187) identified one failing test on both systems: font provenance compared a lexical temporary path with the intentionally canonical manifest path. Commit `11c0487` corrects the expectation and adds explicit alias provenance coverage without changing production behavior. [Native CI at that commit](https://github.com/Feruum/leafrun-document-core/actions/runs/38300522237) passed all three jobs: tests, vet, CLI build, real invoice render, and Linux race detector. LEA-13 remains In Progress until milestone delivery and acceptance.

Folder implementation `05ee9ac` passed the full local tests and vet, Linux/macOS test cross-compilation, and [native CI on all three systems](https://github.com/Feruum/leafrun-document-core/actions/runs/38302698358), including Linux race checks. The subsequent independent review and fixes are recorded below; LEA-14 remains In Progress until milestone delivery and acceptance.

Independent review found selected-result links could hide an ordinary resource and an included directory replacement could escape the final identity checks. Commit `39ee3f2` fixes both. Extended review then identified selected-result links introduced after their final directory enumeration, including inside the excluded output subtree. Commit `3e72f27` adds final ordinary-or-absent leaf validation without requiring stable excluded PDF bytes, timestamps or identities. Permanent regressions and the independent scratch overlay pass, as do full tests, vet and cross-compilation. Independent spec and code-quality reviews approved this commit. [Exact native CI](https://github.com/Feruum/leafrun-document-core/actions/runs/38306324269) passed all Linux, Windows and macOS jobs, including Linux race checks.

## Additional native Linux acquisition review

Follow-up read-only review at `e0c341a` found a bounded cancellation problem when an ordinary native input is replaced with a FIFO before opening. Independent race-enabled Linux probes reproduced blocking in both `snapshotNativeFS.Open` and `snapshotNativeFileInfo`; each pending open was explicitly released and its goroutine drained after the bounded observation. This finding also applies to original identity retention.

Commit `535bb6ceb2603cc4dd5b77f12bfa937fc51aef7d` uses nonblocking Unix acquisition through `os.Root`, validates the opened handle and closes it on failure. The generic metadata helper preserves ordinary-directory compatibility; retained file identities still require regular files and preserve native causes such as socket `ENXIO` alongside `ErrSourceChanged`. Permanent tests recorded bounded FIFO and socket RED to GREEN, with all pending readers released and drained.

Parent verification with Go 1.27.0 and Typst 0.15.1 passed the full native Windows suite (core 13.300 seconds, CLI 1.623 seconds), vet and CLI build. The implementer's full pinned Linux race suite, repeated native regressions, vet/build and macOS test compilation passed. [Exact native CI](https://github.com/Feruum/leafrun-document-core/actions/runs/38317757028) passed all three platforms, including real rendering and Linux race checks.

Fresh spec review independently reproduced the bounded failures against the base plus new tests, then approved the exact fix after pinned Linux race and Windows compatibility checks. Fresh quality review approved the same head after checking cleanup, native causes, containment and test reliability. Its independent descriptor checks remained 8 to 8 across 512 rejection/compatibility cycles and 7 to 7 across three concurrent FIFO exchange runs; pinned Linux race and Windows compatibility checks passed. The original timing-based concurrent probe did not observe the race and skipped; the stronger permanent exchange test and three additional descriptor-accounted runs passed. These results cover this acquisition fix, not the pending Git/shared-render milestone.

## Shared rendering implementation

Commit `a864a64cd2f68ec612da97dc2ae8fd18f892fd77` implements `RenderSnapshot`, `RenderSource`, eager original publication guards, logical structured diagnostic paths and the project/archive/Git CLI bridge. A borrowed snapshot remains caller-owned; the source wrapper releases its owned snapshot and working inputs before atomic publication. Mandatory cleanup failures retain their causes and preserve the old PDF.

Permanent RED-to-GREEN regressions covered Windows extended dependency prefixes, native relative external font directories, original hardlink publication and legacy plain-file protection. Additional real provider checks cover replacing an accepted archive, bare/local/linked Git metadata prefixes and hardlinks, frozen fonts, concurrent snapshot close, compiler cancellation, required working/snapshot cleanup and locked output.

The final full worker verification ran after all production fixes with Go 1.27.0 and Typst 0.15.1. Native Windows Core/command/internal CLI tests passed in 23.144/36.558/2.389 seconds, followed by vet and CLI build. Pinned Linux race tests passed in 44.681/15.482/3.909 seconds, followed by vet and CLI build. Generation's unchanged tests and vet passed on Windows (4.635 seconds) and under Linux race checks (9.480 seconds). Core, command and internal CLI tests cross-compiled for Darwin ARM64. Every final check process exited 0.

Parent independently ran the five-source Windows demonstration at this exact commit: live folder, folder snapshot, ZIP, TAR.GZ and Git all published PDFs, followed by invalid-source diagnostics, exit 1, empty stdout and an unchanged prior PDF digest. Independent full checks and spec/quality reviews precede the final native three-platform delivery gate.

Parent also independently reran the complete pinned Linux race checks at that exact commit: Core/command/internal CLI passed in 50.252/24.002/9.319 seconds, with vet and CLI build succeeding. Unchanged Generation race tests passed in 8.803 seconds, followed by vet. The read-only offline workspace verification process exited 0.

Independent spec review then reproduced a frozen directory-guard path-association gap: replacing an accepted protected directory with a junction allowed a requested output under that original prefix to resolve elsewhere and pass publication checks. This required retaining the requested path association alongside canonical publication and hardlink identity checks before acceptance.

Scoped fix `2ec82ba069c27c0c46debbc50820c9b273658a68` retains the requested original output association separately from the resolved destination. Both participate in frozen file/prefix and private-storage checks; native hardlink checks remain intact. Four permanent RED-to-GREEN cases cover directory, file, project-resource and external-font association after parent retargeting. An additional Windows extended-path spelling regression also passed after equivalent native spelling normalization. The old target PDF remains intact and the accepted snapshot remains usable.

All final fix checks followed the production change and exited 0: Windows Core/command/internal CLI tests 19.717/28.163/1.988 seconds, vet and CLI build; pinned Linux race tests 37.768/12.871/3.717 seconds, vet and build; unchanged Generation Windows tests 4.059 seconds and Linux race tests 8.196 seconds, both with vet. Darwin ARM64 tests cross-compiled.

The same spec reviewer approved `2ec82ba` after the unchanged original junction reproduction rejected publication, returned an empty PDF path and created no target PDF. Its fresh focused render/lifecycle/guard checks passed in 8.742 seconds, including Windows extended paths. The complete remaining API, ownership, diagnostic, CLI and module-boundary audit found no other concrete gaps. Fresh independent quality review follows that approval.

Parent independently repeated the full pinned Linux checks at `2ec82ba`: Core/command/internal CLI race tests passed in 32.239/11.432/3.125 seconds, followed by vet and CLI build; unchanged Generation race tests passed in 6.524 seconds and vet succeeded. The process exited 0. The exact-commit five-source Windows demonstration also passed again, including invalid-source diagnostics and preservation of the previous PDF.

Before the shared-render refactor, the unchanged Generation feature at `211ce80ab5e3499d0ce8399d6761a46dabaf0e21` passed its full Windows compatibility suite (8.599 seconds) and vet through the external M02 workspace. Independent native Linux verification with Go 1.27.0 and Typst 0.15.1 passed the full race-enabled suite (10.546 seconds) and vet. These baselines precede the post-refactor checks recorded above. Generation's repository and module path remain unchanged.

The parent source-demonstration script successfully prepared isolated ZIP, TAR.GZ and local Git fixtures and rendered the live folder with the real pinned engine. Its pre-LEA-17 run then failed exactly at the unsupported `--snapshot` flag. The same demonstration passed all sources after implementation, as recorded above.

During LEA-17 development, the parent reran the real Windows demonstration against stable implementation inputs with Go 1.27.0, Typst 0.15.1 and native Git. Live folder, accepted folder snapshot, ZIP, TAR.GZ and a fixed local Git commit all published PDFs; invalid snapshot input returned diagnostics and exit 1 with empty stdout while preserving the previous PDF's SHA-256. The first attempt exposed a fixture mismatch: .NET's convenience TAR writer emitted `./`-prefixed PAX metadata names rejected by the approved portable path contract. The script now writes explicit clean USTAR entries; production archive validation was unchanged. Repeat this demonstration at the final reviewed head and on native CI.

## Delivery gates

Keep this feature in its own branch while reviews are pending. Prepare related draft pull requests into upstream `develop` only when that branch exists. Do not mark Linear Done, merge or release based solely on local verification. Releasing integrations `v0.1.0`, pinning that tag, checking core with `GOWORK=off` and accepting M01 precede the M02 core release.
