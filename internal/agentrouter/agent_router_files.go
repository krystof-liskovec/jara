package agentrouter

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// The composer's @ completion browses folders like a shell: `@src/` lists
// what src holds. The router answers because it runs where the workspace
// is, on this device, a remote host or another device.

const folderListLimit = 2000

// FolderEntry is one entry of a listed folder.
type FolderEntry struct {
	Name string `json:"name"`
	Dir  bool   `json:"dir,omitempty"`
}

// ListFolder serves jara/files/list: the entries of path, relative to cwd
// unless absolute or under ~/, folders first.
func ListFolder(params json.RawMessage) ([]FolderEntry, error) {
	var request struct {
		CWD  string `json:"cwd"`
		Path string `json:"path"`
	}
	if err := json.Unmarshal(params, &request); err != nil {
		return nil, err
	}
	folder := request.Path
	if rest, ok := strings.CutPrefix(folder, "~/"); ok || folder == "~" {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, err
		}
		folder = filepath.Join(home, rest)
	} else if !filepath.IsAbs(folder) {
		folder = filepath.Join(request.CWD, folder)
	}
	dir, err := os.Open(folder)
	if err != nil {
		return nil, err
	}
	defer dir.Close()
	found, err := dir.ReadDir(folderListLimit)
	if err != nil && len(found) == 0 {
		return []FolderEntry{}, nil
	}
	entries := make([]FolderEntry, 0, len(found))
	for _, entry := range found {
		isDir := entry.IsDir()
		if entry.Type()&os.ModeSymlink != 0 {
			info, statErr := os.Stat(filepath.Join(folder, entry.Name()))
			isDir = statErr == nil && info.IsDir()
		}
		entries = append(entries, FolderEntry{Name: entry.Name(), Dir: isDir})
	}
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].Dir != entries[j].Dir {
			return entries[i].Dir
		}
		return strings.ToLower(entries[i].Name) < strings.ToLower(entries[j].Name)
	})
	return entries, nil
}
