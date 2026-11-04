package jara

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
)

func file(s string) File {
	b := []byte(s)
	h := sha256.Sum256(b)
	return File{Data: b, Hash: hex.EncodeToString(h[:]), Mode: 0644}
}
func TestTransferConflictAndDestinationEdits(t *testing.T) {
	base := Snapshot{"a": file("base")}
	local := Snapshot{"a": file("local")}
	remote := Snapshot{"a": file("remote")}
	if _, err := planUpdate(local, remote, base, base, false); err == nil {
		t.Fatal("two-sided edit must conflict")
	}
	u, err := planUpdate(base, remote, base, base, false)
	if err != nil || len(u.Files) != 0 {
		t.Fatalf("unchanged source overwrote destination: %v", err)
	}
	u, err = planUpdate(local, remote, base, base, true)
	if err != nil || string(u.Files["a"].Data) != "local" {
		t.Fatal("explicit source choice failed")
	}
}

func TestGitFileEquivalenceIgnoresOrdinaryPermissionsOnly(t *testing.T) {
	content := file("same")
	for _, modes := range [][2]uint32{{0600, 0644}, {0400, 0664}, {0700, 0755}, {0711, 0750}} {
		left, right := content, content
		left.Mode, right.Mode = modes[0], modes[1]
		if !filesEquivalent(left, right) {
			t.Errorf("Git-equivalent modes %04o and %04o differ", modes[0], modes[1])
		}
	}
	nonExecutable, executable := content, content
	nonExecutable.Mode, executable.Mode = 0644, 0755
	if filesEquivalent(nonExecutable, executable) {
		t.Fatal("executable-bit change was ignored")
	}
	changedContent := file("different")
	changedContent.Mode = nonExecutable.Mode
	if filesEquivalent(nonExecutable, changedContent) {
		t.Fatal("content change was ignored")
	}
	linkA := File{Link: "target", Hash: "same-link", Mode: 0}
	linkB := File{Link: "target", Hash: "same-link", Mode: 0777}
	if !filesEquivalent(linkA, linkB) || filesEquivalent(linkA, File{Link: "other", Hash: "same-link"}) {
		t.Fatal("symlink equivalence did not preserve link target")
	}
	if !snapshotFilesEquivalent(Snapshot{}, Snapshot{}, "missing") || snapshotFilesEquivalent(Snapshot{}, Snapshot{"missing": content}, "missing") {
		t.Fatal("snapshot equivalence mishandled absence")
	}
}

