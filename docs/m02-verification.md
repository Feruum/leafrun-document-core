# M02 verification

Status: implementation in progress. This file records evidence after checks run; pending rows are not acceptance claims.

## Environment and baseline

On 2026-10-12, the isolated core feature worktree started at M01 `d7709509dfe1f9ab0484b5dc342b09a15c8da45a`. Its unchanged baseline `go test ./... -count=1` passed with Go 1.27.0 and Typst 0.15.1 on Windows. The integrations checkout retains all three upstream commits through `a6492ffb81166d2515ac78e2cf04e9425022aebb`.

## Acceptance evidence

| Area | Required behavior | Evidence |
| --- | --- | --- |
| Manifest | Sorted file hashes, deep copies, origin-independent content ID, ordered fonts | Snapshot tests passed at `95d6993`; independent spec and quality reviews approved |
| Snapshot lifetime | Immutable accepted bytes, read-only access, idempotent Close, closed-snapshot rejection | Accepted bytes, tracked reader closure and independent compiler copies tested; temporary alias and Windows path regressions fixed and reviewed |
| Capture consistency | Changed contents, inventory changes, disappearance and incomplete reads rejected without retry | Folder mutation, replacement, retention-window disappearance and incomplete-read regressions pass at `05ee9ac`; independent review in progress |
| Native files | Canonical root; internal symlinks, junctions and special files rejected | Pinned native root/file/directory checks and local link/special-file tests pass; review checking selected-result link exclusions |
| Exclusions | Git metadata, generated output, selected result and Leafrun staging excluded | Deterministic folder exclusions and ignored-output churn checks pass at `05ee9ac`; excluded-result guard metadata prepared for LEA-17 |
| ZIP | Safe names, CRC, duplicates/collisions, stream completion, actual quotas | Pending |
| TAR.GZ | Safe names, links/special entries, tar checksums and complete gzip footer/stream | Pending |
| Git refs | Branch/tag/full commit, ambiguous or inaccessible refs, moving ref after capture | Pending |
| Git objects | No checkout/hooks/filters; submodule and LFS rejection | Pending |
| Private HTTPS | Local TLS server, ephemeral credentials, configured helper, auth failure and redaction | Pending |
| Private SSH | Native OpenSSH, ephemeral server/client keys and known hosts, auth/host-key failure | Pending |
| Cancellation | Capture, Git descendants and compiler stop before owned cleanup | Pending |
| Shared renderer | Same nested Typst/JSON/SVG/font example from all four sources | Pending |
| Diagnostics | Snapshot-relative positions and traces; raw Typst output unchanged | Pending |
| Publication | Original sources/archives/fonts and hardlinks protected; failed cleanup/locked output preserves previous PDF | Pending |
| Compatibility | Full M01 matrix and existing Generation API/tests | Pending |
| Native CI | Linux/Windows/macOS; mandatory tools, test/vet/build/demos; Linux race detector | Pending |

The full core tests and `go vet ./...` passed on Windows with the pinned tools. Independent review reproduced temporary-path alias redirection and Windows trailing-dot name normalization; `432e426` fixes both with permanent regression coverage. A subsequent review found inconsistent case aliases in the read-only Windows filesystem; `95d6993` rejects them consistently across read and stat operations. Both review stages approved the resulting contract. Focused snapshot tests and the CLI build were independently rerun successfully at `95d6993`.

The reviewed contract is published on [feature/lea-12-project-snapshots](https://github.com/Feruum/leafrun-document-core/tree/feature/lea-12-project-snapshots). [Initial native CI](https://github.com/Feruum/leafrun-document-core/actions/runs/38299654485) passed Linux, including the race detector, but failed Windows/macOS. [Diagnostic CI](https://github.com/Feruum/leafrun-document-core/actions/runs/38300157187) identified one failing test on both systems: font provenance compared a lexical temporary path with the intentionally canonical manifest path. Commit `11c0487` corrects the expectation and adds explicit alias provenance coverage without changing production behavior. [Native CI at that commit](https://github.com/Feruum/leafrun-document-core/actions/runs/38300522237) passed all three jobs: tests, vet, CLI build, real invoice render, and Linux race detector. LEA-13 remains In Progress until milestone delivery and acceptance.

Folder implementation `05ee9ac` passed the full local tests and vet, Linux/macOS test cross-compilation, and [native CI on all three systems](https://github.com/Feruum/leafrun-document-core/actions/runs/38302698358), including Linux race checks. Independent review is still in progress; LEA-14 is not yet accepted or delivered.

## Delivery gates

Keep this feature in its own branch while reviews are pending. Prepare related draft pull requests into upstream `develop` only when that branch exists. Do not mark Linear Done, merge or release based solely on local verification. Releasing integrations `v0.1.0`, pinning that tag, checking core with `GOWORK=off` and accepting M01 precede the M02 core release.
