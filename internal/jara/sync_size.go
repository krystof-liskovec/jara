package jara

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

func uncommittedGitPaths(root string, includeIgnored bool) ([]string, error) {
	cmd := exec.Command("git", "-C", root, "status", "--porcelain=v1", "--untracked-files=all", "-z")
	output, err := cmd.Output()
	if err != nil {
		return nil, err
	}
	paths := map[string]bool{}
	records := strings.Split(string(output), "\x00")
	for i := 0; i < len(records); i++ {
		record := records[i]
		if len(record) < 4 {
			continue
		}
		paths[record[3:]] = true
		if record[0] == 'R' || record[1] == 'R' || record[0] == 'C' || record[1] == 'C' {
			i++
		}
	}
	if includeIgnored {
		cmd = exec.Command("git", "-C", root, "ls-files", "--others", "-z")
		output, err = cmd.Output()
		if err != nil {
			return nil, err
		}
		for _, name := range strings.Split(string(output), "\x00") {
			if name != "" {
				paths[name] = true
			}
		}
	}
	result := make([]string, 0, len(paths))
	for name := range paths {
		result = append(result, name)
	}
	return result, nil
}

func localFileDataSize(root string, paths []string) (int64, error) {
	rootFS, err := os.OpenRoot(root)
	if err != nil {
		return 0, err
	}
	defer rootFS.Close()
	var total int64
	for _, name := range paths {
		if err = validateWorkspacePath(name); err != nil {
			return 0, err
		}
		rel := filepath.FromSlash(name)
		if err = validateWorkspaceParents(rootFS, rel); err != nil {
			return 0, err
		}
		info, statErr := rootFS.Lstat(rel)
		if os.IsNotExist(statErr) {
			continue
		}
		if statErr != nil {
			return 0, statErr
		}
		switch {
		case info.Mode().IsRegular():
			total += info.Size()
		case info.Mode()&os.ModeSymlink != 0:
			link, readErr := rootFS.Readlink(rel)
			if readErr != nil {
				return 0, readErr
			}
			total += int64(len(link))
		}
	}
	return total, nil
}

func formatSyncSize(size int64) string {
	if size < 1024 {
		return fmt.Sprintf("%d B", size)
	}
	units := []string{"KiB", "MiB", "GiB"}
	value := float64(size)
	for _, unit := range units {
		value /= 1024
		if value < 1024 || unit == "GiB" {
			return fmt.Sprintf("%.1f %s", value, unit)
		}
	}
	return fmt.Sprintf("%d B", size)
}
