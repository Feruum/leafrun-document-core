# M02 verification

Implementation, per-task spec/quality reviews and the final cumulative review are complete. Native verification passed on Linux, Windows and macOS. The features are ready for user review; merge, release and Linear acceptance remain separate gates.

## Reviewed inputs

| Component | Verified revision | Purpose |
| --- | --- | --- |
| Core M01 baseline | `d7709509dfe1f9ab0484b5dc342b09a15c8da45a` | Existing live-folder render contract |
| Core M02 production | `2ec82ba069c27c0c46debbc50820c9b273658a68` | Snapshot capture, shared renderer and CLI |
| Core paired CI | `1481587058ea2f84515e22b1b4226634773b09eb` | Documentation, source demonstration and exact-adapter CI over that production revision |
| Integrations | `a4414f5fe3f12815d305d14edba5396a97e5738b` | Documentation over reviewed archive/Git implementation `e28c912` |
| Generation compatibility | `211ce80ab5e3499d0ce8399d6761a46dabaf0e21` | Unchanged legacy module, `Render(string)` and `document.pdf` |

Checks used Go **1.27.0** and Typst **0.15.1**. Git and OpenSSH are mandatory native tools. SSH fixtures pin `golang.org/x/crypto v0.58.0`; Windows process control pins `golang.org/x/sys v0.49.0`.

## Acceptance evidence

| Area | Verified behavior |
| --- | --- |
| Manifest | Sorted file sizes/SHA-256, provenance-independent content ID, ordered fonts and deep copies |
| Snapshot lifetime | Immutable accepted bytes, read-only filesystem, idempotent close, closed-snapshot rejection and independent compiler copies |
| Folder capture | Full before/after inventories and content hashes; same-size/same-time mutations, disappearance and incomplete reads return `ErrSourceChanged` without retries |
| Native inputs | Canonical selected root; internal links, junctions and special files rejected; bounded FIFO/socket acquisition and retained identity cleanup |
| Exclusions | Git metadata, root output, selected regular result and ordinary Leafrun staging excluded without hiding included resources |
| ZIP/TAR.GZ | Portable names, regular entry types, duplicates/case collisions, actual byte quotas, complete ZIP CRC/tar/PAX/gzip checks and owned cleanup |
| Git | Required unambiguous ref resolves once to a full commit; raw objects without checkout/hooks/filters; explicit link/submodule/LFS rejection |
| HTTPS/SSH | Real local TLS/OpenSSH fixtures, ephemeral credentials, configured helpers, strict trust checks, auth failures and sanitized native error causes |
| Process lifetime | Original and late-spawned owned Git descendants exit before cleanup; real compiler cancellation and capture cancellation release owned inputs |
| Shared renderer | The same nested Typst/JSON/SVG/font example works from live folder, folder snapshot, ZIP, TAR.GZ and Git |
| Fonts | Captured font bytes, native local paths and search order retained; later external font changes do not modify the accepted snapshot |
| Diagnostics | Relative structured positions/trace paths, original lines/columns/hints/order and unchanged raw engine output |
| Publication | Frozen original path associations, directory prefixes and file/hardlink identities protected after input removal or replacement |
| Failure behavior | Preparation, compilation, cancellation, mandatory cleanup and locked-output failures preserve the previous PDF and return no new PDF path |
| Compatibility | Full M01 Core/CLI matrix and unchanged Generation tests, legacy output and wrapped native process errors |

Each task passed independent spec and quality review. Capture reviews approved `535bb6c`, archive reviews approved `436baae`, Git reviews approved `e28c912`, and renderer reviews approved `2ec82ba`. Review findings were reproduced and fixed with permanent regression tests before approval. The final renderer fix checks both the requested original output association and canonical destination, including equivalent Windows extended-path spelling after directory retargeting.

## Native CI

- [Core paired CI at `1481587`](https://github.com/Feruum/leafrun-document-core/actions/runs/38435873702) passed all three native jobs in 3 minutes 39 seconds: tests, vet, CLI build, all-source demonstrations and Linux race checks.
- [Integrations CI at `a4414f5`](https://github.com/Feruum/leafrun-integrations/actions/runs/38432001004) passed all three native jobs in 2 minutes 17 seconds: real Git/OpenSSH tests, vet, build and Linux race checks.

Every Core job summary records Go 1.27.0, Typst 0.15.1 and successful PDFs from all five CLI modes. A subsequent invalid folder snapshot returns exit 1 and preserves the previous PDF. Missing mandatory tools fail verification. CI checks out the exact adapter commit with credential persistence disabled and creates its paired workspace outside both repositories.

## Independent local checks

All commands below ran after the final production change and exited 0:

| Check | Windows | Pinned Linux |
| --- | --- | --- |
| Core / command / internal CLI tests | 19.717 / 28.163 / 1.988 seconds | Race tests: 37.768 / 12.871 / 3.717 seconds |
| Core vet and CLI build | Passed | Passed |
| Unchanged Generation tests and vet | 4.059 seconds; vet passed | Race tests: 8.196 seconds; vet passed |

Parent verification independently repeated the complete Linux race suite: Core/command/internal CLI 32.239/11.432/3.125 seconds, followed by vet and CLI build; unchanged Generation 6.524 seconds and vet. The workspace was mounted read-only, outbound network disabled, and the exact Go 1.27.0 toolchain supplied separately. Darwin ARM64 test binaries also cross-compiled before native macOS CI passed.

The exact-production Windows demonstration independently rendered every source, checked PDF signatures and stdout paths, then verified invalid-source diagnostics, empty stdout and an unchanged prior PDF SHA-256. Successful PDFs are compared by format and behavior, not by byte equality.

Fresh renderer quality review independently passed native Windows lifecycle/protection tests (6.996 seconds), adversarial cleanup/cancellation probes (1.195 seconds), extended-path regression (1.065 seconds), CLI checks (1.325 seconds), and archive/Git protection with caller-CWD checks (16.760 seconds). The production checkout remained unchanged during those probes.

The fresh final cumulative review approved exact Core `1481587`, Integrations `a4414f5` and unchanged Generation `211ce80`, with no actionable findings. It independently passed native Core/command/internal CLI checks in 7.251/27.780/1.320 seconds, archive and Windows process-lifetime regressions, and two additional Git cases in 53.002 seconds. Those Git fixtures used a review-only long-path configuration because their assigned temporary directory was unusually long; user and repository Git configuration were unchanged. No production files were edited during the review.

## Delivery gates and limits

Published features are [Core](https://github.com/Feruum/leafrun-document-core/tree/feature/lea-12-project-snapshots) and [Integrations](https://github.com/Feruum/leafrun-integrations/tree/feature/lea-12-source-adapters). The Core feature is stacked on M01. Prepared draft PRs target upstream `develop`; both upstream repositories currently expose only `main`, so the authorized fallback retains the feature branches and prepared descriptions.

Development and CI use an external workspace with exact feature checkouts and version-specific overrides for planned, unpublished module metadata. No `go.work` or local `go.mod` replacement is delivered. After user review, release integrations `v0.1.0`, pin that released dependency and verify Core with `GOWORK=off`. M01 delivery/acceptance precedes Core `v0.2.0`. No merge, tag, release or Linear Done is implied by these checks.

Folder consistency uses optimistic detection rather than an atomic filesystem snapshot. Accepted bytes are immutable; system fonts, environment freezing and deterministic PDF bytes are outside M02. Linux Git process exit watches require kernel 5.3+ pidfds. Private transport tests use local fixtures; account access to an arbitrary hosted repository still depends on the user's configured credentials and permissions.
