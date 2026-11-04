package jara

import (
	"bytes"
	"encoding/json"
	"os"
	"sort"
	"strings"
)

// reconcileLocalPushChanges refreshes only the portions of the saved baseline
// that both endpoints currently share. It runs only after the local summary
// has found pending changes, so the common clean-workspace path stays offline.
func reconcileLocalPushChanges(r Remote, b Binding, root string, includeIgnored, isGit bool) (pending, deletions bool, size int64, err error) {
	local, err := manifestWithIgnored(root, includeIgnored)
	if err != nil {
		return false, false, 0, err
	}
	remote, err := syncRemoteManifest(r, b, includeIgnored)
	if err != nil {
		return false, false, 0, err
	}
	state, err := syncStatePath(b, root)
	if err != nil {
		return false, false, 0, err
	}
	baseline := Baseline{Local: Snapshot{}, Remote: Snapshot{}}
	data, err := os.ReadFile(state)
	if err == nil {
		if err = json.Unmarshal(data, &baseline); err != nil {
			return false, false, 0, err
		}
	} else if !os.IsNotExist(err) {
		return false, false, 0, err
	}
	if baseline.Local == nil {
		baseline.Local = Snapshot{}
	}
	if baseline.Remote == nil {
		baseline.Remote = Snapshot{}
	}

	changed := refreshSharedFileBaseline(&baseline, local, remote)
	deletionsChanged, _ := refreshSharedDeletionBaseline(r, b, root, &baseline, local, remote)
	// A failed existence check cannot establish that an omitted path was
	// deleted rather than excluded, so preserve those baseline entries.
	changed = changed || deletionsChanged
	if changed {
		if err = writeJSON(state, baseline); err != nil {
			return false, false, 0, err
		}
	}
	return localPushChangesSummary(b, root, includeIgnored, isGit)
}

func syncRemoteManifest(r Remote, b Binding, includeIgnored bool) (Snapshot, error) {
	var remote Snapshot
	err := rpc(r, Request{Op: "manifest", ID: b.Workspace, IncludeIgnored: includeIgnored}, nil, &remote)
	if err == nil {
		return remote, nil
	}
	if !strings.Contains(err.Error(), "unknown RPC operation") {
		return nil, err
	}
	// Older hosts return full snapshots; keep them usable until their Jara
	// deployment is upgraded.
	if err = rpc(r, Request{Op: "snapshot", ID: b.Workspace, IncludeIgnored: includeIgnored}, nil, &remote); err != nil {
		return nil, err
	}
	return remote, nil
}

func refreshSharedFileBaseline(baseline *Baseline, local, remote Snapshot) bool {
	changed := false
	for name, localFile := range local {
		remoteFile, exists := remote[name]
		if !exists || !filesEquivalent(localFile, remoteFile) {
			continue
		}
		if !snapshotFilesEquivalent(baseline.Local, Snapshot{name: localFile}, name) {
			localFile.Data = nil
			baseline.Local[name] = localFile
			changed = true
		}
		if !snapshotFilesEquivalent(baseline.Remote, Snapshot{name: remoteFile}, name) {
			remoteFile.Data = nil
			baseline.Remote[name] = remoteFile
			changed = true
		}
	}
	return changed
}

func refreshSharedDeletionBaseline(r Remote, b Binding, root string, baseline *Baseline, local, remote Snapshot) (bool, error) {
	paths := make([]string, 0)
	seen := map[string]bool{}
	for name := range baseline.Local {
		if !seen[name] && snapshotFilesEquivalent(local, remote, name) && !hasSnapshotFile(local, name) {
			paths = append(paths, name)
			seen[name] = true
		}
	}
	for name := range baseline.Remote {
		if !seen[name] && snapshotFilesEquivalent(local, remote, name) && !hasSnapshotFile(local, name) {
			paths = append(paths, name)
			seen[name] = true
		}
	}
	if len(paths) == 0 {
		return false, nil
	}
	sort.Strings(paths)
	localPresent, err := existingPaths(root, paths)
	if err != nil {
		return false, err
	}
	payload, err := json.Marshal(paths)
	if err != nil {
		return false, err
	}
	var remotePresent map[string]bool
	if err = rpc(r, Request{Op: "exists", ID: b.Workspace}, bytes.NewReader(payload), &remotePresent); err != nil {
		return false, err
	}
	changed := false
	for _, name := range paths {
		if localPresent[name] || remotePresent[name] {
			continue
		}
		if _, exists := baseline.Local[name]; exists {
			delete(baseline.Local, name)
			changed = true
		}
		if _, exists := baseline.Remote[name]; exists {
			delete(baseline.Remote, name)
			changed = true
		}
	}
	return changed, nil
}

func hasSnapshotFile(snapshot Snapshot, name string) bool {
	_, ok := snapshot[name]
	return ok
}
