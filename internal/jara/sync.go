package jara

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
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
	Local  Snapshot
	Remote Snapshot
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
func planUpdate(source, dest, baseSource, baseDest Snapshot, force bool) (Update, error) {
	u := Update{Files: Snapshot{}, Expected: map[string]string{}}
	var conflicts []string
	for name, f := range source {
		if sig(source, name) == sig(dest, name) {
			continue
		}
		// A destination-only edit is left in place, rather than overwritten by an unchanged source.
		sourceChanged := sig(source, name) != sig(baseSource, name)
		destChanged := sig(dest, name) != sig(baseDest, name)
		if !sourceChanged && !force {
			continue
		}
		if destChanged && sig(dest, name) != "" && !force {
			conflicts = append(conflicts, name)
			continue
		}
		u.Files[name] = f
		u.Expected[name] = sig(dest, name)
	}
	if len(conflicts) > 0 {
		sort.Strings(conflicts)
		return Update{}, fmt.Errorf("conflicting destination files (nothing transferred): %s; inspect both sides, then --force to choose source", strings.Join(conflicts, ", "))
	}
	return u, nil
}

func planDeletes(u *Update, source, dest, baseSource, baseDest Snapshot, force bool, present map[string]bool) error {
	u.Deletes = map[string]string{}
	for p := range baseSource {
		if present[p] {
			continue
		}
		if _, ok := source[p]; ok {
			continue
		}
		if _, ok := dest[p]; !ok {
			continue
		}
		if sig(dest, p) != sig(baseDest, p) && !force {
			return fmt.Errorf("deletion conflicts with destination edit: %s; nothing transferred", p)
		}
		u.Deletes[p] = sig(dest, p)
	}
	return nil
}
func applyUpdate(root string, u Update) error {
	rootFS, err := os.OpenRoot(root)
	if err != nil {
		return err
	}
	defer rootFS.Close()
	// Preflight every path and expected value before modifying any file.
	for name, expected := range u.Deletes {
		actual, e := physicalSignature(rootFS, name)
		if e != nil {
			return e
		}
		if actual != expected {
			return fmt.Errorf("destination changed before deletion: %s", name)
		}
		if _, ok := u.Files[name]; ok {
			return fmt.Errorf("invalid update both writes and deletes %s", name)
		}
	}
	for name, f := range u.Files {
		if err = validateWorkspacePath(name); err != nil {
			return err
		}
		if f.Link != "" && !validLink(name, f.Link) {
			return fmt.Errorf("unsafe symbolic link %s", name)
		}
		sum := sha256.Sum256(f.Data)
		if hex.EncodeToString(sum[:]) != f.Hash {
			return fmt.Errorf("content hash mismatch for %s", name)
		}
		expected, ok := u.Expected[name]
		actual, e := physicalSignature(rootFS, name)
		if e != nil {
			return e
		}
		if !ok || actual != expected {
			return fmt.Errorf("destination changed during transfer: %s", name)
		}
	}
	names := make([]string, 0, len(u.Files))
	for p := range u.Files {
		names = append(names, p)
	}
	sort.Strings(names)
	for _, name := range names {
		f := u.Files[name]
		name = filepath.FromSlash(name)
		if err = rootFS.MkdirAll(filepath.Dir(name), 0700); err != nil {
			return err
		}
		if f.Link != "" {
			err = rootSymlinkAtomic(rootFS, f.Link, name)
			if err != nil {
				return err
			}
			continue
		}
		err = rootWriteAtomic(rootFS, name, f.Data, os.FileMode(f.Mode)&0777)
		if err != nil {
			return err
		}
	}
	for name := range u.Deletes {
		if e := rootFS.Remove(filepath.FromSlash(name)); e != nil && !os.IsNotExist(e) {
			return e
		}
	}
	return nil
}

