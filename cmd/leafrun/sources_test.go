package main

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	core "github.com/leafrun/leafrun-document-core"
	"github.com/leafrun/leafrun-document-core/internal/cli"
)

func invoiceFixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	err := filepath.WalkDir("../../examples/invoice", func(name string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative, err := filepath.Rel("../../examples/invoice", name)
		if err != nil {
			return err
		}
		// A previous example CLI render must not become an archived source input.
		if relative == "output" && entry.IsDir() {
			return filepath.SkipDir
		}
		destination := filepath.Join(root, relative)
		if entry.IsDir() {
			return os.MkdirAll(destination, 0755)
		}
		data, err := os.ReadFile(name)
		if err != nil {
			return err
		}
		return os.WriteFile(destination, data, 0644)
	})
	if err != nil {
		t.Fatal(err)
	}
	return root
}

func TestInvoiceFixtureOmitsOnlyGeneratedRootOutput(t *testing.T) {
	workspace := t.TempDir()
	source := filepath.Join(workspace, "examples", "invoice")
	for name, data := range map[string]string{
		"main.typ":                   "Invoice source",
		"output/result.pdf":          "%PDF-generated example result",
		"resources/output/data.json": `{"input":true}`,
	} {
		filename := filepath.Join(source, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(filename), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filename, []byte(data), 0644); err != nil {
			t.Fatal(err)
		}
	}
	workingDirectory := filepath.Join(workspace, "cmd", "leafrun")
	if err := os.MkdirAll(workingDirectory, 0755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(workingDirectory)
	fixture := invoiceFixture(t)
	if _, err := os.Stat(filepath.Join(fixture, "output")); !os.IsNotExist(err) {
		t.Fatalf("generated root output copied into source fixture: %v", err)
	}
	for _, name := range []string{"main.typ", "resources/output/data.json"} {
		original, err := os.ReadFile(filepath.Join(source, filepath.FromSlash(name)))
		if err != nil {
			t.Fatal(err)
		}
		copied, err := os.ReadFile(filepath.Join(fixture, filepath.FromSlash(name)))
		if err != nil || !bytes.Equal(original, copied) {
			t.Fatalf("source resource %q changed: %q, %v", name, copied, err)
		}
	}
}

func archiveInvoice(t *testing.T, root string, tarGZ bool) string {
	t.Helper()
	name := filepath.Join(t.TempDir(), "invoice.zip")
	if tarGZ {
		name = filepath.Join(filepath.Dir(name), "invoice.tar.gz")
	}
	file, err := os.Create(name)
	if err != nil {
		t.Fatal(err)
	}
	var zipper *zip.Writer
	var tarball *tar.Writer
	var gzipper *gzip.Writer
	if tarGZ {
		gzipper = gzip.NewWriter(file)
		tarball = tar.NewWriter(gzipper)
	} else {
		zipper = zip.NewWriter(file)
	}
	err = filepath.WalkDir(root, func(name string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
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
		logical := "invoice/" + filepath.ToSlash(relative)
		if tarGZ {
			if err := tarball.WriteHeader(&tar.Header{Name: logical, Mode: 0644, Size: int64(len(data))}); err != nil {
				return err
			}
			_, err = tarball.Write(data)
			return err
		}
		out, err := zipper.Create(logical)
		if err != nil {
			return err
		}
		_, err = out.Write(data)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if tarGZ {
		if err := tarball.Close(); err != nil {
			t.Fatal(err)
		}
		if err := gzipper.Close(); err != nil {
			t.Fatal(err)
		}
	} else {
		if err := zipper.Close(); err != nil {
			t.Fatal(err)
		}
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	return name
}

func gitCommand(t *testing.T, root string, args ...string) string {
	t.Helper()
	args = append([]string{"-c", "core.excludesFile=", "-c", "core.autocrlf=false", "-c", "user.name=Leafrun Test", "-c", "user.email=test@example.invalid", "-C", root}, args...)
	command := exec.Command("git", args...)
	data, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, data)
	}
	return strings.TrimSpace(string(data))
}

func TestAllSourcesUseSameAcceptedInvoice(t *testing.T) {
	fixture := invoiceFixture(t)
	zipFile := archiveInvoice(t, fixture, false)
	tarFile := archiveInvoice(t, fixture, true)
	repository := invoiceFixture(t)
	gitCommand(t, repository, "init", "-b", "invoice")
	gitCommand(t, repository, "add", ".")
	gitCommand(t, repository, "commit", "-m", "Invoice fixture")
	requests := []cli.SourceOptions{{Archive: zipFile, SourceRoot: "invoice"}, {Archive: tarFile, SourceRoot: "invoice"}, {Git: repository, Ref: "invoice", SourceRoot: "."}}
	folder, err := core.CreateSnapshot(context.Background(), core.FolderSource{Root: fixture}, core.SnapshotOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer folder.Close()
	manifest := folder.Manifest()
	if len(manifest.Fonts) != 1 {
		t.Fatalf("bundled Fira missing: %+v", manifest.Fonts)
	}
	live, err := core.Render(context.Background(), core.RenderOptions{ProjectDir: fixture, Output: filepath.Join(t.TempDir(), "live.pdf")})
	if err != nil {
		t.Fatal(err)
	}
	checkPDF(t, live.PDFPath)
	frozen, err := core.RenderSnapshot(context.Background(), folder, core.RenderSnapshotOptions{Output: filepath.Join(t.TempDir(), "frozen.pdf")})
	if err != nil {
		t.Fatal(err)
	}
	checkPDF(t, frozen.PDFPath)
	for _, request := range requests {
		t.Run(request.Archive+request.Git, func(t *testing.T) {
			source, err := providerSource(request)
			if err != nil {
				t.Fatal(err)
			}
			snapshot, err := core.CreateSnapshot(context.Background(), source, core.SnapshotOptions{})
			if err != nil {
				t.Fatal(err)
			}
			defer snapshot.Close()
			captured := snapshot.Manifest()
			if captured.ID != manifest.ID || !reflect.DeepEqual(captured.Files, manifest.Files) || !reflect.DeepEqual(captured.Fonts, manifest.Fonts) {
				t.Fatalf("provider changed accepted content: %+v vs %+v", captured, manifest)
			}
			result, err := core.RenderSnapshot(context.Background(), snapshot, core.RenderSnapshotOptions{Output: filepath.Join(t.TempDir(), "accepted.pdf")})
			if err != nil {
				t.Fatal(err)
			}
			checkPDF(t, result.PDFPath)
			if result.RawOutput != live.RawOutput || len(result.Diagnostics) != len(live.Diagnostics) {
				t.Fatalf("provider changed invoice behavior: %+v", result)
			}
			var stdout, stderr bytes.Buffer
			args := []string{"render", "--output", filepath.Join(t.TempDir(), "cli.pdf"), "--source-root", request.SourceRoot}
			if request.Archive != "" {
				args = append(args, "--archive", request.Archive)
			} else {
				args = append(args, "--git", request.Git, "--ref", request.Ref)
			}
			if code := cli.RunWithSources(context.Background(), args, &stdout, &stderr, providerSource); code != 0 {
				t.Fatalf("CLI code %d: %s", code, &stderr)
			}
			checkPDF(t, strings.TrimSpace(stdout.String()))
		})
	}
}

func checkPDF(t *testing.T, path string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil || !bytes.HasPrefix(data, []byte("%PDF-")) {
		t.Fatalf("PDF %q: %v", path, err)
	}
	if !filepath.IsAbs(path) {
		t.Fatalf("PDF not absolute: %s", path)
	}
}

type afterProviderOpen struct {
	source core.ProjectSource
	after  func(core.SourceTree) error
}

func (source afterProviderOpen) Open(ctx context.Context) (core.SourceTree, error) {
	tree, err := source.source.Open(ctx)
	if err == nil {
		err = source.after(tree)
	}
	return tree, err
}

func TestArchiveAcceptedHandleProtectsReplacedOriginalAndHardlink(t *testing.T) {
	for _, tarGZ := range []bool{false, true} {
		t.Run(map[bool]string{false: "zip", true: "targz"}[tarGZ], func(t *testing.T) {
			archive := archiveInvoice(t, invoiceFixture(t), tarGZ)
			alias := filepath.Join(t.TempDir(), "original-hardlink.pdf")
			if err := os.Link(archive, alias); err != nil {
				t.Fatal(err)
			}
			accepted, err := os.ReadFile(alias)
			if err != nil {
				t.Fatal(err)
			}
			provider, err := providerSource(cli.SourceOptions{Archive: archive, SourceRoot: "invoice"})
			if err != nil {
				t.Fatal(err)
			}
			source := afterProviderOpen{source: provider, after: func(tree core.SourceTree) error {
				if err := os.Remove(archive); err != nil {
					return err
				}
				return os.WriteFile(archive, []byte("replacement compressed path"), 0644)
			}}
			snapshot, err := core.CreateSnapshot(context.Background(), source, core.SnapshotOptions{})
			if err != nil {
				t.Fatal(err)
			}
			defer snapshot.Close()
			for _, output := range []string{alias, archive} {
				result, err := core.RenderSnapshot(context.Background(), snapshot, core.RenderSnapshotOptions{Output: output})
				if err == nil || result.PDFPath != "" {
					t.Fatalf("archive original overwritten: %+v %v", result, err)
				}
			}
			actual, err := os.ReadFile(alias)
			if err != nil || !bytes.Equal(actual, accepted) {
				t.Fatal("accepted archive hardlink changed")
			}
			result, err := core.RenderSnapshot(context.Background(), snapshot, core.RenderSnapshotOptions{Output: filepath.Join(t.TempDir(), "frozen.pdf")})
			if err != nil {
				t.Fatal(err)
			}
			checkPDF(t, result.PDFPath)
		})
	}
}

func TestGitBareLocalAndLinkedMetadataProtection(t *testing.T) {
	repository := invoiceFixture(t)
	gitCommand(t, repository, "init", "-b", "invoice")
	gitCommand(t, repository, "add", ".")
	gitCommand(t, repository, "commit", "-m", "Invoice fixture")
	bare := filepath.Join(t.TempDir(), "bare.git")
	gitCommand(t, repository, "clone", "--bare", repository, bare)
	linked := filepath.Join(t.TempDir(), "linked")
	gitCommand(t, repository, "worktree", "add", "-b", "linked", linked, "invoice")
	for _, test := range []struct{ name, location, metadata, ref string }{
		{"local", repository, filepath.Join(repository, ".git"), "invoice"},
		{"bare", bare, bare, "invoice"},
		{"linked", linked, filepath.Join(repository, ".git", "worktrees", filepath.Base(linked)), "linked"},
	} {
		t.Run(test.name, func(t *testing.T) {
			source, err := providerSource(cli.SourceOptions{Git: test.location, Ref: test.ref})
			if err != nil {
				t.Fatal(err)
			}
			snapshot, err := core.CreateSnapshot(context.Background(), source, core.SnapshotOptions{})
			if err != nil {
				t.Fatal(err)
			}
			defer snapshot.Close()
			// Mutable configuration activity never invalidates accepted committed bytes.
			config := filepath.Join(repository, ".git", "config")
			if test.name == "bare" {
				config = filepath.Join(bare, "config")
			}
			before, err := os.ReadFile(config)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(config, append(before, []byte("\n# metadata changed after acceptance\n")...), 0644); err != nil {
				t.Fatal(err)
			}
			paths := []string{filepath.Join(test.metadata, "untracked/nested/result.pdf")}
			if test.name != "bare" {
				paths = append(paths, filepath.Join(repository, ".git", "untracked-result.pdf"))
			}
			if test.name == "linked" {
				paths = append(paths, filepath.Join(linked, ".git"))
			}
			alias := filepath.Join(t.TempDir(), "metadata-hardlink.pdf")
			if err := os.Link(config, alias); err != nil {
				t.Fatal(err)
			}
			paths = append(paths, alias)
			if test.name != "bare" {
				resourceAlias := filepath.Join(t.TempDir(), "resource-hardlink.pdf")
				if err := os.Link(filepath.Join(test.location, "data.json"), resourceAlias); err != nil {
					t.Fatal(err)
				}
				paths = append(paths, resourceAlias)
			}
			for _, output := range paths {
				result, err := core.RenderSnapshot(context.Background(), snapshot, core.RenderSnapshotOptions{Output: output})
				if err == nil || result.PDFPath != "" {
					t.Fatalf("Git original overwritten at %s: %+v %v", output, result, err)
				}
			}
			result, err := core.RenderSnapshot(context.Background(), snapshot, core.RenderSnapshotOptions{Output: filepath.Join(t.TempDir(), "accepted.pdf")})
			if err != nil {
				t.Fatal(err)
			}
			checkPDF(t, result.PDFPath)
		})
	}
}

func TestExternalCLIOutputIsRelativeToCallerCWD(t *testing.T) {
	fixture := invoiceFixture(t)
	archive := archiveInvoice(t, fixture, false)
	caller := t.TempDir()
	caller, err := filepath.EvalSymlinks(caller)
	if err != nil {
		t.Fatal(err)
	}
	t.Chdir(caller)
	var stdout, stderr bytes.Buffer
	code := cli.RunWithSources(context.Background(), []string{"render", "--archive", archive, "--source-root", "invoice", "--output", "published/invoice.pdf"}, &stdout, &stderr, providerSource)
	if code != 0 {
		t.Fatalf("exit %d: %s", code, &stderr)
	}
	want := filepath.Join(caller, "published/invoice.pdf")
	if strings.TrimSpace(stdout.String()) != want {
		t.Fatalf("cwd output %q want %q", stdout.String(), want)
	}
	checkPDF(t, want)
}
