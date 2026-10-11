# leafrun-document-core

## Read and render a project consistently

This planned shared library will understand a Typst project: files, entry point, parameters, render settings, and diagnostics. The first step is to extract reusable document logic from the working Go renderer.

Generation uses it on the server. Web and VS Code should use compatible native or WASM builds where practical. The current code still lives in leafrun-generation.

**Status:** this repository is planned and has no runnable implementation yet. For code you can try now, see the [local prototypes](https://github.com/leafrun/leafrun/blob/main/docs/repositories.md).

**First roadmap step:** [LEA-5 — Extract the minimal render core](https://linear.app/bulnik/issue/LEA-5/e01-minimalnoe-yadro-rendera). Linear may require workspace access; the public [roadmap](https://github.com/leafrun/leafrun/blob/main/docs/roadmap.md) explains the build order. See the [repository map](https://github.com/leafrun/leafrun/blob/main/docs/repositories.md) and [contribution guide](https://github.com/leafrun/leafrun/blob/main/CONTRIBUTING.md).

No project license has been selected yet.
