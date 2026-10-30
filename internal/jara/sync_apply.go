package jara

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

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
