package jara

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"krystof.liskovec/jara/internal/fsutil"
)

func syncWorkspace(r Remote, b Binding, root, direction string, force bool, options ...bool) error {
	includeIgnored, deleteMissing := false, false
	if len(options) > 0 {
		includeIgnored = options[0]
	}
	if len(options) > 1 {
		deleteMissing = options[1]
	}
	advance, err := syncCommits(r, b, root, direction, force)
	if err != nil {
		return err
	}
	local, err := manifestWithIgnored(root, includeIgnored)
	if err != nil {
		return err
	}
	var remote Snapshot
	remoteHasManifest := true
	if err = rpc(r, Request{Op: "manifest", ID: b.Workspace, IncludeIgnored: includeIgnored}, nil, &remote); err != nil {
		if !strings.Contains(err.Error(), "unknown RPC operation") {
			return err
		}
		// Older hosts return full snapshots; keep them usable until their Jara deployment is upgraded.
		remoteHasManifest = false
		if err = rpc(r, Request{Op: "snapshot", ID: b.Workspace, IncludeIgnored: includeIgnored}, nil, &remote); err != nil {
			return err
		}
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
	if advance != nil {
		// Save now: a later file conflict must not leave the checkout update
		// looking like an edit on the side that was fast-forwarded.
		if err = alignCommittedBaseline(root, &baseline, advance); err != nil {
			return err
		}
		if err = fsutil.WriteJSON(state, baseline); err != nil {
			return err
		}
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
	if len(u.Files) > 0 {
		files, readErr := changedFileData(r, b, root, direction, remoteHasManifest, u.Files)
		if readErr != nil {
			return readErr
		}
		u.Files = files
	}
	if direction == "push" {
		if hasUpdate(u) {
			payload, e := json.Marshal(u)
			if e != nil {
				return e
			}
			if err = rpc(r, Request{Op: "apply", ID: b.Workspace, Force: force}, bytes.NewReader(payload), nil); err != nil {
				return err
			}
			for p, f := range metadata(u.Files) {
				remote[p] = f
			}
			for p := range u.Deletes {
				delete(remote, p)
			}
		}
	} else {
		if hasUpdate(u) {
			// Pull is also prohibited while Jara-managed agents are active remotely.
			if err = rpc(r, Request{Op: "guard", ID: b.Workspace, Force: force}, nil, nil); err != nil {
				return err
			}
			if err = applyUpdate(root, u); err != nil {
				return err
			}
			for p, f := range metadata(u.Files) {
				local[p] = f
			}
			for p := range u.Deletes {
				delete(local, p)
			}
		}
	}
	// Preserve each endpoint's previous baseline for unresolved, one-sided differences.
	next := Baseline{Local: metadata(local), Remote: metadata(remote), LocalHead: gitHead(root)}
	all := map[string]bool{}
	for p := range local {
		all[p] = true
	}
	for p := range remote {
		all[p] = true
	}
	for p := range all {
		if !snapshotFilesEquivalent(local, remote, p) {
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
	if err = fsutil.WriteJSON(state, next); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "Transferred %d file(s), deleted %d previously synchronized file(s).\n", len(u.Files), len(u.Deletes))
	return nil
}

func hasUpdate(update Update) bool {
	return len(update.Files) > 0 || len(update.Deletes) > 0
}

func changedFileData(r Remote, b Binding, root, direction string, remoteHasManifest bool, planned Snapshot) (Snapshot, error) {
	names := make([]string, 0, len(planned))
	for name := range planned {
		names = append(names, name)
	}
	var files Snapshot
	var err error
	if direction == "push" {
		files, err = readSnapshotFiles(root, names)
	} else if !remoteHasManifest {
		files = planned
	} else {
		payload, marshalErr := json.Marshal(names)
		if marshalErr != nil {
			return nil, marshalErr
		}
		err = rpc(r, Request{Op: "read-files", ID: b.Workspace}, bytes.NewReader(payload), &files)
	}
	if err != nil {
		return nil, err
	}
	for name, expected := range planned {
		actual, ok := files[name]
		if !ok || signature(actual) != signature(expected) {
			return nil, fmt.Errorf("source file changed during sync: %s; retry", name)
		}
	}
	return files, nil
}

func syncStatePath(b Binding, root string) (string, error) {
	config, err := configPath()
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256([]byte(b.Remote + "\n" + root + "\n" + b.Workspace))
	return filepath.Join(filepath.Dir(config), "sync", hex.EncodeToString(sum[:])+".json"), nil
}

func localPushChangesPending(b Binding, root string, includeIgnored, isGit bool) (pending, deletions bool, err error) {
	pending, deletions, _, err = localPushChangesSummary(b, root, includeIgnored, isGit)
	return pending, deletions, err
}

type localSyncSummary struct {
	Pending   bool
	Deletions bool
	Size      int64
	Files     []string
	Deleted   []string
}

func localPushChangesSummary(b Binding, root string, includeIgnored, isGit bool) (pending, deletions bool, size int64, err error) {
	summary, err := localPushChangesDetails(b, root, includeIgnored, isGit)
	return summary.Pending, summary.Deletions, summary.Size, err
}

func localPushChangesDetails(b Binding, root string, includeIgnored, isGit bool) (localSyncSummary, error) {
	state, err := syncStatePath(b, root)
	if err != nil {
		return localSyncSummary{}, err
	}
	data, err := os.ReadFile(state)
	if os.IsNotExist(err) {
		if !isGit {
			return localSyncSummary{}, nil
		}
		status, statusErr := gitOutput(root, "status", "--porcelain")
		if statusErr != nil || status == "" {
			return localSyncSummary{}, statusErr
		}
		paths, pathErr := uncommittedGitPaths(root, includeIgnored)
		if pathErr != nil {
			return localSyncSummary{}, pathErr
		}
		summary := localSyncSummary{Pending: true}
		present, pathErr := existingPaths(root, paths)
		if pathErr != nil {
			return localSyncSummary{}, pathErr
		}
		for _, name := range paths {
			if present[name] {
				summary.Files = append(summary.Files, name)
			} else {
				summary.Deleted = append(summary.Deleted, name)
			}
		}
		sort.Strings(summary.Files)
		sort.Strings(summary.Deleted)
		summary.Size, pathErr = localFileDataSize(root, summary.Files)
		return summary, pathErr
	}
	if err != nil {
		return localSyncSummary{}, err
	}
	var baseline Baseline
	if err = json.Unmarshal(data, &baseline); err != nil {
		return localSyncSummary{}, err
	}
	summary := localSyncSummary{}
	var head string
	if isGit && !includeIgnored {
		status, statusErr := gitOutput(root, "status", "--porcelain")
		if statusErr != nil {
			return localSyncSummary{}, statusErr
		}
		head = gitHead(root)
		if status == "" && head == baseline.LocalHead {
			return summary, nil
		}
	}
	local, err := manifestWithIgnored(root, includeIgnored)
	if err != nil {
		return localSyncSummary{}, err
	}
	for name := range local {
		if !snapshotFilesEquivalent(local, baseline.Local, name) {
			summary.Pending = true
			summary.Files = append(summary.Files, name)
		}
	}
	sort.Strings(summary.Files)
	summary.Size, err = localFileDataSize(root, summary.Files)
	if err != nil {
		return localSyncSummary{}, err
	}
	var missing []string
	for name := range baseline.Local {
		if _, exists := local[name]; !exists {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		present, presentErr := existingPaths(root, missing)
		if presentErr != nil {
			return localSyncSummary{}, presentErr
		}
		for _, name := range missing {
			if !present[name] {
				summary.Pending = true
				summary.Deletions = true
				summary.Deleted = append(summary.Deleted, name)
			}
		}
	}
	sort.Strings(summary.Deleted)
	if !summary.Pending && head != "" && head != baseline.LocalHead {
		baseline.LocalHead = head
		if err = fsutil.WriteJSON(state, baseline); err != nil {
			return localSyncSummary{}, err
		}
	}
	return summary, nil
}

func gitHead(root string) string {
	output, err := gitOutput(root, "rev-parse", "HEAD")
	if err != nil {
		return ""
	}
	return output
}

func seedCloneBaseline(r Remote, b Binding, root string) error {
	var remote Snapshot
	if err := rpc(r, Request{Op: "manifest", ID: b.Workspace}, nil, &remote); err != nil {
		if !strings.Contains(err.Error(), "unknown RPC operation") {
			return err
		}
		if err = rpc(r, Request{Op: "snapshot", ID: b.Workspace}, nil, &remote); err != nil {
			return err
		}
	}
	state, err := syncStatePath(b, root)
	if err != nil {
		return err
	}
	return fsutil.WriteJSON(state, Baseline{Local: metadata(remote), Remote: metadata(remote), LocalHead: gitHead(root)})
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

// repositoryBundle bundles HEAD, omitting history the receiver already has.
func repositoryBundle(root string, exclude ...string) (io.Reader, func(), error) {
	f, err := os.CreateTemp("", "jara-bundle-*")
	if err != nil {
		return nil, func() {}, err
	}
	p := f.Name()
	f.Close()
	os.Remove(p)
	args := []string{"-C", root, "bundle", "create", p, "HEAD"}
	for _, commit := range exclude {
		args = append(args, "^"+commit)
	}
	cmd := exec.Command("git", args...)
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
