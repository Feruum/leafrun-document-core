package documentcore

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
)

func TestReadProjectDocumentedProjectContract(t *testing.T) {
	projectFS := projectFixtureFS(t)
	manifest, err := ReadProject(context.Background(), projectFS)
	if err != nil {
		t.Fatalf("ReadProject() error = %v", err)
	}
	if manifest.FormatVersion != 1 || manifest.Name != "sales-documents" || manifest.Engine.TypstVersion != "0.15.1" {
		t.Fatalf("unexpected project identity: %#v", manifest)
	}
	if len(manifest.Templates) != 3 || len(manifest.Targets) != 2 || manifest.DefaultProfile != "production" {
		t.Fatalf("documented templates, targets, and default profile were not read: %#v", manifest)
	}
}

func TestReadProjectAllowsLargeRegularTemplateEntrypoint(t *testing.T) {
	files := mutableProjectFixtureFS(t)
	(*files)["templates/invoice.typ"] = &fstest.MapFile{Data: []byte(strings.Repeat("x", int(ProjectManifestLimit)+1))}
	if _, err := ReadProject(context.Background(), files); err != nil {
		t.Fatalf("ReadProject() rejected a regular template entrypoint larger than the manifest limit: %v", err)
	}
}

func TestReadProjectReportsStrictManifestErrors(t *testing.T) {
	tests := []struct {
		name        string
		mutate      func(*testing.T, *fstest.MapFS)
		wantCode    string
		wantPointer string
	}{
		{
			name: "unknown field",
			mutate: func(t *testing.T, files *fstest.MapFS) {
				editManifest(t, files, `"entrypoint": "templates/invoice.typ"`, `"entrypoint": "templates/invoice.typ", "credentials": {}`)
			},
			wantCode:    "unknown_field",
			wantPointer: "/templates/0/credentials",
		},
		{
			name: "duplicate key",
			mutate: func(t *testing.T, files *fstest.MapFS) {
				editManifest(t, files, `"name": "sales-documents"`, `"name": "sales-documents", "name": "other"`)
			},
			wantCode:    "duplicate_key",
			wantPointer: "",
		},
		{
			name: "malformed unicode",
			mutate: func(t *testing.T, files *fstest.MapFS) {
				(*files)["leafrun.json"] = &fstest.MapFile{Data: []byte(`{"formatVersion":1,"name":"\ud800"}`)}
			},
			wantCode:    "invalid_unicode",
			wantPointer: "",
		},
		{
			name: "unsupported version",
			mutate: func(t *testing.T, files *fstest.MapFS) {
				editManifest(t, files, `"formatVersion": 1`, `"formatVersion": 2`)
			},
			wantCode:    "unsupported_version",
			wantPointer: "/formatVersion",
		},
		{
			name:        "duplicate template id",
			mutate:      func(t *testing.T, files *fstest.MapFS) { editManifest(t, files, `"id": "contract"`, `"id": "invoice"`) },
			wantCode:    "duplicate_id",
			wantPointer: "/templates/1/id",
		},
		{
			name:        "missing entrypoint",
			mutate:      func(t *testing.T, files *fstest.MapFS) { deleteProjectFile(t, files, "templates/invoice.typ") },
			wantCode:    "missing_resource",
			wantPointer: "/templates/0/entrypoint",
		},
		{
			name: "traversal path",
			mutate: func(t *testing.T, files *fstest.MapFS) {
				editManifest(t, files, "templates/invoice.typ", "../templates/invoice.typ")
			},
			wantCode:    "invalid_logical_path",
			wantPointer: "/templates/0/entrypoint",
		},
		{
			name: "native separator path",
			mutate: func(t *testing.T, files *fstest.MapFS) {
				editManifest(t, files, "templates/invoice.typ", `templates\\invoice.typ`)
			},
			wantCode:    "invalid_logical_path",
			wantPointer: "/templates/0/entrypoint",
		},
		{
			name: "reserved profile input",
			mutate: func(t *testing.T, files *fstest.MapFS) {
				editManifest(t, files, `"production": {}`, `"production": { "inputs": { "leafrun-data": "override" } }`)
			},
			wantCode: "reserved_profile_input", wantPointer: "/profiles/production/inputs/leafrun-data",
		},
		{
			name: "unsupported PDF standard",
			mutate: func(t *testing.T, files *fstest.MapFS) {
				editManifest(t, files, `"production": {}`, `"production": { "pdfStandard": "pdf-9" }`)
			},
			wantCode: "unsupported_pdf_standard", wantPointer: "/profiles/production/pdfStandard",
		},
		{
			name: "credentials field",
			mutate: func(t *testing.T, files *fstest.MapFS) {
				editManifest(t, files, `"name": "sales-documents"`, `"name": "sales-documents", "acl": {}`)
			},
			wantCode:    "unknown_field",
			wantPointer: "/acl",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			files := mutableProjectFixtureFS(t)
			test.mutate(t, files)
			_, err := ReadProject(context.Background(), files)
			if err == nil {
				t.Fatal("ReadProject() succeeded for invalid manifest")
			}
			var projectErr *ProjectValidationError
			if !errors.As(err, &projectErr) {
				t.Fatalf("ReadProject() error %T does not expose a project diagnostic: %v", err, err)
			}
			if projectErr.File != "leafrun.json" || projectErr.Code != test.wantCode || projectErr.Pointer != test.wantPointer {
				t.Fatalf("diagnostic = (%q, %q, %q), want file leafrun.json, code %q, pointer %q", projectErr.File, projectErr.Pointer, projectErr.Code, test.wantCode, test.wantPointer)
			}
		})
	}
}