func TestPlanUpdateUsesGitEquivalenceButKeepsRawRaceSignatures(t *testing.T) {
	baseSource, baseDest := file("base"), file("base")
	baseSource.Mode, baseDest.Mode = 0644, 0600
	source, dest := file("base"), file("base")
	source.Mode, dest.Mode = 0600, 0644
	update, err := planUpdate(Snapshot{"a": source}, Snapshot{"a": dest}, Snapshot{"a": baseSource}, Snapshot{"a": baseDest}, false)
	if err != nil || len(update.Files) != 0 {
		t.Fatalf("permission-only differences prompted sync: files=%v err=%v", update.Files, err)
	}

	source = file("new content")
	source.Mode = 0755
	dest.Mode = 0600
	update, err = planUpdate(Snapshot{"a": source}, Snapshot{"a": dest}, Snapshot{"a": baseSource}, Snapshot{"a": baseDest}, false)
	if err != nil || len(update.Files) != 1 || update.Expected["a"] != signature(dest) {
		t.Fatalf("executable update or raw expected signature lost: update=%+v err=%v", update, err)
	}
	physical := t.TempDir()
	if err := os.WriteFile(filepath.Join(physical, "a"), dest.Data, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(physical, "a"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := applyUpdate(physical, update); err == nil {
		t.Fatal("raw expected signature failed to catch a destination race")
	}

	deleteUpdate := Update{Files: Snapshot{}, Expected: map[string]string{}}
	deleteDest, deleteBase := file("same content"), file("same content")
	deleteDest.Mode, deleteBase.Mode = 0600, 0644
	if err := planDeletes(&deleteUpdate, Snapshot{}, Snapshot{"gone": deleteDest}, Snapshot{"gone": deleteBase}, Snapshot{"gone": deleteBase}, false, nil); err != nil {
		t.Fatalf("permission-only deletion difference conflicted: %v", err)
	}
	if deleteUpdate.Deletes["gone"] != signature(deleteDest) {
		t.Fatal("deletion race guard did not preserve the raw destination signature")
	}
	deleteRoot := t.TempDir()
	if err := os.WriteFile(filepath.Join(deleteRoot, "gone"), deleteDest.Data, 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(deleteRoot, "gone"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := applyUpdate(deleteRoot, deleteUpdate); err == nil {
		t.Fatal("raw deletion signature failed to catch a destination race")
	}

	source = file("source edit")
	dest = file("destination edit")
	base := file("base")
	if _, err := planUpdate(Snapshot{"a": source}, Snapshot{"a": dest}, Snapshot{"a": base}, Snapshot{"a": base}, false); err == nil {
		t.Fatal("divergent content changes stopped conflicting")
	}
	source, dest, base = file("same link"), file("same link"), file("same link")
	source.Link, dest.Link, base.Link = "source", "destination", "old"
	if _, err := planUpdate(Snapshot{"a": source}, Snapshot{"a": dest}, Snapshot{"a": base}, Snapshot{"a": base}, false); err == nil {
		t.Fatal("divergent symlink target changes stopped conflicting")
	}
}

func TestGitPushPullWithRemotePermissionDifferencesDoesNotPromptSync(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Git permission semantics require Unix modes")
	}
	baseDir := t.TempDir()
	bare := filepath.Join(baseDir, "origin.git")
	remoteWork := filepath.Join(baseDir, "remote-develop")
	localWork := filepath.Join(baseDir, "local")
	gitSyncTestCommand(t, "", "init", "--bare", "--initial-branch=main", bare)
	gitSyncTestCommand(t, "", "clone", bare, remoteWork)
	gitSyncTestCommand(t, remoteWork, "config", "user.name", "Sync Test")
	gitSyncTestCommand(t, remoteWork, "config", "user.email", "sync@example.invalid")
	if err := os.MkdirAll(filepath.Join(remoteWork, "src"), 0700); err != nil {
		t.Fatal(err)
	}
	const count = 12
	for i := 0; i < count; i++ {
		name := filepath.Join(remoteWork, "src", fmt.Sprintf("module_%02d.go", i))
		if err := os.WriteFile(name, []byte(fmt.Sprintf("package module%d\nconst value%d = %d\n", i, i, i)), 0644); err != nil {
			t.Fatal(err)
		}
	}
	gitSyncTestCommand(t, remoteWork, "add", ".")
	gitSyncTestCommand(t, remoteWork, "commit", "-m", "base")
	gitSyncTestCommand(t, remoteWork, "push", "-u", "origin", "main")
	gitSyncTestCommand(t, "", "clone", bare, localWork)

	for i := 0; i < count; i++ {
		name := filepath.Join(remoteWork, "src", fmt.Sprintf("module_%02d.go", i))
		if err := os.Chmod(name, 0600); err != nil {
			t.Fatal(err)
		}
	}
	baseLocal, err := snapshot(localWork)
	if err != nil {
		t.Fatal(err)
	}
	baseRemote, err := snapshot(remoteWork)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < count; i++ {
		name := filepath.Join(remoteWork, "src", fmt.Sprintf("module_%02d.go", i))
		if err := os.WriteFile(name, []byte(fmt.Sprintf("package module%d\nconst value%d = %d // remote\n", i, i, i+1)), 0600); err != nil {
			t.Fatal(err)
		}
	}
	gitSyncTestCommand(t, remoteWork, "add", ".")
	gitSyncTestCommand(t, remoteWork, "commit", "-m", "remote develop changes")
	gitSyncTestCommand(t, remoteWork, "push")
	gitSyncTestCommand(t, localWork, "pull", "--ff-only")
	for i := 0; i < count; i++ {
		name := filepath.Join(remoteWork, "src", fmt.Sprintf("module_%02d.go", i))
		if err := os.Chmod(name, 0600); err != nil {
			t.Fatal(err)
		}
		name = filepath.Join(localWork, "src", fmt.Sprintf("module_%02d.go", i))
		if err := os.Chmod(name, 0644); err != nil {
			t.Fatal(err)
		}
	}

	local, err := snapshot(localWork)
	if err != nil {
		t.Fatal(err)
	}
	remote, err := snapshot(remoteWork)
	if err != nil {
		t.Fatal(err)
	}
	if local["src/module_00.go"].Mode == remote["src/module_00.go"].Mode {
		t.Fatalf("test setup did not produce distinct ordinary permission modes: local=%04o remote=%04o", local["src/module_00.go"].Mode, remote["src/module_00.go"].Mode)
	}
	update, err := planUpdate(remote, local, baseRemote, baseLocal, false)
	if err != nil || len(update.Files) != 0 {
		t.Fatalf("clean Git pull was reported as Jara sync changes: files=%d err=%v", len(update.Files), err)
	}
}

func gitSyncTestCommand(t *testing.T, dir string, args ...string) []byte {
	t.Helper()
	cmd := exec.Command("git", args...)
	if dir != "" {
		cmd.Dir = dir
	}
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s failed: %v", args[0], err)
	}
	return output
}
func TestTransferPreservesDestinationOnlyFiles(t *testing.T) {
	u, err := planUpdate(Snapshot{"new": file("new")}, Snapshot{"only": file("keep")}, Snapshot{}, Snapshot{}, false)
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	if err = os.WriteFile(filepath.Join(root, "only"), []byte("keep"), 0644); err != nil {
		t.Fatal(err)
	}
	if err = applyUpdate(root, u); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(root, "only"))
	if err != nil || string(b) != "keep" {
		t.Fatal("destination-only file lost")
	}
}
func TestTransferPreflightRejectsRaceAndTraversal(t *testing.T) {
	root := t.TempDir()
	os.WriteFile(filepath.Join(root, "a"), []byte("changed"), 0644)
	u := Update{Files: Snapshot{"a": file("replacement"), "b": file("new")}, Expected: map[string]string{"a": signature(file("old")), "b": ""}}
	if err := applyUpdate(root, u); err == nil {
		t.Fatal("expected-value check failed")
	}
	if _, err := os.Stat(filepath.Join(root, "b")); !os.IsNotExist(err) {
		t.Fatal("partial preflight write")
	}
	for _, p := range []string{"../escape", "/absolute", "a/../../escape", ".git/config", ".GIT/config", "a\\..\\escape", "C:/escape"} {
		if _, err := safePath(root, p); err == nil {
			t.Errorf("accepted %q", p)
		}
	}
}
func TestTransferRejectsSymlinkParents(t *testing.T) {
	root, outside := t.TempDir(), t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, "link")); err != nil {
		t.Skip(err)
	}
	if _, err := safePath(root, "link/file"); err == nil {
		t.Fatal("symlink parent accepted")
	}
	if validLink("a", "../outside") {
		t.Fatal("external link accepted")
	}
	if !validLink("dir/link", "../file") {
		t.Fatal("internal link rejected")
	}
}
func TestJaraIgnoreAndBinaryTransfer(t *testing.T) {
	root := t.TempDir()
	os.WriteFile(filepath.Join(root, ".jaraignore"), []byte("build/\n**/*.secret\n!keep.secret\n"), 0644)
	os.Mkdir(filepath.Join(root, "build"), 0755)
	for _, p := range []string{"build/output", "hide.secret", "keep.secret", "binary"} {
		os.WriteFile(filepath.Join(root, p), []byte{0, 255, 10}, 0644)
	}
	s, err := snapshot(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"build/output", "hide.secret"} {
		if _, ok := s[p]; ok {
			t.Fatalf("included ignored %s", p)
		}
	}
	dest := t.TempDir()
	u, err := planUpdate(s, Snapshot{}, Snapshot{}, Snapshot{}, false)
	if err != nil {
		t.Fatal(err)
	}
	if err = applyUpdate(dest, u); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(dest, "binary"))
	if err != nil || len(b) != 3 || b[1] != 255 {
		t.Fatal("binary transfer changed contents")
	}
}

