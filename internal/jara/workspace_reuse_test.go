package jara

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"gitea.liskovec.dev/krystof.liskovec/jara/internal/fsutil"
)

func reuseFixtureRepo(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	reuseGit(t, root, "init", "-b", "main")
	for name, content := range map[string]string{"local.txt": "base", "remote.txt": "base", "deleted.txt": "base"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
	}
	reuseGit(t, root, "add", ".")
	reuseGit(t, root, "-c", "user.name=Test", "-c", "user.email=test@example.com", "commit", "-m", "base")
	reuseGit(t, root, "remote", "add", "origin", "https://example.com/team/jara.git")
	return root
}

func reuseGit(t *testing.T, root string, args ...string) {
	t.Helper()
	output, err := exec.Command("git", append([]string{"-C", root}, args...)...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, output)
	}
}

func TestExistingCheckoutOptionsMatchRepositoryIdentity(t *testing.T) {
	workspaces := []Workspace{
		{ID: "a", Ready: true, URL: "git@EXAMPLE.com:team/jara.git", SourceHost: "laptop"},
		{ID: "b", Ready: true, URL: "https://example.com/team/jara/", SourceHost: "desktop"},
		{ID: "c", Ready: false, URL: "https://example.com/team/jara.git"},
		{ID: "d", Ready: true, URL: "https://other.com/team/jara.git"},
		{ID: "e", Ready: true, URL: "https://example.com/other/jara.git"},
		{ID: "f", Ready: true},
		{ID: "../bad", Ready: true, URL: "https://example.com/team/jara.git"},
	}
	matches, items := existingCheckoutOptions(workspaces, "https://example.com/team/jara.git")
	if len(matches) != 2 || len(items) != 3 || matches[0].ID != "b" || matches[1].ID != "a" {
		t.Fatalf("wrong checkout options: %+v, %+v", matches, items)
	}
	if items[0].Title == items[1].Title || !strings.Contains(items[0].Title, "desktop") || items[2].Kind != "new" {
		t.Fatalf("indistinguishable or missing options: %+v", items)
	}
	if found, _ := existingCheckoutOptions(workspaces, "https://example.com/team/another.git"); len(found) != 0 {
		t.Fatal("offered unrelated repository")
	}
}

func TestExistingBaselinePreservesRemoteEditsAndDetectsConflicts(t *testing.T) {
	t.Setenv("JARA_CONFIG", filepath.Join(t.TempDir(), "config.json"))
	root := reuseFixtureRepo(t)
	if err := os.WriteFile(filepath.Join(root, "local.txt"), []byte("local edit"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(root, "deleted.txt")); err != nil {
		t.Fatal(err)
	}
	binding := Binding{Remote: "work", Workspace: "existing"}
	if err := seedExistingBaseline(binding, root); err != nil {
		t.Fatal(err)
	}
	state, _ := syncStatePath(binding, root)
	data, err := os.ReadFile(state)
	if err != nil {
		t.Fatal(err)
	}
	var baseline Baseline
	if err = json.Unmarshal(data, &baseline); err != nil {
		t.Fatal(err)
	}
	local, err := snapshot(root)
	if err != nil {
		t.Fatal(err)
	}
	remote := Snapshot{"local.txt": file("base"), "remote.txt": file("remote edit"), "deleted.txt": file("base")}
	update, err := planUpdate(local, remote, baseline.Local, baseline.Remote, false)
	if err != nil || len(update.Files) != 1 || string(update.Files["local.txt"].Data) != "local edit" {
		t.Fatalf("local edit could not be sent safely: %+v, %v", update, err)
	}
	if err = planDeletes(&update, local, remote, baseline.Local, baseline.Remote, false, map[string]bool{}); err != nil {
		t.Fatal(err)
	}
	if _, ok := update.Deletes["deleted.txt"]; !ok {
		t.Fatal("initial local deletion was not tracked")
	}
	remote["local.txt"] = file("conflicting remote edit")
	if _, err = planUpdate(local, remote, baseline.Local, baseline.Remote, false); err == nil {
		t.Fatal("initial sync would overwrite a conflicting remote edit")
	}
	if pending, deletions, err := localPushChangesPending(binding, root, false, true); err != nil || !pending || !deletions {
		t.Fatalf("missing initial sync prompt: %v %v %v", pending, deletions, err)
	}
	if err = seedExistingBaseline(binding, root); err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(state)
	if string(data) != string(after) {
		t.Fatal("reconnecting reset the sync history")
	}
}

func TestCommittedManifestIgnoresArchiveAttributesAndIncludesFileModes(t *testing.T) {
	root := reuseFixtureRepo(t)
	for name, content := range map[string]string{
		".gitattributes": "remote.txt export-ignore\nlocal.txt export-subst\n",
		".jaraignore":    "ignored.txt\n",
		"ignored.txt":    "excluded",
		"local.txt":      "id=$Format:%H$\n",
		"script.sh":      "#!/bin/sh\n",
	} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Chmod(filepath.Join(root, "script.sh"), 0755); err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" {
		if err := os.Symlink("local.txt", filepath.Join(root, "link")); err != nil {
			t.Fatal(err)
		}
	}
	reuseGit(t, root, "add", ".")
	reuseGit(t, root, "-c", "user.name=Test", "-c", "user.email=test@example.com", "commit", "-m", "attributes")
	base, err := committedManifest(root, gitHead(root))
	if err != nil {
		t.Fatal(err)
	}
	current, err := manifestWithIgnored(root, false)
	if err != nil {
		t.Fatal(err)
	}
	for name := range current {
		if sig(current, name) != sig(base, name) {
			t.Fatalf("incorrect committed metadata for %s: %+v / %+v", name, current[name], base[name])
		}
	}
	if _, ok := base["ignored.txt"]; ok {
		t.Fatal("included .jaraignore exclusion")
	}
	if _, ok := base["remote.txt"]; !ok {
		t.Fatal("Git export-ignore incorrectly removed a tracked sync file")
	}
}

