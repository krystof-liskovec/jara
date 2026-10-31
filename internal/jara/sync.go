package jara

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// A bounded first protocol; reject large workspaces instead of exhausting memory.
const maxTransfer = 64 << 20

const maxIgnoreFile = 1 << 20

type File struct {
	Data []byte `json:"data,omitempty"`
	Hash string `json:"hash"`
	Mode uint32 `json:"mode"`
	Link string `json:"link,omitempty"`
}

type Snapshot map[string]File

type Update struct {
	Files    Snapshot          `json:"files"`
	Expected map[string]string `json:"expected"`
	Deletes  map[string]string `json:"deletes,omitempty"`
}

type Baseline struct {
	Local     Snapshot
	Remote    Snapshot
	LocalHead string `json:"local_head,omitempty"`
}

func signature(f File) string { return fmt.Sprintf("%s:%o:%s", f.Hash, f.Mode, f.Link) }

func sig(s Snapshot, p string) string {
	f, ok := s[p]
	if !ok {
		return ""
	}
	return signature(f)
}

func metadata(s Snapshot) Snapshot {
	m := Snapshot{}
	for p, f := range s {
		f.Data = nil
		m[p] = f
	}
	return m
}

func safePath(root, name string) (string, error) {
	if err := validateWorkspacePath(name); err != nil {
		return "", err
	}
	parts := strings.Split(name, "/")
	current := root
	for _, s := range parts[:len(parts)-1] {
		current = filepath.Join(current, s)
		info, err := os.Lstat(current)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return "", err
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return "", fmt.Errorf("path traverses a link or file: %s", name)
		}
	}
	return filepath.Join(root, filepath.FromSlash(name)), nil
}

func validateWorkspacePath(name string) error {
	if name == "" || name == "." || strings.ContainsAny(name, "\\\x00") || strings.HasPrefix(name, "/") || strings.Contains(name, ":") || path.Clean(name) != name {
		return fmt.Errorf("unsafe path %q", name)
	}
	parts := strings.Split(name, "/")
	for _, s := range parts {
		if s == ".." || strings.EqualFold(s, ".git") {
			return fmt.Errorf("unsafe path %q", name)
		}
	}
	return nil
}

func validLink(name, target string) bool {
	if target == "" || strings.ContainsAny(target, "\\\x00:") || path.IsAbs(target) {
		return false
	}
	resolved := path.Clean(path.Join(path.Dir(name), target))
	if resolved == ".." || strings.HasPrefix(resolved, "../") {
		return false
	}
	for _, part := range strings.Split(resolved, "/") {
		if strings.EqualFold(part, ".git") {
			return false
		}
	}
	return true
}

func snapshot(root string) (Snapshot, error) { return snapshotWithIgnored(root, false) }

