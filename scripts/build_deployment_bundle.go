package main

import (
	"archive/tar"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const stampRel = "internal/jara/build_stamp.json"

func main() {
	if len(os.Args) != 2 {
		fail("usage: go run scripts/build_deployment_bundle.go PROJECT_ROOT")
	}
	root, err := filepath.Abs(os.Args[1])
	if err != nil {
		fail(err.Error())
	}
	var files []string
	files = append(files, ".dockerignore", "go.mod", "go.sum", "scripts/build_deployment_bundle.go")
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
			if strings.HasSuffix(rel, "_test.go") || rel == "internal/jara/deploy_bundle.tar.gz" || rel == stampRel || rel == "deploy/.jara-native.json" || strings.HasPrefix(rel, "deploy/.env") || strings.HasPrefix(rel, "deploy/authorized_keys") {
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
	// The build stamp identifies this source and when it was bundled: the
	// client embeds it, and so does a host's Jara built from the bundle, so
	// a client can tell a host runs an older build.
	source := sha256.New()
	for _, rel := range files {
		data, e := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
		if e != nil {
			fail(e.Error())
		}
		fmt.Fprintf(source, "%s\x00%d\x00", rel, len(data))
		source.Write(data)
	}
	stamp, err := json.Marshal(map[string]any{"time": time.Now().Unix(), "source": hex.EncodeToString(source.Sum(nil))})
	if err != nil {
		fail(err.Error())
	}
	stampPath := os.Getenv("JARA_DEPLOY_STAMP_OUTPUT")
	if stampPath == "" {
		stampPath = filepath.Join(root, filepath.FromSlash(stampRel))
	}
	if stampPath, err = filepath.Abs(stampPath); err != nil {
		fail(err.Error())
	}
	if err = os.WriteFile(stampPath, append(stamp, '\n'), 0644); err != nil {
		fail(err.Error())
	}
	files = append(files, stampRel)
	sort.Strings(files)
	bundlePath := os.Getenv("JARA_DEPLOY_BUNDLE_OUTPUT")
	if bundlePath == "" {
		bundlePath = filepath.Join(root, "internal", "jara", "deploy_bundle.tar.gz")
	}
	bundlePath, err = filepath.Abs(bundlePath)
	if err != nil {
		fail(err.Error())
	}
	if err = os.MkdirAll(filepath.Dir(bundlePath), 0700); err != nil {
		fail(err.Error())
	}
	output, err := os.Create(bundlePath)
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
		path := filepath.Join(root, filepath.FromSlash(rel))
		if rel == stampRel {
			path = stampPath
		}
		data, e := os.ReadFile(path)
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
	if overlayPath := os.Getenv("JARA_DEPLOY_OVERLAY_OUTPUT"); overlayPath != "" {
		sourcePath, e := filepath.Abs(filepath.Join(root, "internal", "jara", "deploy_bundle.tar.gz"))
		if e != nil {
			fail(e.Error())
		}
		overlayPath, e = filepath.Abs(overlayPath)
		if e != nil {
			fail(e.Error())
		}
		stampSource, e := filepath.Abs(filepath.Join(root, filepath.FromSlash(stampRel)))
		if e != nil {
			fail(e.Error())
		}
		data, e := json.Marshal(struct {
			Replace map[string]string `json:"Replace"`
		}{Replace: map[string]string{sourcePath: bundlePath, stampSource: stampPath}})
		if e != nil {
			fail(e.Error())
		}
		if e = os.WriteFile(overlayPath, data, 0600); e != nil {
			fail(e.Error())
		}
	}
}

func fail(message string) {
	fmt.Fprintln(os.Stderr, message)
	os.Exit(1)
}
