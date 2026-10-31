package main

import (
	"archive/tar"
	"compress/gzip"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

func main() {
	if len(os.Args) != 2 {
		fail("usage: go run scripts/build_deployment_bundle.go PROJECT_ROOT")
	}
	root, err := filepath.Abs(os.Args[1])
	if err != nil {
		fail(err.Error())
	}
	var files []string
	files = append(files, ".dockerignore", "go.mod", "go.sum")
	for _, dir := range []string{"cmd", "internal", "deploy"} {
		err = filepath.WalkDir(filepath.Join(root, dir), func(path string, entry fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if entry.IsDir() {
				return nil
			}
			if !entry.Type().IsRegular() {
				return fmt.Errorf("unsupported non-file deployment asset: %s", path)
			}
			rel, e := filepath.Rel(root, path)
			if e != nil {
				return e
			}
			rel = filepath.ToSlash(rel)
			if strings.HasSuffix(rel, "_test.go") || rel == "internal/jara/deploy_bundle.tar.gz" || rel == "deploy/.jara-native.json" || strings.HasPrefix(rel, "deploy/.env") || strings.HasPrefix(rel, "deploy/authorized_keys") {
				return nil
			}
			files = append(files, rel)
			return nil
		})
		if err != nil {
			fail(err.Error())
		}
	}
	sort.Strings(files)
	output, err := os.Create(filepath.Join(root, "internal", "jara", "deploy_bundle.tar.gz"))
	if err != nil {
		fail(err.Error())
	}
	zipper, err := gzip.NewWriterLevel(output, gzip.BestCompression)
	if err != nil {
		fail(err.Error())
	}
	zipper.Header.ModTime = time.Unix(0, 0)
	archive := tar.NewWriter(zipper)
	for _, rel := range files {
		data, e := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
		if e != nil {
			fail(e.Error())
		}
		header := &tar.Header{Name: rel, Mode: 0644, Size: int64(len(data)), ModTime: time.Unix(0, 0), Typeflag: tar.TypeReg, Format: tar.FormatPAX}
		if e = archive.WriteHeader(header); e != nil {
			fail(e.Error())
		}
		if _, e = archive.Write(data); e != nil {
			fail(e.Error())
		}
	}
	if err = archive.Close(); err != nil {
		fail(err.Error())
	}
	if err = zipper.Close(); err != nil {
		fail(err.Error())
	}
	if err = output.Close(); err != nil {
		fail(err.Error())
	}
}

func fail(message string) {
	fmt.Fprintln(os.Stderr, message)
	os.Exit(1)
}
