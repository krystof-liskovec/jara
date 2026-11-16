package jara

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"krystof.liskovec/jara/internal/fsutil"
)

type promptReadTracker struct{ read bool }

func (r *promptReadTracker) Read(p []byte) (int, error) {
	r.read = true
	copy(p, "n\n")
	return 2, nil
}

func startReconcileGitRepo(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	if runtime.GOOS == "windows" {
		t.Skip("shell SSH fixture")
	}
	reuseGit(t, root, "init", "-b", "main")
	for name, contents := range files {
		if err := os.WriteFile(filepath.Join(root, name), []byte(contents), 0644); err != nil {
			t.Fatal(err)
		}
	}
	reuseGit(t, root, "add", ".")
	reuseGit(t, root, "-c", "user.name=Test", "-c", "user.email=test@example.com", "commit", "-m", "base")
	return root
}

func commitReconcileChange(t *testing.T, root, message string) {
	t.Helper()
	reuseGit(t, root, "add", ".")
	reuseGit(t, root, "-c", "user.name=Test", "-c", "user.email=test@example.com", "commit", "-m", message)
}

func installReconcileSSH(t *testing.T, remote Snapshot, exists map[string]bool, logPath string) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	manifestJSON, err := json.Marshal(remote)
	if err != nil {
		t.Fatal(err)
	}
	existsJSON, err := json.Marshal(exists)
	if err != nil {
		t.Fatal(err)
	}
	manifestToken := rpcToken(Request{Op: "manifest", ID: "workspace"})
	existsToken := rpcToken(Request{Op: "exists", ID: "workspace"})
	script := "#!/bin/sh\ncase \"$*\" in\n" +
		"*" + manifestToken + "*) printf '%s' " + quote(string(manifestJSON)) + "; printf 'manifest\\n' >>" + quote(logPath) + ";;\n" +
		"*" + existsToken + "*) cat >/dev/null; printf '%s' " + quote(string(existsJSON)) + "; printf 'exists\\n' >>" + quote(logPath) + ";;\n" +
		"*) printf '%s\\n' \"$*\" >>" + quote(logPath) + "; exit 1;;\nesac\n"
	if err = os.WriteFile(filepath.Join(dir, "ssh"), []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
}

func installUnavailableReconcileSSH(t *testing.T, logPath string) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	manifestToken := rpcToken(Request{Op: "manifest", ID: "workspace"})
	script := "#!/bin/sh\ncase \"$*\" in\n*" + manifestToken + "*) echo 'manifest unavailable' >&2; printf 'manifest\\n' >>" + quote(logPath) + "; exit 1;;\n*) printf '%s\\n' \"$*\" >>" + quote(logPath) + "; exit 1;;\nesac\n"
	if err := os.WriteFile(filepath.Join(dir, "ssh"), []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
}

func TestSessionPrecheckReconcilesPulledCommitWithoutPrompt(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell SSH fixture")
	}
	t.Setenv("JARA_CONFIG", filepath.Join(t.TempDir(), "config.json"))
	root := startReconcileGitRepo(t, map[string]string{"file.txt": "A"})
	binding := Binding{Remote: "work", Workspace: "workspace"}
	base, err := manifestWithIgnored(root, false)
	if err != nil {
		t.Fatal(err)
	}
	if err = writeSyncBaseline(t, binding, root, base); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(root, "file.txt"), []byte("B"), 0644); err != nil {
		t.Fatal(err)
	}
	commitReconcileChange(t, root, "pulled remote commit")
	current, err := manifestWithIgnored(root, false)
	if err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(t.TempDir(), "requests.log")
	installReconcileSSH(t, metadata(current), nil, logPath)
	reader := &promptReadTracker{}
	if err = syncLocalChangesBeforeSession(Remote{Host: "fixture"}, binding, root, "work", false, true, bufio.NewReader(reader)); err != nil {
		t.Fatal(err)
	}
	if reader.read {
		t.Fatal("already-pulled remote commit triggered a sync prompt")
	}
	requests, err := os.ReadFile(logPath)
	if err != nil || strings.TrimSpace(string(requests)) != "manifest" {
		t.Fatalf("unexpected remote operations: %q, %v", requests, err)
	}
	state, err := syncStatePath(binding, root)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(state)
	if err != nil {
		t.Fatal(err)
	}
	var baseline Baseline
	if err = json.Unmarshal(data, &baseline); err != nil {
		t.Fatal(err)
	}
	if baseline.LocalHead != gitHead(root) || !snapshotFilesEquivalent(baseline.Local, current, "file.txt") {
		t.Fatalf("shared baseline did not catch up: %+v", baseline)
	}
}

