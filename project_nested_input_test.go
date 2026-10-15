package documentcore

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"path/filepath"
	"testing"
)

func TestPreparedFileRendersFromNestedTemplateEntrypoint(t *testing.T) {
	ctx := context.Background()
	source := t.TempDir()
	if err := os.Mkdir(filepath.Join(source, "templates"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "templates", "main.typ"), []byte(`#let data = json(sys.inputs.at("leafrun-data"))
#image(data.logo)
`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "schema.json"), []byte(`{"type":"object","properties":{"logo":{"type":"string","x-leafrun-file":true}},"required":["logo"]}`), 0600); err != nil {
		t.Fatal(err)
	}
	base, err := CreateSnapshot(ctx, FolderSource{Root: source}, SnapshotOptions{TempDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer base.Close()
	svg := []byte(`<svg xmlns="http://www.w3.org/2000/svg" width="20" height="20"><rect width="20" height="20" fill="navy"/></svg>`)
	digest := sha256.Sum256(svg)
	prepared, err := PrepareParameters(ctx, base.FS(), "schema.json", []byte(`{}`), []FileRef{{Pointer: "/logo", ResourceID: "logo-resource", Filename: "logo.svg", SHA256: hex.EncodeToString(digest[:]), SizeBytes: int64(len(svg))}})
	if err != nil {
		t.Fatal(err)
	}
	derived, err := CreatePreparedSnapshot(ctx, base, prepared, func(context.Context, PreparedFile) (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader(svg)), nil
	}, CreateInputSnapshotOptions{TempDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer derived.Close()
	if _, err := RenderSnapshot(ctx, derived, RenderSnapshotOptions{Entrypoint: "templates/main.typ", Output: filepath.Join(t.TempDir(), "result.pdf"), Inputs: prepared.Inputs}); err != nil {
		t.Fatalf("nested template could not use its bound file: %v", err)
	}
}