func TestDescribeTargetInheritsSchemaAndFreezesProfiles(t *testing.T) {
	files := mutableProjectFixtureFS(t)
	editManifest(t, files, `"profiles": { "draft": {}, "production": {} },`, `"profiles": { "draft": { "inputs": { "watermark": "DRAFT" } }, "production": { "pdfStandard": "a-2b" } },`)
	manifest, err := ReadProject(context.Background(), files)
	if err != nil {
		t.Fatal(err)
	}
	snapshot := projectSnapshotManifest(t, files)
	first, err := DescribeTarget(context.Background(), files, manifest, snapshot, "invoice", "")
	if err != nil {
		t.Fatalf("DescribeTarget() error = %v", err)
	}
	if first.InputSchema != "schemas/sales.schema.json" || first.Entrypoint != "templates/invoice.typ" {
		t.Fatalf("target did not inherit the template schema and entrypoint: %#v", first)
	}
	if first.SelectedProfile != "production" || first.ResolvedProfile.Name != "production" {
		t.Fatalf("default profile was not resolved: %#v", first)
	}
	if first.ResolvedProfile.PDFStandard != "a-2b" {
		t.Fatalf("selected profile PDF standard = %q, want a-2b", first.ResolvedProfile.PDFStandard)
	}
	if first.SnapshotContentID != snapshot.ID || first.Digest == "" || first.ExecutionDigest == "" {
		t.Fatalf("descriptor is missing immutable content identity or digests: %#v", first)
	}

	first.Profiles["draft"].Inputs["watermark"] = "mutated"
	first.Steps = append(first.Steps, TargetStepDescriptor{ID: "caller-edit"})
	second, err := DescribeTarget(context.Background(), files, manifest, snapshot, "invoice", "production")
	if err != nil {
		t.Fatal(err)
	}
	if value := second.Profiles["draft"].Inputs["watermark"]; value != "DRAFT" || len(second.Steps) != 0 {
		t.Fatalf("descriptor data aliases the earlier caller result: %#v", second)
	}
	draft, err := DescribeTarget(context.Background(), files, manifest, snapshot, "invoice", "draft")
	if err != nil {
		t.Fatal(err)
	}
	if draft.Digest != second.Digest || draft.ExecutionDigest == second.ExecutionDigest {
		t.Fatalf("base and selected-profile digests are not separated: default=%#v draft=%#v", second, draft)
	}
}

