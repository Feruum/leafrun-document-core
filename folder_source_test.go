package documentcore

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func TestFolderSourceCapturesIndependentBytesAndNativeInputIdentities(t *testing.T) {
	root := t.TempDir()
	writeFolderFile(t, root, "main.typ", "document")
	writeFolderFile(t, root, "nested/data.json", "{\"title\":\"captured\"}")
	if err := os.Mkdir(filepath.Join(root, "empty"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(filepath.Join(root, "main.typ"), filepath.Join(root, "alias.typ")); err != nil {
		t.Fatal(err)
	}
	snapshot, err := CreateSnapshot(context.Background(), FolderSource{Root: root}, SnapshotOptions{TempDir: t.TempDir()})
	if err != nil || snapshot == nil {
		t.Fatalf("folder capture = %v, %v", snapshot, err)
	}
	t.Cleanup(func() {
		if err := snapshot.Close(); err != nil {
			t.Error(err)
		}
	})
	manifest := snapshot.Manifest()
	canonicalRoot, err := canonicalDirectory(root)
	if err != nil {
		t.Fatal(err)
	}
	if manifest.Origin.Kind != "folder" || manifest.Origin.Location != canonicalRoot || manifest.Origin.SourceRoot != canonicalRoot || manifest.Origin.Revision != manifest.ID {
		t.Fatalf("folder provenance = %+v", manifest.Origin)
	}
	if got := folderFilePaths(manifest.Files); !reflect.DeepEqual(got, []string{"alias.typ", "main.typ", "nested/data.json"}) {
		t.Fatalf("captured paths = %v", got)
	}
	if !reflect.DeepEqual(manifest.Directories, []string{"empty", "nested"}) {
		t.Fatalf("captured directories = %v", manifest.Directories)
	}
	if len(snapshot.inputs) != 3 {
		t.Fatalf("original inputs = %+v", snapshot.inputs)
	}
	for _, input := range snapshot.inputs {
		original, err := os.Stat(input.path)
		if err != nil || !os.SameFile(original, input.info) {
			t.Fatalf("native identity for %q was lost: %v", input.path, err)
		}
		captured, err := os.Stat(filepath.Join(snapshot.root, "project", filepath.FromSlash(input.projectPath)))
		if err != nil || os.SameFile(original, captured) {
			t.Fatalf("snapshot shares original identity for %q: %v", input.path, err)
		}
	}
	first, err := os.Stat(filepath.Join(snapshot.root, "project", "main.typ"))
	if err != nil {
		t.Fatal(err)
	}
	second, err := os.Stat(filepath.Join(snapshot.root, "project", "alias.typ"))
	if err != nil {
		t.Fatal(err)
	}
	if os.SameFile(first, second) {
		t.Fatal("captured files share hardlinks")
	}
	writeFolderFile(t, root, "main.typ", "changed later")
	if err := os.Remove(filepath.Join(root, "nested", "data.json")); err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]string{"main.typ": "document", "alias.typ": "document", "nested/data.json": "{\"title\":\"captured\"}"} {
		data, err := fs.ReadFile(snapshot.FS(), name)
		if err != nil || string(data) != want {
			t.Errorf("accepted %q = %q, %v", name, data, err)
		}
	}
	if err := snapshot.Close(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(root, "main.typ"))
	if err != nil || string(data) != "changed later" {
		t.Fatalf("Close changed caller files: %q, %v", data, err)
	}
}

func TestFolderSourceExcludesOnlyReservedMetadataAndSelectedResult(t *testing.T) {
	root := t.TempDir()
	files := map[string]string{
		"main.typ": "document", ".github/workflow.yml": "resource",
		".git/config": "metadata", "nested/.git": "gitdir: private",
		"output/result.pdf": "old result", "output/input.json": "reserved output",
		"nested/output/input.json": "resource", "nested/result.pdf": "selected result",
		".leafrun-partial": "staging", "nested/.leafrun-result.pdf": "staging",
		".leafrun-resources/data.json": "resource", "nested/other.pdf": "resource",
	}
	for name, data := range files {
		writeFolderFile(t, root, name, data)
	}
	snapshot, err := CreateSnapshot(context.Background(), FolderSource{Root: root}, SnapshotOptions{Output: "nested/result.pdf", TempDir: t.TempDir()})
	if err != nil || snapshot == nil {
		t.Fatalf("capture = %v, %v", snapshot, err)
	}
	defer snapshot.Close()
	want := []string{".github/workflow.yml", ".leafrun-resources/data.json", "main.typ", "nested/other.pdf", "nested/output/input.json"}
	if got := folderFilePaths(snapshot.Manifest().Files); !reflect.DeepEqual(got, want) {
		t.Fatalf("filtered paths = %v; want %v", got, want)
	}
	for _, name := range []string{".git/config", "nested/.git", "output/result.pdf", "output/input.json", "nested/result.pdf", ".leafrun-partial", "nested/.leafrun-result.pdf"} {
		if data, err := fs.ReadFile(snapshot.FS(), name); !errors.Is(err, fs.ErrNotExist) || len(data) != 0 {
			t.Errorf("excluded %q = %q, %v", name, data, err)
		}
		data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(name)))
		if err != nil || string(data) != files[name] {
			t.Errorf("excluded original %q changed: %q, %v", name, data, err)
		}
	}
}