func TestExplicitDeletionProtectsEditsAndExcludedFiles(t *testing.T) {
	base := Snapshot{"gone": file("base")}
	dest := Snapshot{"gone": file("changed")}
	u := Update{Files: Snapshot{}, Expected: map[string]string{}}
	if err := planDeletes(&u, Snapshot{}, dest, base, base, false, nil); err == nil {
		t.Fatal("deleted edited destination")
	}
	if err := planDeletes(&u, Snapshot{}, dest, base, base, true, map[string]bool{"gone": true}); err != nil || len(u.Deletes) != 0 {
		t.Fatal("excluded existing file treated as deleted")
	}
	dest = Snapshot{"gone": file("base")}
	if err := planDeletes(&u, Snapshot{}, dest, base, base, false, nil); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	os.WriteFile(filepath.Join(root, "gone"), []byte("base"), 0644)
	if err := os.Chmod(filepath.Join(root, "gone"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := applyUpdate(root, u); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "gone")); !os.IsNotExist(err) {
		t.Fatal("explicit deletion did not apply")
	}
}

func TestExcludedDestinationCannotBeSilentlyOverwritten(t *testing.T) {
	root := t.TempDir()
	os.WriteFile(filepath.Join(root, ".jaraignore"), []byte("secret\n"), 0644)
	os.WriteFile(filepath.Join(root, "secret"), []byte("keep"), 0644)
	u := Update{Files: Snapshot{"secret": file("overwrite")}, Expected: map[string]string{"secret": ""}}
	if err := applyUpdate(root, u); err == nil {
		t.Fatal("ignored destination was overwritten")
	}
	b, _ := os.ReadFile(filepath.Join(root, "secret"))
	if string(b) != "keep" {
		t.Fatal("destination changed")
	}
}

