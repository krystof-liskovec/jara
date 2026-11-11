package jara

import (
	"os/exec"
	"reflect"
	"strings"
	"testing"

	"gitea.liskovec.dev/krystof.liskovec/jara/internal/fsutil"
)

func TestDuplicateProjectLabelsIdentifyCheckouts(t *testing.T) {
	for _, test := range []struct {
		name       string
		workspaces []Workspace
		want       []string
	}{
		{"devices", []Workspace{
			{ID: "a", Name: "jara", SourceHost: "laptop"},
			{ID: "b", Name: "jara", SourceHost: "desktop"},
		}, []string{"jara · laptop", "jara · desktop"}},
		{"directories on one device", []Workspace{
			{ID: "a", Name: "jara", SourceHost: "laptop", SourcePath: "/home/me/projects/jara"},
			{ID: "b", Name: "jara", SourceHost: "laptop", SourcePath: "/home/me/scratch/jara"},
		}, []string{"jara · laptop:projects/jara", "jara · laptop:scratch/jara"}},
		{"windows source paths", []Workspace{
			{ID: "a", Name: "jara", SourceHost: "pc", SourcePath: `C:\projects\jara`},
			{ID: "b", Name: "jara", SourceHost: "pc", SourcePath: `C:\scratch\jara`},
		}, []string{"jara · pc:projects/jara", "jara · pc:scratch/jara"}},
		{"existing workspaces", []Workspace{
			{ID: "a", Name: "jara", Created: "2026-11-01T10:00:00Z"},
			{ID: "b", Name: "jara", Created: "2026-11-02T10:00:00Z"},
		}, []string{"jara · created 2026-11-01 10:00Z", "jara · created 2026-11-02 10:00Z"}},
		{"same creation minute", []Workspace{
			{ID: "w-aaaaaaaa", Name: "jara", Created: "2026-11-01T10:00:00Z"},
			{ID: "w-bbbbbbbb", Name: "jara", Created: "2026-11-01T10:00:01Z"},
		}, []string{"jara · created 2026-11-01 10:00Z · aaaaaaaa", "jara · created 2026-11-01 10:00Z · bbbbbbbb"}},
		{"unique names", []Workspace{
			{ID: "a", Name: "jara", SourceHost: "laptop"},
			{ID: "b", Name: "other", SourceHost: "desktop"},
		}, []string{"jara", "other"}},
		{"missing provenance", []Workspace{
			{ID: "w-aaaaaaaa", Name: "jara"},
			{ID: "w-bbbbbbbb", Name: "jara"},
		}, []string{"jara · checkout aaaaaaaa", "jara · checkout bbbbbbbb"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			projects := make([]conversationProject, len(test.workspaces))
			for i, workspace := range test.workspaces {
				projects[i] = conversationProject{Workspace: workspace}
			}
			labelDuplicateProjects(projects)
			var got []string
			for _, p := range projects {
				got = append(got, projectName(p))
			}
			if !reflect.DeepEqual(got, test.want) {
				t.Fatalf("labels = %q, want %q", got, test.want)
			}
			// A change in activity order must not rename either checkout.
			projects[0], projects[1] = projects[1], projects[0]
			labelDuplicateProjects(projects)
			if projectName(projects[0]) != got[1] || projectName(projects[1]) != got[0] {
				t.Fatal("labels changed with ordering")
			}
		})
	}
}

func TestProjectProvenancePersistsAndBrowserReadsCurrentBranch(t *testing.T) {
	t.Setenv("JARA_DATA", t.TempDir())
	q := Request{Op: "create", ID: "w-copy", Name: "jara", SourceHost: "laptop", SourcePath: "/projects/jara"}
	if err := remoteRPC(q); err != nil {
		t.Fatal(err)
	}
	w, err := workspaceInfo(q.ID)
	if err != nil {
		t.Fatal(err)
	}
	if w.SourceHost != q.SourceHost || w.SourcePath != q.SourcePath {
		t.Fatalf("provenance lost: %+v", w)
	}
	if output, err := exec.Command("git", "init", "-b", "feature", w.Path).CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, output)
	}
	w.Ready, w.Branch, w.URL = true, "old-branch", "https://example.com/team/jara.git"
	if err := fsutil.WriteJSON(w.Path+".json", w); err != nil {
		t.Fatal(err)
	}
	workspaces, err := readBrowserWorkspaces()
	if err != nil || len(workspaces) != 1 {
		t.Fatalf("browser workspaces: %+v, %v", workspaces, err)
	}
	if workspaces[0].Branch != "feature" {
		t.Fatalf("stale branch displayed: %q", workspaces[0].Branch)
	}
	detail := projectDetail(conversationProject{Workspace: workspaces[0]})
	if !strings.Contains(detail, "feature") || !strings.Contains(detail, q.SourcePath) {
		t.Fatalf("missing checkout details: %q", detail)
	}
}