func physicalSignature(root *os.Root, name string) (string, error) {
	if err := validateWorkspacePath(name); err != nil {
		return "", err
	}
	name = filepath.FromSlash(name)
	if err := validateWorkspaceParents(root, name); err != nil {
		return "", err
	}
	info, err := root.Lstat(name)
	if os.IsNotExist(err) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	f := File{Mode: uint32(info.Mode().Perm())}
	if info.Mode()&os.ModeSymlink != 0 {
		f.Link, err = root.Readlink(name)
		f.Mode = 0
	} else if info.Mode().IsRegular() {
		if info.Size() > maxTransfer {
			return "", fmt.Errorf("destination file too large: %s", name)
		}
		f.Data, err = rootReadFile(root, name, maxTransfer)
	} else {
		return "", fmt.Errorf("destination is not a regular file or link: %s", name)
	}
	if err != nil {
		return "", err
	}
	h := sha256.Sum256(f.Data)
	f.Hash = hex.EncodeToString(h[:])
	return signature(f), nil
}

// Reject existing symlink parents so an otherwise safe relative name cannot
// reach protected paths such as .git through an in-workspace link.
func validateWorkspaceParents(root *os.Root, name string) error {
	parts := strings.Split(name, string(filepath.Separator))
	current := ""
	for _, part := range parts[:len(parts)-1] {
		if current == "" {
			current = part
		} else {
			current = filepath.Join(current, part)
		}
		info, err := root.Lstat(current)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return fmt.Errorf("path traverses a link or file: %s", filepath.ToSlash(name))
		}
	}
	return nil
}

func rootReadFile(root *os.Root, name string, limit int64) ([]byte, error) {
	f, err := root.Open(name)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("not a regular file: %s", name)
	}
	if info.Size() > limit {
		return nil, fmt.Errorf("file exceeds the 64 MiB transfer limit: %s", name)
	}
	b, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > limit {
		return nil, fmt.Errorf("file exceeds the 64 MiB transfer limit: %s", name)
	}
	return b, nil
}