func TestDescribeTargetRequiresManifestBoundToSnapshot(t *testing.T) {
	files := mutableProjectFixtureFS(t)
	manifest, err := ReadProject(context.Background(), files)
	if err != nil {
		t.Fatal(err)
	}
	snapshot := projectSnapshotManifest(t, files)

	// Derived profile names and empty maps do not alter manifest semantics.
	equivalent := cloneProjectManifest(manifest)
	profile := equivalent.Profiles["production"]
	profile.Name = "not-persisted"
	profile.Inputs = map[string]string{}
	equivalent.Profiles["production"] = profile
	if _, err := DescribeTarget(context.Background(), files, equivalent, snapshot, "invoice", "production"); err != nil {
		t.Fatalf("DescribeTarget() rejected a semantically equivalent manifest copy: %v", err)
	}

	tests := []struct {
		name   string
		mutate func(*ProjectManifest)
	}{
		{
			name: "profile",
			mutate: func(candidate *ProjectManifest) {
				candidate.Profiles["custom"] = ProjectProfile{}
			},
		},
		{
			name: "template",
			mutate: func(candidate *ProjectManifest) {
				candidate.Templates[0].Entrypoint = "templates/contract.typ"
			},
		},
		{
			name: "output",
			mutate: func(candidate *ProjectManifest) {
				candidate.Targets[0].Output.Path = "output/other.pdf"
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			candidate := cloneProjectManifest(manifest)
			test.mutate(&candidate)
			_, err := DescribeTarget(context.Background(), files, candidate, snapshot, "invoice", "production")
			assertProjectDiagnostic(t, err, "manifest_mismatch", "")
		})
	}
}

func TestDescribeTargetRejectsInvalidSelectionAndSchemaConflict(t *testing.T) {
	files := mutableProjectFixtureFS(t)
	manifest, err := ReadProject(context.Background(), files)
	if err != nil {
		t.Fatal(err)
	}
	snapshot := projectSnapshotManifest(t, files)

	_, err = DescribeTarget(context.Background(), files, manifest, snapshot, "absent", "production")
	assertProjectDiagnostic(t, err, "target_not_found", "/targets/absent")
	_, err = DescribeTarget(context.Background(), files, manifest, snapshot, "invoice", "unknown")
	assertProjectDiagnostic(t, err, "profile_not_found", "/profiles/unknown")

	editManifest(t, files, `"template": "invoice",
      "inputSchema": "schemas/sales.schema.json",`, `"template": "invoice",
      "inputSchema": "schemas/other.schema.json",`)
	_, err = ReadProject(context.Background(), files)
	assertProjectDiagnostic(t, err, "schema_conflict", "/targets/0/inputSchema")
}

func TestReadProjectValidatesBundleReferencesOrderAndDAG(t *testing.T) {
	tests := []struct {
		name        string
		mutate      func(*testing.T, *fstest.MapFS)
		wantCode    string
		wantPointer string
	}{
		{
			name: "unknown step template",
			mutate: func(t *testing.T, files *fstest.MapFS) {
				editManifest(t, files, `"template": "contract"`, `"template": "missing"`)
			},
			wantCode: "template_not_found", wantPointer: "/targets/1/steps/0/template",
		},
		{
			name: "missing order entry",
			mutate: func(t *testing.T, files *fstest.MapFS) {
				editManifest(t, files, `"order": ["contract", "invoice", "act"]`, `"order": ["contract", "invoice"]`)
			},
			wantCode: "invalid_bundle_order", wantPointer: "/targets/1/order",
		},
		{
			name: "dependency order conflict",
			mutate: func(t *testing.T, files *fstest.MapFS) {
				editManifest(t, files, `"id": "invoice", "template": "invoice", "inputPointer": ""`, `"id": "invoice", "template": "invoice", "inputPointer": "", "dependsOn": ["contract"]`)
				editManifest(t, files, `"order": ["contract", "invoice", "act"]`, `"order": ["invoice", "contract", "act"]`)
			},
			wantCode: "invalid_bundle_order", wantPointer: "/targets/1/order",
		},
		{
			name: "dependency cycle",
			mutate: func(t *testing.T, files *fstest.MapFS) {
				editManifest(t, files, `"id": "contract", "template": "contract", "inputPointer": ""`, `"id": "contract", "template": "contract", "inputPointer": "", "dependsOn": ["invoice"]`)
				editManifest(t, files, `"id": "invoice", "template": "invoice", "inputPointer": ""`, `"id": "invoice", "template": "invoice", "inputPointer": "", "dependsOn": ["contract"]`)
			},
			wantCode: "bundle_cycle", wantPointer: "/targets/1/steps",
		},
		{
			name: "unknown dependency",
			mutate: func(t *testing.T, files *fstest.MapFS) {
				editManifest(t, files, `"id": "invoice", "template": "invoice", "inputPointer": ""`, `"id": "invoice", "template": "invoice", "inputPointer": "", "dependsOn": ["missing"]`)
			},
			wantCode: "step_not_found", wantPointer: "/targets/1/steps/1/dependsOn/0",
		},
		{
			name: "invalid input pointer",
			mutate: func(t *testing.T, files *fstest.MapFS) {
				editManifest(t, files, `"id": "contract", "template": "contract", "inputPointer": ""`, `"id": "contract", "template": "contract", "inputPointer": "customer/~2name"`)
			},
			wantCode: "invalid_json_pointer", wantPointer: "/targets/1/steps/0/inputPointer",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			files := mutableProjectFixtureFS(t)
			test.mutate(t, files)
			_, err := ReadProject(context.Background(), files)
			assertProjectDiagnostic(t, err, test.wantCode, test.wantPointer)
		})
	}
}