func TestRootedWorkspaceWriteCannotEscapeThroughSymlinkParent(t *testing.T) {
	workspace, outside := t.TempDir(), t.TempDir()
	sentinel := filepath.Join(outside, "sentinel")
	if err := os.WriteFile(sentinel, []byte("host-safe"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(workspace, "escape")); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(workspace)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	if err := rootWriteAtomic(root, filepath.Join("escape", "sentinel"), []byte("owned"), 0600); err == nil {
		t.Fatal("write followed an external symlink")
	}
	if err := rootSymlinkAtomic(root, sentinel, filepath.Join("escape", "link")); err == nil {
		t.Fatal("symlink creation followed an external parent")
	}
	got, err := os.ReadFile(sentinel)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "host-safe" {
		t.Fatalf("outside file changed: %q", got)
	}
}

func TestTransferCannotReachGitMetadataThroughSymlinkParents(t *testing.T) {
	workspace := t.TempDir()
	gitDir := filepath.Join(workspace, ".git")
	if err := os.Mkdir(gitDir, 0700); err != nil {
		t.Fatal(err)
	}
	protected := filepath.Join(gitDir, "config")
	if err := os.WriteFile(protected, []byte("protected"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(".git", filepath.Join(workspace, "alias")); err != nil {
		t.Skip(err)
	}
	u := Update{
		Files:    Snapshot{"alias/config": file("overwritten")},
		Expected: map[string]string{"alias/config": signature(file("protected"))},
	}
	if err := applyUpdate(workspace, u); err == nil {
		t.Fatal("transfer followed an in-workspace symlink into .git")
	}
	got, err := os.ReadFile(protected)
	if err != nil || string(got) != "protected" {
		t.Fatalf(".git/config changed: %q, %v", got, err)
	}
	if validLink("alias", ".git") || validLink("alias", ".GiT") || validLink("dir/alias", "../.git/config") {
		t.Fatal("accepted a symbolic link into .git")
	}
}