func TestOpenLinksExistingCheckoutAndRemembersAssociation(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell SSH fixture")
	}
	root := reuseFixtureRepo(t)
	dir := t.TempDir()
	t.Setenv("JARA_CONFIG", filepath.Join(dir, "config.json"))
	t.Setenv("TERM", "dumb")
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	workspace := Workspace{ID: "existing", Name: "jara", URL: "https://example.com/team/jara.git", Branch: "main", Ready: true}
	list, _ := json.Marshal([]Workspace{workspace})
	info, _ := json.Marshal(workspace)
	script := "#!/bin/sh\ncase \"$*\" in\n" +
		"*" + rpcToken(Request{Op: "workspace-list"}) + "*) printf '%s' " + quote(string(list)) + ";;\n" +
		"*" + rpcToken(Request{Op: "info", ID: workspace.ID}) + "*) printf '%s' " + quote(string(info)) + ";;\n" +
		"*_open*) exit 0;;\n*) echo 'unexpected RPC: workspace should be reused' >&2; exit 1;;\nesac\n"
	if err := os.WriteFile(filepath.Join(dir, "ssh"), []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	c := Config{Default: "work", Remotes: map[string]Remote{"work": {Host: "test"}}}
	if err := fsutil.WriteJSON(os.Getenv("JARA_CONFIG"), c); err != nil {
		t.Fatal(err)
	}
	input, err := os.CreateTemp(dir, "input")
	if err != nil {
		t.Fatal(err)
	}
	defer input.Close()
	if _, err = input.WriteString("1\n"); err != nil {
		t.Fatal(err)
	}
	if _, err = input.Seek(0, 0); err != nil {
		t.Fatal(err)
	}
	original := os.Stdin
	os.Stdin = input
	defer func() { os.Stdin = original }()
	if err = Run([]string{"--interface", "codex", "--source", root}); err != nil {
		t.Fatal(err)
	}
	saved, err := readConfig()
	if err != nil {
		t.Fatal(err)
	}
	root, _ = filepath.EvalSymlinks(root)
	key := workspaceAssociationKey("work", root, workspace.URL)
	if saved.Bindings[root].Workspace != workspace.ID || saved.Associations[key].Workspace != workspace.ID {
		t.Fatalf("existing checkout was not linked: %+v", saved)
	}
	saved.Bindings[root] = Binding{Remote: "work", Workspace: "previous"}
	saved.Associations[key] = saved.Bindings[root]
	if err = saveConfig(saved); err != nil {
		t.Fatal(err)
	}
	if _, err = input.Seek(0, 0); err != nil {
		t.Fatal(err)
	}
	if err = Run([]string{"--interface", "codex", "--choose-workspace", "--source", root}); err != nil {
		t.Fatal(err)
	}
	saved, err = readConfig()
	if err != nil || saved.Bindings[root].Workspace != workspace.ID || saved.Associations[key].Workspace != workspace.ID {
		t.Fatalf("folder could not be relinked: %+v, %v", saved, err)
	}
}

func TestSeparateCheckoutNeverReusesTheLocalPathWorkspaceID(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell SSH fixture")
	}
	root := reuseFixtureRepo(t)
	dir := t.TempDir()
	capture := filepath.Join(dir, "capture")
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	script := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> " + quote(capture) + "\nexit 1\n"
	if err := os.WriteFile(filepath.Join(dir, "ssh"), []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	origin := "https://example.com/team/jara.git"
	for i := 0; i < 2; i++ {
		if _, err := createClientWorkspace(Remote{Host: "test"}, "work", root, origin, false, false, true); err == nil {
			t.Fatal("expected the stub to stop workspace creation")
		}
	}
	data, err := os.ReadFile(capture)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, call := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		args := strings.Fields(call)
		var q Request
		if err := decode(strings.Trim(args[len(args)-1], "'"), &q); err != nil {
			t.Fatal(err)
		}
		if q.Op != "create" || !strings.HasPrefix(q.ID, workspaceID(root, origin)+"-") || seen[q.ID] {
			t.Fatalf("separate checkout reused an existing identity: %+v", q)
		}
		seen[q.ID] = true
	}
	if len(seen) != 2 {
		t.Fatalf("expected two separate workspace identities, got %v", seen)
	}
}