func TestReadProjectKeepsSchemaResolutionInsideSnapshot(t *testing.T) {
	files := mutableProjectFixtureFS(t)
	(*files)["schemas/sales.schema.json"] = &fstest.MapFile{Data: []byte(`{"type":"object","properties":{"x":{"$ref":"https://example.com/schema.json"}}}`)}
	_, err := ReadProject(context.Background(), files)
	if err == nil {
		t.Fatal("ReadProject() accepted an external schema reference")
	}
	var projectErr *ProjectValidationError
	if !errors.As(err, &projectErr) {
		t.Fatalf("error does not expose a project diagnostic: %v", err)
	}
	if projectErr.File != "schemas/sales.schema.json" || projectErr.Pointer != "/properties/x/$ref" || projectErr.Code != "schema_reference_escape" {
		t.Fatalf("diagnostic = %#v", projectErr)
	}
}

func TestReadProjectUsesOneSchemaBudgetAcrossTheWholeProject(t *testing.T) {
	files := mutableProjectFixtureFS(t)
	editManifest(t, files, `"entrypoint": "templates/invoice.typ",
      "inputSchema": "schemas/sales.schema.json"`, `"entrypoint": "templates/invoice.typ",
      "inputSchema": "schemas/large-b.schema.json"`)
	editManifest(t, files, `"id": "invoice", "kind": "document", "template": "invoice",
      "inputSchema": "schemas/sales.schema.json"`, `"id": "invoice", "kind": "document", "template": "invoice",
      "inputSchema": "schemas/large-b.schema.json"`)
	editManifest(t, files, `"inputSchema": "schemas/sales.schema.json",
      "steps": [`, `"inputSchema": "schemas/large-a.schema.json",
      "steps": [`)
	largeSchema := func() []byte {
		return []byte(`{"type":"object","description":"` + strings.Repeat("x", 2_100_000) + `"}`)
	}
	(*files)["schemas/large-a.schema.json"] = &fstest.MapFile{Data: largeSchema()}
	(*files)["schemas/large-b.schema.json"] = &fstest.MapFile{Data: largeSchema()}
	_, err := ReadProject(context.Background(), files)
	assertProjectDiagnostic(t, err, "schema_resource_limit", "")
}

func TestReadProjectRequiresBundleSchemaAndDescribeTargetVerifiesSnapshot(t *testing.T) {
	files := mutableProjectFixtureFS(t)
	editManifest(t, files, `"inputSchema": "schemas/sales.schema.json",
      "steps": [`, `"inputSchema": "schemas/missing.schema.json",
      "steps": [`)
	_, err := ReadProject(context.Background(), files)
	assertProjectDiagnostic(t, err, "missing_resource", "/targets/1/inputSchema")

	files = mutableProjectFixtureFS(t)
	manifest, err := ReadProject(context.Background(), files)
	if err != nil {
		t.Fatal(err)
	}
	snapshot := projectSnapshotManifest(t, files)
	(*files)["templates/invoice.typ"] = &fstest.MapFile{Data: []byte("changed after capture")}
	_, err = DescribeTarget(context.Background(), files, manifest, snapshot, "invoice", "production")
	assertProjectDiagnostic(t, err, "snapshot_mismatch", "")
}

