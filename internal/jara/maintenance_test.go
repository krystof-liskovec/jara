//go:build linux

package jara

import (
	"path/filepath"
	"testing"
	"time"

	"krystof.liskovec/jara/internal/fsutil"
)

func TestWorkspaceGateBlocksOnlyDuringActiveLease(t *testing.T) {
	t.Setenv("JARA_DATA", t.TempDir())
	if err := fsutil.WriteJSON(maintenancePath(), maintenanceLease{Token: "test", Expires: time.Now().Add(time.Minute)}); err != nil {
		t.Fatal(err)
	}
	if _, err := lockWorkspace("w-test"); err == nil {
		t.Fatal("workspace operation admitted during update")
	}
	if err := fsutil.WriteJSON(filepath.Join(fsutil.DataRoot(), ".maintenance.json"), maintenanceLease{Token: "test", Expires: time.Now().Add(-time.Minute)}); err != nil {
		t.Fatal(err)
	}
	unlock, err := lockWorkspace("w-test")
	if err != nil {
		t.Fatal(err)
	}
	unlock()
}
