# leafrun-document-core

Planned shared Typst document core for native and WASM environments.

**Status: planned component.** This repository currently contains its scope and roadmap links. It does not yet provide a runnable service or package. The existing local rendering prototype lives in [leafrun-generation](https://github.com/leafrun/leafrun-generation).

## Responsibility

Project/VFS/manifest/input/profile logic, Typst and diagnostics; native and WASM artifacts.

## Boundaries

This is a versioned library, not a separately deployed service. Consumers pin compatible versions; domain rules and database ownership remain with their owning services.

The initial Go `Render(path)` implementation remains in `leafrun-generation`. Extracting shared native/WASM document logic is roadmap work; this scaffold does not perform that extraction.

## Roadmap

- [LEA-5 — Minimal rendering core](https://linear.app/bulnik/issue/LEA-5/e01-minimalnoe-yadro-rendera)
- [LEA-12 — Project sources and immutable snapshots](https://linear.app/bulnik/issue/LEA-12/e02-istochniki-proekta-i-neizmenyaemye-snimki)
- [LEA-33 — Project contracts, parameters and releases](https://linear.app/bulnik/issue/LEA-33/e05-kontrakt-proekta-parametry-i-relizy)
- [LEA-58 — Shared WASM core and web editor](https://linear.app/bulnik/issue/LEA-58/e08-obshij-wasm-i-web-redaktor)
- [LEA-92 — RichText, history, comments and proposals](https://linear.app/bulnik/issue/LEA-92/e12-richtext-istoriya-kommentarii-i-predlozheniya)
- [LEA-122 — Future capabilities: discovery](https://linear.app/bulnik/issue/LEA-122/e16-dalnejshie-vozmozhnosti-issledovanie)

See the [development roadmap](https://github.com/leafrun/leafrun/blob/main/docs/roadmap.md) for dependencies and acceptance milestones. Creating this repository does not complete its implementation issues.

## Contributing

Use English for code comments, documentation, examples, commit messages and pull requests. See the [contribution guide](https://github.com/leafrun/leafrun/blob/main/CONTRIBUTING.md) and [current architecture](https://github.com/leafrun/leafrun/blob/main/docs/architecture.md).

## License status

A project license has not been selected. No license is assigned by this repository scaffold.