func snapshotWithIgnored(root string, includeIgnored bool) (Snapshot, error) {
	files := []string{}
	args := []string{"-C", root, "ls-files", "-z", "--cached", "--others"}
	if !includeIgnored {
		args = append(args, "--exclude-standard")
	}
	cmd := exec.Command("git", args...)
	out, err := cmd.Output()
	// Only use Git's list if root really is this checkout's root, not its parent.
	gitRoot, e := gitOutput(root, "rev-parse", "--show-toplevel")
	same := e == nil && filepath.Clean(gitRoot) == filepath.Clean(root)
	if err == nil && same {
		for _, p := range bytes.Split(out, []byte{0}) {
			if len(p) > 0 {
				files = append(files, string(p))
			}
		}
	} else {
		err = filepath.WalkDir(root, func(p string, d os.DirEntry, e error) error {
			if e != nil {
				return e
			}
			if p == root {
				return nil
			}
			rel, e := filepath.Rel(root, p)
			if e != nil {
				return e
			}
			if d.Name() == ".git" {
				if d.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
			if !d.IsDir() {
				files = append(files, filepath.ToSlash(rel))
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	rules, err := readIgnore(root)
	if err != nil {
		return nil, err
	}
	rootFS, err := os.OpenRoot(root)
	if err != nil {
		return nil, err
	}
	defer rootFS.Close()
	sort.Strings(files)
	result := Snapshot{}
	total := int64(0)
	for _, name := range files {
		if ignored(name, rules) {
			continue
		}
		if err := validateWorkspacePath(name); err != nil {
			return nil, err
		}
		rel := filepath.FromSlash(name)
		if err := validateWorkspaceParents(rootFS, rel); err != nil {
			return nil, err
		}
		info, err := rootFS.Lstat(rel)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return nil, err
		}
		f := File{Mode: uint32(info.Mode().Perm())}
		switch {
		case info.Mode()&os.ModeSymlink != 0:
			f.Link, err = rootFS.Readlink(rel)
			if err != nil {
				return nil, err
			}
			if !validLink(name, f.Link) {
				return nil, fmt.Errorf("link %s points outside workspace; exclude it in .jaraignore", name)
			}
			f.Mode = 0
		case info.Mode().IsRegular():
			total += info.Size()
			if total > maxTransfer {
				return nil, fmt.Errorf("workspace exceeds the 64 MiB transfer limit; exclude generated/large files in .jaraignore")
			}
			f.Data, err = rootReadFile(rootFS, rel, maxTransfer-total)
			if err != nil {
				return nil, err
			}
		case info.IsDir():
			return nil, fmt.Errorf("%s is a submodule/directory entry; submodule transfer is not supported yet", name)
		default:
			return nil, fmt.Errorf("cannot transfer special file %s", name)
		}
		if err != nil {
			return nil, err
		}
		sum := sha256.Sum256(f.Data)
		f.Hash = hex.EncodeToString(sum[:])
		result[name] = f
	}
	return result, nil
}

type ignoreRule struct {
	re      *regexp.Regexp
	include bool
}

func readIgnore(root string) ([]ignoreRule, error) {
	rootFS, err := os.OpenRoot(root)
	if err != nil {
		return nil, err
	}
	defer rootFS.Close()
	info, err := rootFS.Lstat(".jaraignore")
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf(".jaraignore must be a regular file")
	}
	b, err := rootReadFile(rootFS, ".jaraignore", maxIgnoreFile)
	if err != nil {
		return nil, err
	}
	var rules []ignoreRule
	for _, line := range strings.Split(string(b), "\n") {
		s := strings.TrimSpace(line)
		if s == "" || strings.HasPrefix(s, "#") {
			continue
		}
		include := strings.HasPrefix(s, "!")
		if include {
			s = s[1:]
		}
		anchored := strings.HasPrefix(s, "/")
		s = strings.Trim(s, "/")
		if s == "" {
			continue
		}
		var p strings.Builder
		if anchored || strings.Contains(s, "/") {
			p.WriteString("^")
		} else {
			p.WriteString("(^|/)")
		}
		for i := 0; i < len(s); i++ {
			switch s[i] {
			case '*':
				if i+1 < len(s) && s[i+1] == '*' {
					i++
					if i+1 < len(s) && s[i+1] == '/' {
						i++
						p.WriteString("(.*/)?")
					} else {
						p.WriteString(".*")
					}
				} else {
					p.WriteString("[^/]*")
				}
			case '?':
				p.WriteString("[^/]")
			default:
				p.WriteString(regexp.QuoteMeta(s[i : i+1]))
			}
		}
		p.WriteString("($|/)")
		re, err := regexp.Compile(p.String())
		if err != nil {
			return nil, err
		}
		rules = append(rules, ignoreRule{re, include})
	}
	return rules, nil
}

func ignored(name string, rules []ignoreRule) bool {
	excluded := false
	for _, r := range rules {
		if r.re.MatchString(name) {
			excluded = !r.include
		}
	}
	return excluded
}
