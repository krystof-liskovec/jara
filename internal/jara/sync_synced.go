package jara

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
)

// maxSyncedRecordBytes bounds the synced record a push sends inside its
// request, which travels as an SSH command argument. Paths beyond the budget
// are left out, so they fall back to blocking as before.
const maxSyncedRecordBytes = 48 << 10

// syncedSignature identifies a file's content as last synced, compactly
// enough to send: Git's tracked-file semantics, as in filesEquivalent.
func syncedSignature(f File) string {
	if f.Link != "" {
		return "l:" + f.Link
	}
	hash := f.Hash
	if len(hash) > 32 {
		hash = hash[:32]
	}
	if f.Mode&0111 != 0 {
		return "x:" + hash
	}
	return "f:" + hash
}

// agreedSyncRecord returns the signature of every file both sides held with
// the same content at the last sync. A checkout that still holds that content
// has not changed the file since, and the other side already has it, so
// incoming commits may replace it without losing work. It returns nil when no
// sync record exists or it cannot be read.
func agreedSyncRecord(b Binding, root string) map[string]string {
	state, err := syncStatePath(b, root)
	if err != nil {
		return nil
	}
	data, err := os.ReadFile(state)
	if err != nil {
		return nil
	}
	var baseline Baseline
	if json.Unmarshal(data, &baseline) != nil {
		return nil
	}
	agreed := map[string]string{}
	for name, local := range baseline.Local {
		if remote, ok := baseline.Remote[name]; ok && filesEquivalent(local, remote) {
			agreed[name] = syncedSignature(remote)
		}
	}
	return agreed
}

// syncedRecordFor keeps the agreed record of the paths commits from..to
// change, within maxSyncedRecordBytes.
func syncedRecordFor(b Binding, root, from, to string) map[string]string {
	agreed := agreedSyncRecord(b, root)
	if len(agreed) == 0 {
		return nil
	}
	paths, err := commitPathsChanged(root, from, to)
	if err != nil {
		return nil
	}
	sort.Strings(paths)
	record, size := map[string]string{}, 0
	for _, name := range paths {
		signature, ok := agreed[name]
		if !ok {
			continue
		}
		if size += len(name) + len(signature) + 8; size > maxSyncedRecordBytes {
			break
		}
		record[name] = signature
	}
	if len(record) == 0 {
		return nil
	}
	return record
}

// unchangedSinceSync reports whether the checkout's file still holds the
// content recorded for it at the last sync.
func unchangedSinceSync(root, name string, synced map[string]string) bool {
	want, ok := synced[name]
	if !ok || validateWorkspacePath(name) != nil {
		return false
	}
	rootFS, err := os.OpenRoot(root)
	if err != nil {
		return false
	}
	defer rootFS.Close()
	rel := filepath.FromSlash(name)
	info, err := rootFS.Lstat(rel)
	if err != nil {
		return false
	}
	f := File{Mode: uint32(info.Mode().Perm())}
	switch {
	case info.Mode()&os.ModeSymlink != 0:
		if f.Link, err = rootFS.Readlink(rel); err != nil {
			return false
		}
	case info.Mode().IsRegular():
		if f.Hash, err = hashRootFile(rootFS, rel); err != nil {
			return false
		}
	default:
		return false
	}
	return syncedSignature(f) == want
}
