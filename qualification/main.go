package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/mod/module"
	"golang.org/x/mod/sumdb/dirhash"
	"golang.org/x/mod/zip"
)

func check(err error) {
	if err != nil {
		log.Fatal(err)
	}
}

func main() {
	repo, rev, root := os.Args[1], os.Args[2], os.Args[3]
	const path = "gitlab.com/leafrun/leafrun-integrations"
	git := func(args ...string) []byte {
		b, err := exec.Command("git", append([]string{"-C", repo}, args...)...).Output()
		check(err)
		return b
	}
	tags := strings.Fields(string(git("tag", "--merged", rev)))
	if len(tags) != 1 || tags[0] != "v0.1.0" {
		log.Fatal("unexpected release ancestry")
	}
	stamp, err := time.Parse(time.RFC3339, strings.TrimSpace(string(git("show", "-s", "--format=%cI", rev))))
	check(err)
	ver := module.PseudoVersion("v0", "v0.1.0", stamp, rev[:12])
	dir := filepath.Join(root, filepath.FromSlash(path), "@v")
	check(os.MkdirAll(dir, 0755))
	base := filepath.Join(dir, ver)
	mod := git("show", rev+":go.mod")
	check(os.WriteFile(base+".mod", mod, 0644))
	info, err := json.Marshal(map[string]string{"Version": ver, "Time": stamp.UTC().Format(time.RFC3339)})
	check(err)
	check(os.WriteFile(base+".info", info, 0644))
	file, err := os.Create(base + ".zip")
	check(err)
	check(zip.CreateFromVCS(file, module.Version{Path: path, Version: ver}, repo, rev, ""))
	check(file.Close())
	sum, err := dirhash.HashZip(base+".zip", dirhash.Hash1)
	check(err)
	modsum, err := dirhash.Hash1([]string{"go.mod"}, func(string) (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(mod)), nil })
	check(err)
	fmt.Println(path, ver, sum)
	fmt.Println(path, ver+"/go.mod", modsum)
}