func rootTempName(dir string) (string, error) {
	var nonce [12]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return "", err
	}
	return filepath.Join(dir, fmt.Sprintf(".jara-transfer-%x", nonce[:])), nil
}
func rootWriteAtomic(root *os.Root, name string, data []byte, mode os.FileMode) error {
	dir := filepath.Dir(name)
	for attempt := 0; attempt < 8; attempt++ {
		tmp, err := rootTempName(dir)
		if err != nil {
			return err
		}
		f, err := root.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if os.IsExist(err) {
			continue
		}
		if err != nil {
			return err
		}
		_, err = f.Write(data)
		if err == nil {
			err = f.Chmod(mode)
		}
		if closeErr := f.Close(); err == nil {
			err = closeErr
		}
		if err == nil {
			err = root.Rename(tmp, name)
		}
		_ = root.Remove(tmp)
		return err
	}
	return fmt.Errorf("could not allocate temporary workspace file")
}
func rootSymlinkAtomic(root *os.Root, target, name string) error {
	for attempt := 0; attempt < 8; attempt++ {
		tmp, err := rootTempName(filepath.Dir(name))
		if err != nil {
			return err
		}
		err = root.Symlink(target, tmp)
		if os.IsExist(err) {
			continue
		}
		if err != nil {
			return err
		}
		err = root.Rename(tmp, name)
		_ = root.Remove(tmp)
		return err
	}
	return fmt.Errorf("could not allocate temporary workspace link")
}
func syncWorkspace(r Remote, b Binding, root, direction string, force bool, options ...bool) error {
	includeIgnored, deleteMissing := false, false
	if len(options) > 0 {
		includeIgnored = options[0]
	}
	if len(options) > 1 {
		deleteMissing = options[1]
	}
	local, err := snapshotWithIgnored(root, includeIgnored)
	if err != nil {
		return err
	}
	var remote Snapshot
	if err = rpc(r, Request{Op: "snapshot", ID: b.Workspace, IncludeIgnored: includeIgnored}, nil, &remote); err != nil {
		return err
	}
	state, err := syncStatePath(b, root)
	if err != nil {
		return err
	}
	baseline := Baseline{Local: Snapshot{}, Remote: Snapshot{}}
	data, err := os.ReadFile(state)
	if err == nil {
		if err = json.Unmarshal(data, &baseline); err != nil {
			return err
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	var u Update
	if direction == "push" {
		u, err = planUpdate(local, remote, baseline.Local, baseline.Remote, force)
	} else {
		u, err = planUpdate(remote, local, baseline.Remote, baseline.Local, force)
	}
	if err != nil {
		return err
	}
	if deleteMissing {
		base := baseline.Local
		if direction == "pull" {
			base = baseline.Remote
		}
		paths := make([]string, 0, len(base))
		for p := range base {
			paths = append(paths, p)
		}
		var present map[string]bool
		if direction == "push" {
			present, err = existingPaths(root, paths)
		} else {
			payload, _ := json.Marshal(paths)
			err = rpc(r, Request{Op: "exists", ID: b.Workspace}, bytes.NewReader(payload), &present)
		}
		if err != nil {
			return err
		}
		if direction == "push" {
			err = planDeletes(&u, local, remote, baseline.Local, baseline.Remote, force, present)
		} else {
			err = planDeletes(&u, remote, local, baseline.Remote, baseline.Local, force, present)
		}
		if err != nil {
			return err
		}
	}
	if direction == "push" {
		payload, e := json.Marshal(u)
		if e != nil {
			return e
		}
		if err = rpc(r, Request{Op: "apply", ID: b.Workspace, Force: force}, bytes.NewReader(payload), nil); err != nil {
			return err
		}
		for p, f := range u.Files {
			remote[p] = f
		}
		for p := range u.Deletes {
			delete(remote, p)
		}
	} else {
		// Pull is also prohibited while Jara-managed agents are active remotely.
		if err = rpc(r, Request{Op: "guard", ID: b.Workspace, Force: force}, nil, nil); err != nil {
			return err
		}
		if err = applyUpdate(root, u); err != nil {
			return err
		}
		for p, f := range u.Files {
			local[p] = f
		}
		for p := range u.Deletes {
			delete(local, p)
		}
	}
	// Preserve each endpoint's previous baseline for unresolved, one-sided differences.
	next := Baseline{Local: metadata(local), Remote: metadata(remote)}
	all := map[string]bool{}
	for p := range local {
		all[p] = true
	}
	for p := range remote {
		all[p] = true
	}
	for p := range all {
		if sig(local, p) != sig(remote, p) {
			if f, ok := baseline.Local[p]; ok {
				next.Local[p] = f
			} else {
				delete(next.Local, p)
			}
			if f, ok := baseline.Remote[p]; ok {
				next.Remote[p] = f
			} else {
				delete(next.Remote, p)
			}
		}
	}
	if err = writeJSON(state, next); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "Transferred %d file(s), deleted %d previously synchronized file(s).\n", len(u.Files), len(u.Deletes))
	return nil
}

func syncStatePath(b Binding, root string) (string, error) {
	config, err := configPath()
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256([]byte(b.Remote + "\n" + root + "\n" + b.Workspace))
	return filepath.Join(filepath.Dir(config), "sync", hex.EncodeToString(sum[:])+".json"), nil
}

func seedCloneBaseline(r Remote, b Binding, root string) error {
	var remote Snapshot
	if err := rpc(r, Request{Op: "snapshot", ID: b.Workspace}, nil, &remote); err != nil {
		return err
	}
	state, err := syncStatePath(b, root)
	if err != nil {
		return err
	}
	return writeJSON(state, Baseline{Local: metadata(remote), Remote: metadata(remote)})
}

func existingPaths(root string, paths []string) (map[string]bool, error) {
	rootFS, err := os.OpenRoot(root)
	if err != nil {
		return nil, err
	}
	defer rootFS.Close()
	present := map[string]bool{}
	for _, p := range paths {
		if e := validateWorkspacePath(p); e != nil {
			return nil, e
		}
		_, e := rootFS.Lstat(filepath.FromSlash(p))
		if e == nil {
			present[p] = true
		} else if !os.IsNotExist(e) {
			return nil, e
		}
	}
	return present, nil
}
func repositoryBundle(root string) (io.Reader, func(), error) {
	f, err := os.CreateTemp("", "jara-bundle-*")
	if err != nil {
		return nil, func() {}, err
	}
	p := f.Name()
	f.Close()
	os.Remove(p)
	cmd := exec.Command("git", "-C", root, "bundle", "create", p, "HEAD")
	cmd.Stderr = os.Stderr
	if err = cmd.Run(); err != nil {
		os.Remove(p)
		return nil, func() {}, err
	}
	f, err = os.Open(p)
	if err != nil {
		os.Remove(p)
		return nil, func() {}, err
	}
	return f, func() { f.Close(); os.Remove(p) }, nil
}
