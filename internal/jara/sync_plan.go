package jara

import (
	"fmt"
	"sort"
	"strings"
)

func planUpdate(source, dest, baseSource, baseDest Snapshot, force bool) (Update, error) {
	u := Update{Files: Snapshot{}, Expected: map[string]string{}}
	var conflicts []string
	for name, f := range source {
		if snapshotFilesEquivalent(source, dest, name) {
			continue
		}
		// A destination-only edit is left in place, rather than overwritten by an unchanged source.
		sourceChanged := !snapshotFilesEquivalent(source, baseSource, name)
		destChanged := !snapshotFilesEquivalent(dest, baseDest, name)
		if !sourceChanged && !force {
			continue
		}
		if destChanged && sig(dest, name) != "" && !force {
			conflicts = append(conflicts, name)
			continue
		}
		u.Files[name] = f
		u.Expected[name] = sig(dest, name)
	}
	if len(conflicts) > 0 {
		sort.Strings(conflicts)
		return Update{}, fmt.Errorf("conflicting destination files (nothing transferred): %s; inspect both sides, then --force to choose source", strings.Join(conflicts, ", "))
	}
	return u, nil
}

func planDeletes(u *Update, source, dest, baseSource, baseDest Snapshot, force bool, present map[string]bool) error {
	u.Deletes = map[string]string{}
	for p := range baseSource {
		if present[p] {
			continue
		}
		if _, ok := source[p]; ok {
			continue
		}
		if _, ok := dest[p]; !ok {
			continue
		}
		if !snapshotFilesEquivalent(dest, baseDest, p) && !force {
			return fmt.Errorf("deletion conflicts with destination edit: %s; nothing transferred", p)
		}
		u.Deletes[p] = sig(dest, p)
	}
	return nil
}
