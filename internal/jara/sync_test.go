package jara

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
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
