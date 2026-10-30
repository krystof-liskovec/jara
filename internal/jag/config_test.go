package jag

import (
	"os"
	"path/filepath"
	"testing"
)

func TestConfigRefusesConcurrentLostUpdates(t *testing.T) {
	t.Setenv("JAG_CONFIG", filepath.Join(t.TempDir(), "config.json"))
	a, err := readConfig()
	if err != nil {
		t.Fatal(err)
	}
	b, err := readConfig()
	if err != nil {
		t.Fatal(err)
	}
	a.Default = "first"
	if err = saveConfig(a); err != nil {
		t.Fatal(err)
	}
	b.Default = "second"
	if err = saveConfig(b); err == nil {
		t.Fatal("stale configuration replaced newer changes")
	}
	actual, err := readConfig()
	if err != nil || actual.Default != "first" {
		t.Fatal("configuration was lost")
	}
}

func TestCopyAssociationReopensFromSubdirectory(t *testing.T) {
	root := t.TempDir()
	sub := filepath.Join(root, "sub")
	os.Mkdir(sub, 0755)
	c := Config{Bindings: map[string]Binding{root: {Remote: "work", Workspace: "w-copy"}}}
	got, rel, _ := workspaceContext(c, sub, false)
	if got != root || rel != "sub" {
		t.Fatalf("association not found: %s %s", got, rel)
	}
}
