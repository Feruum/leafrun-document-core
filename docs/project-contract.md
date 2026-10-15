# Project manifest and target descriptors

Core reads a project's `leafrun.json` from a logical `fs.FS`, checks every
referenced entrypoint and schema, and resolves a target against a verified
snapshot. It does not compile Typst or execute bundle steps.

Pass an immutable captured filesystem, such as `Snapshot.FS()`, to descriptor
resolution. The supplied manifest must match its captured `leafrun.json`;
descriptor resolution rejects a modified manifest even when it is otherwise
technically valid. Mutable working folders must be captured first.

The version 1 shape is the documented example in
[`testdata/project-contract/leafrun.json`](../testdata/project-contract/leafrun.json).
The fixture also contains its three Typst templates and the shared Draft
2020-12 schema. Unknown fields are rejected at every manifest level. JSON
duplicate keys, invalid Unicode, null where a typed value is required,
unsupported format versions, native paths, and traversal paths fail before a
descriptor is returned.

`ProjectValidationError` carries `File`, an RFC 6901 `Pointer`, and a stable
`Code`. A pointer names the invalid manifest field or the invalid schema
location. For example, a missing template entrypoint reports
`leafrun.json#/templates/0/entrypoint` with code `missing_resource`.

## Templates and targets

Each template has a unique ID, a regular-file `entrypoint`, and a regular-file
`inputSchema`. Targets have a separate unique-ID namespace:

- A `document` target references one template and produces a logical PDF path.
  Its schema inherits the template schema when omitted. If provided, it must
  be the same logical path as the template schema.
- A `bundle` target references an input schema, an ordered list of unique
  steps, and a logical ZIP output path. Each step references a template and
  has an RFC 6901 `inputPointer`. Optional `dependsOn` IDs define a DAG, and
  `order` must include each step exactly once with dependencies before their
  dependants. Core describes these steps; bundle execution belongs to E07.

Output paths use slash-separated project-relative syntax and cannot name a
source file or directory. Every template entrypoint and schema must resolve to
a regular project file. Draft 2020-12 schema references resolve only within
the immutable `leafrun://snapshot/` namespace. Schema resources are capped at
4 MiB total, and Typst is never invoked during validation.

`SchemaResources` returns only a resolved input schema and its reachable local
schema resources. It excludes unrelated project files and returns independent
copies while preserving schema number lexemes, so a form client can expose the
validated contract without returning the project tree.

## Profiles and digests

Profiles are Leafrun metadata named by keys such as `draft` and `production`;
they are not Typst mode flags. `defaultProfile` must name a defined profile.
`inputs` contains bounded string values passed later as template input keys.
The key `leafrun-data` is reserved to Core. `pdfStandard` accepts the
comma-separated values supported by the pinned Typst 0.15.1 CLI: `1.4`, `1.5`,
`1.6`, `1.7`, `2.0`, `a-1b`, `a-1a`, `a-2b`, `a-2u`, `a-2a`, `a-3b`, `a-3u`,
`a-3a`, `a-4`, `a-4f`, `a-4e`, and `ua-1`.

`DescribeTarget` resolves an omitted profile to `defaultProfile`. Its base
`Digest` binds the target, resolved schema path, every profile definition and
the default choice, Typst version, project resource hashes, captured font
hashes, and snapshot content ID. `ExecutionDigest` additionally binds the
selected profile. Changing the selected profile therefore leaves the base
descriptor digest unchanged while producing a different execution digest.
Descriptor slices and maps are copied before return.

## Parameters, defaults, and file inputs

`PrepareParameters` accepts a JSON object, the selected schema path, and
caller-authorized file references. It uses the strict shared JSON codec, keeps
number lexemes intact, and returns canonical JSON plus Core-owned `sys.inputs`.
The request and prepared JSON are each limited to 1 MiB; schema resources are
limited to 4 MiB total, and nested values are bounded.

Defaults are copied only into absent properties. Explicit `null`, `false`,
zero, and empty values remain as supplied. Defaults apply inside existing
objects and array items; a missing object is created only when that property
has an explicit object default. Tuple defaults do not extend arrays. Defaults
from local `$ref` and `allOf` schemas are supported when they agree. Invalid,
conflicting, conditional, union-branch, or unbounded recursive defaults fail
technical schema validation. `VerifyPreparedInput` validates persisted
canonical JSON and confirms that required defaults are already present; it
never applies defaults again. Expansion reserves the encoded byte budget before
copying each default, and shared schema references are checked once per combined
schema group.

Schemas may mark string fields with `x-leafrun-file: true`. Those fields accept
only a bound file reference; callers cannot supply arbitrary paths or default
file paths. Each reference binds a JSON Pointer, opaque resource ID, safe
filename, byte size, and lowercase SHA-256. Core replaces the field with a
stable path of the form
`.leafrun-input/<sha256><safe-extension>`. A project cannot already own the
reserved `.leafrun-input/` root. At most 64 references and 25 MiB across unique
generated paths are allowed. The trusted adapter opens only authorized frozen
resources; Core checks exact size and digest before copying bytes.

The physical descriptor path is logical and relative to the snapshot root.
Prepared JSON prefixes it with `/`, so Typst resolves the file from the project
root even when a template lives in a subdirectory. Array file fields use an
explicit `null` slot with a bound fileRef; arrays are never extended and caller
path strings are rejected. Numeric pointer tokens follow the actual object or
array shape. Object and array defaults containing file-marked descendants are
rejected during technical validation.

Defaults and file fields must have static applicability through ordinary local
references and `allOf`. Annotations under unions, conditionals, `contains` and
unevaluated branches fail technical validation. Dynamic/recursive references
cannot be combined with reachable defaults or file fields in this version;
pure dynamic validation remains supported. Unreferenced definitions do not
participate in preparation. Parameter failures identify the affected field with
an escaped JSON Pointer and a bounded message without parameter values.

`CreatePreparedSnapshot` writes the exact prepared bytes as `input.json` and
the verified files into an independent snapshot. Captured project and external
font trees, font order, and provenance remain intact, even if the source is
later closed or removed. Concurrent preparations and snapshots own independent
copies. Core reserves `leafrun-data` as `/input.json`; a selected profile may
add up to 64 bounded string inputs, which are passed as sorted Typst
`--input key=value` arguments. Supported PDF standards are passed using the
verified Typst 0.15.1 `--pdf-standard` option. Omitting both settings preserves
legacy rendering behavior.

```go
manifest, err := documentcore.ReadProject(ctx, projectFS)
if err != nil {
	return err
}

descriptor, err := documentcore.DescribeTarget(
	ctx, projectFS, manifest, snapshotManifest, "invoice", "production",
)
```

The snapshot manifest's `ID` is the Core content ID. Storage snapshot IDs and
project revisions remain separate Projects-owned values.