func TestFolderSourceRequiresAnExplicitExistingDirectory(t *testing.T) {
	file := filepath.Join(t.TempDir(), "file.typ")
	if err := os.WriteFile(file, []byte("text"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, root := range []string{"", filepath.Join(t.TempDir(), "missing"), file} {
		t.Run(root, func(t *testing.T) {
			snapshot, err := CreateSnapshot(context.Background(), FolderSource{Root: root}, SnapshotOptions{TempDir: t.TempDir()})
			if snapshot != nil {
				snapshot.Close()
			}
			if snapshot != nil || err == nil {
				t.Fatalf("invalid selected root %q accepted: %v", root, err)
			}
		})
	}
}

func TestFolderSourceIgnoresExcludedOnlyDirectoryActivity(t *testing.T) {
	root := t.TempDir()
	writeFolderFile(t, root, "main.typ", "stable")
	writeFolderFile(t, root, "nested/data.json", "stable")
	writeFolderFile(t, root, "nested/result.pdf", "old selected output")
	source := &folderHookSource{source: FolderSource{Root: root}}
	source.onOpen = func(name string, count int) {
		if name != "main.typ" || count != 2 {
			return
		}
		writeFolderFile(t, root, "output/result.pdf", "new output")
		writeFolderFile(t, root, ".leafrun-new", "new staging")
		writeFolderFile(t, root, "nested/.leafrun-new", "new staging")
		writeFolderFile(t, root, "nested/result.pdf", "new selected output")
		for _, directory := range []string{root, filepath.Join(root, "nested")} {
			future := time.Unix(1800000000, 0)
			if err := os.Chtimes(directory, future, future); err != nil {
				t.Fatal(err)
			}
		}
	}
	snapshot, err := CreateSnapshot(context.Background(), source, SnapshotOptions{Output: "nested/result.pdf", TempDir: t.TempDir()})
	if err != nil || snapshot == nil {
		t.Fatalf("excluded-only activity rejected coherent inputs: %v", err)
	}
	defer snapshot.Close()
	if got := folderFilePaths(snapshot.Manifest().Files); !reflect.DeepEqual(got, []string{"main.typ", "nested/data.json"}) {
		t.Fatalf("excluded activity entered capture: %v", got)
	}
	if source.opens != 1 || source.releases != 1 || source.counts["main.typ"] != 3 {
		t.Fatalf("capture retried or leaked source: %+v", source)
	}
}

func TestFolderSourceRejectsIncludedDirectoryReplacement(t *testing.T) {
	root := t.TempDir()
	writeFolderFile(t, root, "main.typ", "stable")
	writeFolderFile(t, root, "nested/data.json", "stable")
	before := map[string]fs.FileInfo{}
	for _, name := range []string{".", "nested", "nested/data.json"} {
		info, err := os.Stat(filepath.Join(root, filepath.FromSlash(name)))
		if err != nil {
			t.Fatal(err)
		}
		before[name] = info
	}
	source := &folderHookSource{source: FolderSource{Root: root}}
	source.onOpen = func(name string, count int) {
		if name != "main.typ" || count != 2 {
			return
		}
		if err := os.Rename(filepath.Join(root, "nested"), filepath.Join(t.TempDir(), "original")); err != nil {
			t.Fatal(err)
		}
		writeFolderFile(t, root, "nested/data.json", "stable")
		for _, name := range []string{"nested/data.json", "nested", "."} {
			stamp := before[name].ModTime()
			if err := os.Chtimes(filepath.Join(root, filepath.FromSlash(name)), stamp, stamp); err != nil {
				t.Fatal(err)
			}
		}
	}
	snapshot, err := CreateSnapshot(context.Background(), source, SnapshotOptions{TempDir: t.TempDir()})
	if snapshot != nil {
		snapshot.Close()
	}
	if snapshot != nil || !errors.Is(err, ErrSourceChanged) {
		t.Fatalf("directory replacement was accepted: snapshot=%v, %v", snapshot, err)
	}
}

func TestFolderSourcePinsIncludedDirectoryIdentity(t *testing.T) {
	root := t.TempDir()
	writeFolderFile(t, root, "nested/data.json", "stable")
	tree, err := (FolderSource{Root: root}).Open(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer tree.Close()
	if _, err := fs.ReadDir(tree.FS, "nested"); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(filepath.Join(root, "nested"), filepath.Join(t.TempDir(), "original")); err != nil {
		t.Fatal(err)
	}
	writeFolderFile(t, root, "nested/data.json", "stable")
	if _, err := fs.ReadDir(tree.FS, "nested"); !errors.Is(err, ErrSourceChanged) {
		t.Fatalf("replaced included directory accepted: %v", err)
	}
}

func TestFolderSourceSupportsMissingResultsAndCanonicalSelectedAliases(t *testing.T) {
	root := t.TempDir()
	writeFolderFile(t, root, "main.typ", "stable")
	writeFolderFile(t, root, "nested/result.pdf", "old output")
	alias := filepath.Join(t.TempDir(), "project-alias")
	makeTestDirectoryLink(t, root, alias)
	canonicalRoot, err := canonicalDirectory(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, output := range []string{filepath.Join(alias, "nested", "result.pdf"), "missing/nested/result.pdf"} {
		t.Run(output, func(t *testing.T) {
			snapshot, err := CreateSnapshot(context.Background(), FolderSource{Root: alias}, SnapshotOptions{Output: output, TempDir: t.TempDir()})
			if err != nil || snapshot == nil {
				t.Fatalf("selected alias or missing result rejected: %v", err)
			}
			defer snapshot.Close()
			if snapshot.Manifest().Origin.Location != canonicalRoot || snapshot.Manifest().Origin.SourceRoot != canonicalRoot {
				t.Fatalf("alias provenance is not canonical: %+v", snapshot.Manifest().Origin)
			}
			if output != "missing/nested/result.pdf" {
				if _, err := fs.ReadFile(snapshot.FS(), "nested/result.pdf"); !errors.Is(err, fs.ErrNotExist) {
					t.Fatalf("canonical result alias was captured: %v", err)
				}
			}
		})
	}
}

func TestFolderSourcePinsCanonicalSelectedRootAcrossAliasRetargeting(t *testing.T) {
	root, unrelated := t.TempDir(), t.TempDir()
	writeFolderFile(t, root, "main.typ", "selected")
	writeFolderFile(t, unrelated, "main.typ", "unrelated")
	alias := filepath.Join(t.TempDir(), "selected-alias")
	makeTestDirectoryLink(t, root, alias)
	tree, err := (FolderSource{Root: alias}).Open(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer tree.Close()
	canonical, err := canonicalDirectory(root)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(tree.ProtectedPaths, []string{canonical}) || tree.Origin.Location != canonical || tree.Origin.SourceRoot != canonical {
		t.Fatalf("selected root retained alias paths: %+v", tree)
	}
	if err := os.Remove(alias); err != nil {
		t.Fatal(err)
	}
	makeTestDirectoryLink(t, unrelated, alias)
	data, err := fs.ReadFile(tree.FS, "main.typ")
	if err != nil || string(data) != "selected" {
		t.Fatalf("alias redirected bounded reads: %q, %v", data, err)
	}
	if err := tree.Close(); err != nil {
		t.Fatal(err)
	}
	data, err = os.ReadFile(filepath.Join(unrelated, "main.typ"))
	if err != nil || string(data) != "unrelated" {
		t.Fatalf("source release changed alias target: %q, %v", data, err)
	}
}

func TestFolderSourceAcceptsExplicitExternalFontRootAlias(t *testing.T) {
	root, fonts := t.TempDir(), t.TempDir()
	writeFolderFile(t, root, "main.typ", "document")
	writeFolderFile(t, fonts, "sample.ttf", "external")
	alias := filepath.Join(t.TempDir(), "external-font-alias")
	makeTestDirectoryLink(t, fonts, alias)
	snapshot, err := CreateSnapshot(context.Background(), FolderSource{Root: root}, SnapshotOptions{TempDir: t.TempDir(), FontPaths: []string{alias}})
	if err != nil {
		t.Fatal(err)
	}
	defer snapshot.Close()
	canonical, err := canonicalDirectory(fonts)
	if err != nil {
		t.Fatal(err)
	}
	manifest := snapshot.Manifest()
	if len(manifest.Fonts) != 1 || manifest.Fonts[0].SourcePath != canonical || manifest.Fonts[0].InProject {
		t.Fatalf("external font alias provenance = %+v", manifest.Fonts)
	}
	if got := folderFilePaths(manifest.Fonts[0].Files); !reflect.DeepEqual(got, []string{"sample.ttf"}) {
		t.Fatalf("external font alias lost bytes: %v", got)
	}
}

func TestFolderSourceCopiesConfigurationBeforeAcquiringHandles(t *testing.T) {
	root := t.TempDir()
	writeFolderFile(t, root, "main.typ", "document")
	writeFolderFile(t, root, "custom/sample.ttf", "font")
	options := SnapshotOptions{FontPaths: []string{"custom"}, Output: "custom/sample.ttf"}
	configured := (FolderSource{Root: root}).withSnapshotOptions(options)
	options.FontPaths[0] = "other"
	options.Output = "main.typ"
	tree, err := configured.Open(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer tree.Close()
	metadata, err := tree.FS.(snapshotInputMetadataSource).snapshotInputMetadata(context.Background())
	if err != nil || len(metadata) != 1 || !metadata[0].font || metadata[0].projectPath != "custom/sample.ttf" {
		t.Fatalf("caller changes mutated selected options: %+v, %v", metadata, err)
	}
	if data, err := fs.ReadFile(tree.FS, "main.typ"); err != nil || string(data) != "document" {
		t.Fatalf("caller changes altered exclusions: %q, %v", data, err)
	}
}

func TestFolderSourceCapturesFontsInOrderWithProjectExclusions(t *testing.T) {
	root := t.TempDir()
	first, second := t.TempDir(), t.TempDir()
	for name, data := range map[string]string{"main.typ": "text", "fonts/default.ttf": "default", "alternate/relative.otf": "relative", ".git/config": "metadata", "output/old.pdf": "output", ".leafrun-partial": "staging"} {
		writeFolderFile(t, root, name, data)
	}
	writeFolderFile(t, first, "sample.ttf", "first")
	writeFolderFile(t, second, "sample.ttf", "second")
	alias := filepath.Join(t.TempDir(), "project-font-alias")
	makeTestDirectoryLink(t, root, alias)
	fontPaths := []string{"alternate", first, second, alias}
	before := append([]string(nil), fontPaths...)
	source := &folderHookSource{source: FolderSource{Root: root}}
	snapshot, err := CreateSnapshot(context.Background(), source, SnapshotOptions{TempDir: t.TempDir(), FontPaths: fontPaths})
	if err != nil || snapshot == nil {
		t.Fatalf("font capture = %v, %v", snapshot, err)
	}
	defer snapshot.Close()
	if !reflect.DeepEqual(fontPaths, before) {
		t.Fatalf("capture mutated caller font paths: %v", fontPaths)
	}
	fonts := snapshot.Manifest().Fonts
	canonicalFirst, err := canonicalDirectory(first)
	if err != nil {
		t.Fatal(err)
	}
	canonicalSecond, err := canonicalDirectory(second)
	if err != nil {
		t.Fatal(err)
	}
	canonicalRoot, err := canonicalDirectory(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(fonts) != 5 || fonts[0].Path != "fonts" || fonts[1].Path != "alternate" || fonts[2].SourcePath != canonicalFirst || fonts[3].SourcePath != canonicalSecond || fonts[4].SourcePath != canonicalRoot {
		t.Fatalf("font search order = %+v", fonts)
	}
	if got := folderFilePaths(fonts[4].Files); !reflect.DeepEqual(got, folderFilePaths(snapshot.Manifest().Files)) {
		t.Fatalf("project alias bypassed exclusions: %v", got)
	}
	writeFolderFile(t, first, "sample.ttf", "later")
	writeFolderFile(t, second, "sample.ttf", "later")
	working, err := snapshot.prepareWorkingCopy(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer working.Close()
	for index, want := range map[int]string{2: "first", 3: "second"} {
		data, err := os.ReadFile(filepath.Join(working.fonts[index], "sample.ttf"))
		if err != nil || string(data) != want {
			t.Fatalf("frozen font %d = %q, %v", index, data, err)
		}
	}
	if source.opens != 1 || source.releases != 1 {
		t.Fatalf("font source lifecycle = %d opens, %d releases", source.opens, source.releases)
	}
	if _, err := source.filesystem.Open("main.typ"); err == nil {
		t.Fatal("released project root remains open")
	}
}

func TestFolderSourceOwnsAdditionalFontHandlesUntilRelease(t *testing.T) {
	root, fonts := t.TempDir(), t.TempDir()
	writeFolderFile(t, root, "main.typ", "text")
	writeFolderFile(t, fonts, "sample.ttf", "font")
	tree, err := (FolderSource{Root: root}).Open(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := tree.Close(); err != nil {
			t.Error(err)
		}
	})
	provider, ok := tree.FS.(snapshotNativeFontSource)
	if !ok {
		t.Fatal("folder source does not own additional font capture")
	}
	fontFS, err := provider.snapshotFontFS(fonts)
	if err != nil {
		t.Fatal(err)
	}
	if data, err := fs.ReadFile(fontFS, "sample.ttf"); err != nil || string(data) != "font" {
		t.Fatalf("owned font FS = %q, %v", data, err)
	}
	if err := tree.Close(); err != nil {
		t.Fatal(err)
	}
	if err := tree.Close(); err != nil {
		t.Fatalf("release is not idempotent: %v", err)
	}
	if _, err := fontFS.Open("sample.ttf"); err == nil {
		t.Fatal("source release left font root open")
	}
	if _, err := tree.FS.(snapshotNativeFontSource).snapshotFontFS(fonts); !errors.Is(err, fs.ErrClosed) {
		t.Fatalf("released source accepted a font root: %v", err)
	}
	if data, err := os.ReadFile(filepath.Join(fonts, "sample.ttf")); err != nil || string(data) != "font" {
		t.Fatalf("release changed caller fonts: %q, %v", data, err)
	}
}

func TestFolderSourceRetainsExcludedResultIdentityForPublicationGuards(t *testing.T) {
	for _, item := range []struct {
		name, selected string
		fonts          []string
		font           bool
	}{
		{name: "entrypoint", selected: "main.typ"},
		{name: "json-resource", selected: "resources/data.json"},
		{name: "default-font", selected: "fonts/sample.ttf", font: true},
		{name: "declared-font", selected: "custom/sample.otf", fonts: []string{"custom"}, font: true},
		{name: "project-font-root", selected: "sample.ttc", fonts: []string{"."}, font: true},
		{name: "old-generated-result", selected: "result.pdf"},
		{name: "old-generated-result-in-font-root", selected: "result.pdf", fonts: []string{"."}},
	} {
		t.Run(item.name, func(t *testing.T) {
			root := t.TempDir()
			writeFolderFile(t, root, "main.typ", "document")
			writeFolderFile(t, root, item.selected, "selected bytes")
			output := filepath.Join(root, filepath.FromSlash(item.selected))
			snapshot, err := CreateSnapshot(context.Background(), FolderSource{Root: root}, SnapshotOptions{TempDir: t.TempDir(), Output: item.selected, FontPaths: item.fonts})
			if err != nil || snapshot == nil {
				t.Fatalf("capture = %v, %v", snapshot, err)
			}
			defer snapshot.Close()
			if _, err := fs.ReadFile(snapshot.FS(), item.selected); !errors.Is(err, fs.ErrNotExist) {
				t.Fatalf("excluded result bytes are exposed: %v", err)
			}
			info, err := os.Stat(output)
			if err != nil {
				t.Fatal(err)
			}
			found := false
			for _, input := range snapshot.inputs {
				if input.projectPath != item.selected {
					continue
				}
				found = true
				if !os.SameFile(info, input.info) || input.font != item.font {
					t.Fatalf("excluded result lost guard metadata: %+v", input)
				}
				if !input.excluded {
					t.Fatal("excluded result is marked as captured input bytes")
				}
			}
			if !found {
				t.Fatalf("excluded result lost original identity: %+v", snapshot.inputs)
			}
		})
	}
}

func TestFolderSourceExclusionPreservesHardlinkSiblingAndContentDigest(t *testing.T) {
	root := t.TempDir()
	writeFolderFile(t, root, "main.typ", "document")
	writeFolderFile(t, root, "resources/data.json", "resource")
	if err := os.Link(filepath.Join(root, "resources", "data.json"), filepath.Join(root, "result.pdf")); err != nil {
		t.Fatal(err)
	}
	options := SnapshotOptions{Output: "result.pdf", TempDir: t.TempDir()}
	snapshot, err := CreateSnapshot(context.Background(), FolderSource{Root: root}, options)
	if err != nil {
		t.Fatal(err)
	}
	defer snapshot.Close()
	outputInfo, err := os.Stat(filepath.Join(root, "result.pdf"))
	if err != nil {
		t.Fatal(err)
	}
	for _, input := range snapshot.inputs {
		if input.projectPath == "resources/data.json" {
			if !os.SameFile(input.info, outputInfo) {
				t.Fatal("selected output hid native hardlink identity")
			}
		}
	}
	if got := folderFilePaths(snapshot.Manifest().Files); !reflect.DeepEqual(got, []string{"main.typ", "resources/data.json"}) {
		t.Fatalf("output exclusion hid siblings: %v", got)
	}
	if err := os.Remove(filepath.Join(root, "result.pdf")); err != nil {
		t.Fatal(err)
	}
	writeFolderFile(t, root, "result.pdf", "different generated result")
	second, err := CreateSnapshot(context.Background(), FolderSource{Root: root}, options)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	if snapshot.Manifest().ID != second.Manifest().ID {
		t.Fatal("excluded result metadata entered the content digest")
	}
}

func TestFolderSourceRetainsExternalSelectedFontIdentityWithoutBytes(t *testing.T) {
	root, fonts := t.TempDir(), t.TempDir()
	writeFolderFile(t, root, "main.typ", "document")
	writeFolderFile(t, fonts, "selected.otf", "font")
	output := filepath.Join(fonts, "selected.otf")
	snapshot, err := CreateSnapshot(context.Background(), FolderSource{Root: root}, SnapshotOptions{TempDir: t.TempDir(), Output: output, FontPaths: []string{fonts}})
	if err != nil {
		t.Fatal(err)
	}
	defer snapshot.Close()
	if len(snapshot.Manifest().Fonts) != 1 || len(snapshot.Manifest().Fonts[0].Files) != 0 {
		t.Fatalf("external selected output entered font bytes: %+v", snapshot.Manifest().Fonts)
	}
	info, err := os.Stat(output)
	if err != nil {
		t.Fatal(err)
	}
	for _, input := range snapshot.inputs {
		if input.font && os.SameFile(info, input.info) {
			return
		}
	}
	t.Fatalf("external selected font identity not protected: %+v", snapshot.inputs)
}

// This wrapper changes real native inputs exactly between read passes. It
// forwards the source's private options and font provider, without global hooks.
type folderHookSource struct {
	source          FolderSource
	onOpen          func(string, int)
	onClose         func(string, int)
	onRead          func(string, int)
	wrapFontFS      func(string, fs.FS) fs.FS
	counts          map[string]int
	opens, releases int
	filesystem      fs.FS
}

func (source *folderHookSource) withSnapshotOptions(options SnapshotOptions) ProjectSource {
	source.source = source.source.withSnapshotOptions(options).(FolderSource)
	return source
}

func (source *folderHookSource) Open(ctx context.Context) (SourceTree, error) {
	source.opens++
	tree, err := source.source.Open(ctx)
	if tree.Close != nil {
		close := tree.Close
		tree.Close = func() error { source.releases++; return close() }
	}
	if tree.FS != nil {
		source.filesystem = tree.FS
		source.counts = make(map[string]int)
		tree.FS = &folderHookFS{FS: tree.FS, source: source}
	}
	return tree, err
}

type folderHookFS struct {
	fs.FS
	source *folderHookSource
}

func (filesystem *folderHookFS) Open(name string) (fs.File, error) {
	filesystem.source.counts[name]++
	if filesystem.source.onOpen != nil {
		filesystem.source.onOpen(name, filesystem.source.counts[name])
	}
	file, err := filesystem.FS.Open(name)
	if err != nil {
		return nil, err
	}
	return &folderHookFile{File: file, source: filesystem.source, name: name, pass: filesystem.source.counts[name]}, nil
}

type folderHookFile struct {
	fs.File
	source *folderHookSource
	name   string
	pass   int
	read   bool
}

func (file folderHookFile) Close() error {
	err := file.File.Close()
	if file.source.onClose != nil {
		file.source.onClose(file.name, file.pass)
	}
	return err
}

func (file folderHookFile) ReadDir(n int) ([]fs.DirEntry, error) {
	return file.File.(fs.ReadDirFile).ReadDir(n)
}

func (file *folderHookFile) Read(data []byte) (int, error) {
	if !file.read {
		file.read = true
		if file.source.onRead != nil {
			file.source.onRead(file.name, file.pass)
		}
	}
	return file.File.Read(data)
}

func (filesystem *folderHookFS) snapshotFontFS(root string) (fs.FS, error) {
	provider, ok := filesystem.FS.(snapshotNativeFontSource)
	if !ok {
		return nil, errors.New("folder source does not own additional font capture")
	}
	fonts, err := provider.snapshotFontFS(root)
	if err == nil && filesystem.source.wrapFontFS != nil {
		fonts = filesystem.source.wrapFontFS(root, fonts)
	}
	return fonts, err
}

func (filesystem *folderHookFS) snapshotInputMetadata(ctx context.Context) ([]snapshotInput, error) {
	if provider, ok := filesystem.FS.(snapshotInputMetadataSource); ok {
		return provider.snapshotInputMetadata(ctx)
	}
	return nil, nil
}

func writeFolderFile(t *testing.T, root, name, data string) {
	t.Helper()
	filename := filepath.Join(root, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(filename), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filename, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
}

func folderFilePaths(files []SnapshotFile) []string {
	paths := make([]string, len(files))
	for i, file := range files {
		paths[i] = file.Path
	}
	return paths
}
