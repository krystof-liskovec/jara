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
)

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