func TestDescribeTargetBindsSnapshotFilesAndRejectsOutputOverwrite(t *testing.T) {
	files := mutableProjectFixtureFS(t)
	manifest, err := ReadProject(context.Background(), files)
	if err != nil {
		t.Fatal(err)
	}
	snapshot := projectSnapshotManifest(t, files)
	if _, err = DescribeTarget(context.Background(), files, manifest, snapshot, "sales-pack", "draft"); err != nil {
		t.Fatalf("bundle descriptor should be returned for E07 execution: %v", err)
	}

	editManifest(t, files, `"path": "output/result.pdf"`, `"path": "templates/invoice.typ"`)
	_, err = ReadProject(context.Background(), files)
	assertProjectDiagnostic(t, err, "output_overwrites_source", "/targets/0/output/path")
}

func projectFixtureFS(t *testing.T) fs.FS {
	t.Helper()
	return os.DirFS(filepath.Join("testdata", "project-contract"))
}

func mutableProjectFixtureFS(t *testing.T) *fstest.MapFS {
	t.Helper()
	root := filepath.Join("testdata", "project-contract")
	files := fstest.MapFS{}
	err := filepath.WalkDir(root, func(name string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		relative, err := filepath.Rel(root, name)
		if err != nil {
			return err
		}
		data, err := os.ReadFile(name)
		if err != nil {
			return err
		}
		files[filepath.ToSlash(relative)] = &fstest.MapFile{Data: data}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return &files
}

func editManifest(t *testing.T, files *fstest.MapFS, old, replacement string) {
	t.Helper()
	// Git checkouts may use CRLF; mutation needles describe logical JSON lines.
	content := strings.ReplaceAll(string((*files)["leafrun.json"].Data), "\r\n", "\n")
	if !strings.Contains(content, old) {
		t.Fatalf("manifest fixture does not contain %q", old)
	}
	(*files)["leafrun.json"] = &fstest.MapFile{Data: []byte(strings.Replace(content, old, replacement, 1))}
}

func deleteProjectFile(t *testing.T, files *fstest.MapFS, name string) {
	t.Helper()
	delete(*files, name)
}

func projectSnapshotManifest(t *testing.T, files fs.FS) SnapshotManifest {
	t.Helper()
	manifest := SnapshotManifest{FormatVersion: 1, Files: []SnapshotFile{}, Directories: []string{}, Fonts: []SnapshotFont{}}
	var directories []string
	err := fs.WalkDir(files, ".", func(name string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if name == "." {
			return nil
		}
		if entry.IsDir() {
			directories = append(directories, name)
			return nil
		}
		data, err := fs.ReadFile(files, name)
		if err != nil {
			return err
		}
		hash := sha256.Sum256(data)
		manifest.Files = append(manifest.Files, SnapshotFile{Path: name, Size: int64(len(data)), SHA256: fmt.Sprintf("%x", hash)})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	manifest.Directories = directories
	manifest.ID, err = SnapshotContentID(manifest)
	if err != nil {
		t.Fatal(err)
	}
	return manifest
}

func assertProjectDiagnostic(t *testing.T, err error, code, pointer string) {
	t.Helper()
	if err == nil {
		t.Fatal("operation unexpectedly succeeded")
	}
	var projectErr *ProjectValidationError
	if !errors.As(err, &projectErr) {
		t.Fatalf("error %T does not expose a project diagnostic: %v", err, err)
	}
	if projectErr.Code != code || projectErr.Pointer != pointer {
		t.Fatalf("diagnostic = (%q, %q), want (%q, %q): %v", projectErr.Code, projectErr.Pointer, code, pointer, projectErr)
	}
}
