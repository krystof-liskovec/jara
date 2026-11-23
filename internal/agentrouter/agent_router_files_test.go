package agentrouter

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestListFolderPutsFoldersFirstAndFollowsLinks(t *testing.T) {
	root := t.TempDir()
	for _, dir := range []string{".config/claude", ".config/Code", "src"} {
		if err := os.MkdirAll(filepath.Join(root, dir), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, ".config", "a.toml"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(root, "src"), filepath.Join(root, ".config", "linked")); err != nil {
		t.Fatal(err)
	}
	params, _ := json.Marshal(map[string]string{"cwd": root, "path": ".config/"})
	entries, err := ListFolder(params)
	if err != nil {
		t.Fatal(err)
	}
	want := []FolderEntry{{Name: "claude", Dir: true}, {Name: "Code", Dir: true}, {Name: "linked", Dir: true}, {Name: "a.toml"}}
	if len(entries) != len(want) {
		t.Fatalf("entries: %+v", entries)
	}
	for i := range want {
		if entries[i] != want[i] {
			t.Fatalf("entries: %+v", entries)
		}
	}
	t.Setenv("HOME", root)
	params, _ = json.Marshal(map[string]string{"cwd": "/", "path": "~/src"})
	if entries, err = ListFolder(params); err != nil || len(entries) != 0 {
		t.Fatalf("home folder: %+v %v", entries, err)
	}
	params, _ = json.Marshal(map[string]string{"cwd": root, "path": "missing/"})
	if _, err = ListFolder(params); err == nil {
		t.Fatal("a missing folder listed")
	}
}