func TestReconciliationLeavesUnsharedLocalCommitPendingAndCountsOnlyIt(t *testing.T) {
	t.Setenv("JARA_CONFIG", filepath.Join(t.TempDir(), "config.json"))
	root := startReconcileGitRepo(t, map[string]string{"shared.txt": "A", "local.txt": "A"})
	binding := Binding{Remote: "work", Workspace: "workspace"}
	base, err := manifestWithIgnored(root, false)
	if err != nil {
		t.Fatal(err)
	}
	if err = writeSyncBaseline(t, binding, root, base); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(root, "shared.txt"), []byte("remote B"), 0644); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(root, "local.txt"), []byte("local C"), 0644); err != nil {
		t.Fatal(err)
	}
	commitReconcileChange(t, root, "local commit")
	remote := Snapshot{"shared.txt": file("remote B"), "local.txt": file("A")}
	installReconcileSSH(t, metadata(remote), nil, filepath.Join(t.TempDir(), "requests.log"))
	for attempt := 0; attempt < 2; attempt++ {
		pending, deletions, size, reconcileErr := reconcileLocalPushChanges(Remote{Host: "fixture"}, binding, root, false, true)
		if reconcileErr != nil || !pending || deletions || size != int64(len("local C")) {
			t.Fatalf("attempt %d lost the unshared local change: pending=%v deletions=%v size=%d err=%v", attempt, pending, deletions, size, reconcileErr)
		}
	}
	state, err := syncStatePath(binding, root)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(state)
	if err != nil {
		t.Fatal(err)
	}
	var baseline Baseline
	if err = json.Unmarshal(data, &baseline); err != nil {
		t.Fatal(err)
	}
	if baseline.LocalHead == gitHead(root) {
		t.Fatal("partial reconciliation advanced LocalHead while local changes remained")
	}
	if !snapshotFilesEquivalent(baseline.Local, base, "local.txt") || !snapshotFilesEquivalent(baseline.Remote, base, "local.txt") {
		t.Fatal("divergent local change baseline was overwritten")
	}
}

func TestReconciliationClearsOnlyProvenSharedDeletions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell SSH fixture")
	}
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "excluded.txt"), []byte("kept"), 0644); err != nil {
		t.Fatal(err)
	}
	baseline := Baseline{
		Local:  Snapshot{"deleted.txt": file("gone"), "excluded.txt": file("excluded")},
		Remote: Snapshot{"deleted.txt": file("gone"), "excluded.txt": file("excluded")},
	}
	logPath := filepath.Join(t.TempDir(), "requests.log")
	installReconcileSSH(t, Snapshot{}, map[string]bool{"excluded.txt": true}, logPath)
	changed, err := refreshSharedDeletionBaseline(Remote{Host: "fixture"}, Binding{Workspace: "workspace"}, root, &baseline, Snapshot{}, Snapshot{})
	if err != nil || !changed {
		t.Fatalf("converged deletion was not reconciled: changed=%v err=%v", changed, err)
	}
	if _, ok := baseline.Local["deleted.txt"]; ok {
		t.Fatal("proven deletion remained in the baseline")
	}
	if _, ok := baseline.Remote["deleted.txt"]; ok {
		t.Fatal("proven remote deletion remained in the baseline")
	}
	if _, ok := baseline.Local["excluded.txt"]; !ok {
		t.Fatal("existing excluded local path was dropped from the baseline")
	}
	if _, ok := baseline.Remote["excluded.txt"]; !ok {
		t.Fatal("existing excluded remote path was dropped from the baseline")
	}
}

func TestSessionPrecheckKeepsPromptWhenRemoteManifestIsUnavailable(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell SSH fixture")
	}
	t.Setenv("JARA_CONFIG", filepath.Join(t.TempDir(), "config.json"))
	root := startReconcileGitRepo(t, map[string]string{"file.txt": "A"})
	binding := Binding{Remote: "work", Workspace: "workspace"}
	base, err := manifestWithIgnored(root, false)
	if err != nil {
		t.Fatal(err)
	}
	if err = writeSyncBaseline(t, binding, root, base); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(root, "file.txt"), []byte("local B"), 0644); err != nil {
		t.Fatal(err)
	}
	commitReconcileChange(t, root, "local-only commit")
	logPath := filepath.Join(t.TempDir(), "requests.log")
	installUnavailableReconcileSSH(t, logPath)
	reader := &promptReadTracker{}
	if err = syncLocalChangesBeforeSession(Remote{Host: "fixture"}, binding, root, "work", false, true, bufio.NewReader(reader)); err != nil {
		t.Fatal(err)
	}
	if !reader.read {
		t.Fatal("remote lookup failure incorrectly suppressed the local changes prompt")
	}
	requests, err := os.ReadFile(logPath)
	if err != nil || strings.TrimSpace(string(requests)) != "manifest" {
		t.Fatalf("unexpected remote operations after unavailable manifest: %q, %v", requests, err)
	}
}

func TestReconciliationPreservesDivergentEditsForConflictDetection(t *testing.T) {
	base := Snapshot{"file.txt": file("A")}
	local := Snapshot{"file.txt": file("local B")}
	remote := Snapshot{"file.txt": file("remote C")}
	baseline := Baseline{Local: metadata(base), Remote: metadata(base)}
	if refreshSharedFileBaseline(&baseline, local, remote) {
		t.Fatal("divergent edits were treated as a shared baseline")
	}
	if !snapshotFilesEquivalent(baseline.Local, base, "file.txt") || !snapshotFilesEquivalent(baseline.Remote, base, "file.txt") {
		t.Fatal("divergent edits overwrote the saved baseline")
	}
	if _, err := planUpdate(local, remote, baseline.Local, baseline.Remote, false); err == nil {
		t.Fatal("divergent local and remote edits no longer conflict")
	}
}

func writeSyncBaseline(t *testing.T, binding Binding, root string, snapshot Snapshot) error {
	t.Helper()
	state, err := syncStatePath(binding, root)
	if err != nil {
		return err
	}
	return fsutil.WriteJSON(state, Baseline{Local: metadata(snapshot), Remote: metadata(snapshot), LocalHead: gitHead(root)})
}
