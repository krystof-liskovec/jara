// Package portable locates agent-native conversation files and rewrites the
// checkout paths inside them, so a conversation can continue on a machine
// whose checkout lives somewhere else.
//
// Neither Claude Code's session JSONL nor Codex's rollout JSONL is a
// documented interchange format. Callers must treat an import as best effort
// and keep a fallback (Jara's own transcript) when the native file is missing
// or rejected by the agent.
package portable

import (
	"bytes"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// MaxNativeFile bounds a single native conversation file that Jara moves.
const MaxNativeFile = 48 << 20

var backendIDRE = regexp.MustCompile(`^[A-Za-z0-9_-]{1,80}$`)

// ValidBackendID reports whether id is safe to use in a file name.
func ValidBackendID(id string) bool { return backendIDRE.MatchString(id) }

// ClaudeHome is Claude Code's configuration directory.
func ClaudeHome() string {
	if dir := os.Getenv("CLAUDE_CONFIG_DIR"); dir != "" {
		return dir
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".claude")
}

// CodexHome is Codex's state directory.
func CodexHome() string {
	if dir := os.Getenv("CODEX_HOME"); dir != "" {
		return dir
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".codex")
}

// ClaudeProjectName is the directory name Claude Code uses for a working
// directory: every character other than an ASCII letter or digit becomes "-".
func ClaudeProjectName(cwd string) string {
	var b strings.Builder
	for _, r := range cwd {
		if r < 128 && (r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9') {
			b.WriteRune(r)
		} else {
			b.WriteByte('-')
		}
	}
	return b.String()
}

// ClaudeSessionPath is where Claude Code keeps session id started in cwd.
func ClaudeSessionPath(cwd, id string) string {
	return filepath.Join(ClaudeHome(), "projects", ClaudeProjectName(cwd), id+".jsonl")
}

// ClaudeSessionExists reports whether Claude Code can resume id from cwd.
func ClaudeSessionExists(cwd, id string) bool {
	if !ValidBackendID(id) {
		return false
	}
	info, err := os.Stat(ClaudeSessionPath(cwd, id))
	return err == nil && info.Mode().IsRegular()
}

// IndexCodexRollouts maps thread IDs to rollout paths relative to CodexHome
// in one directory walk. Rollout names end in "-<thread UUID>.jsonl".
func IndexCodexRollouts() map[string]string {
	home := CodexHome()
	index := map[string]string{}
	for _, dir := range []string{"archived_sessions", "sessions"} { // active wins
		_ = filepath.WalkDir(filepath.Join(home, dir), func(path string, entry fs.DirEntry, err error) error {
			if err != nil || entry.IsDir() {
				return nil
			}
			name, ok := strings.CutSuffix(entry.Name(), ".jsonl")
			if !ok || !strings.HasPrefix(name, "rollout-") || len(name) < 36 {
				return nil
			}
			if rel, relErr := filepath.Rel(home, path); relErr == nil {
				index[name[len(name)-36:]] = filepath.ToSlash(rel)
			}
			return nil
		})
	}
	return index
}

// CodexRolloutDestination validates a rollout path relative to CodexHome and
// returns where to write it. Archived rollouts come back as active sessions.
func CodexRolloutDestination(rel, id string) (string, error) {
	rel = filepath.ToSlash(filepath.Clean(filepath.FromSlash(rel)))
	rel = strings.TrimPrefix(rel, "archived_")
	if !strings.HasPrefix(rel, "sessions/") || strings.Contains(rel, "..") || !strings.HasSuffix(rel, "-"+id+".jsonl") {
		return "", errors.New("unexpected Codex rollout path " + rel)
	}
	return filepath.Join(CodexHome(), filepath.FromSlash(rel)), nil
}

// RewritePath replaces the directory from with to wherever from appears as a
// whole path or path prefix. "/a/b" is replaced in "/a/b" and "/a/b/c" but
// not in "/a/bc". JSON-escaped slashes are handled too.
func RewritePath(data []byte, from, to string) []byte {
	from, to = strings.TrimRight(from, "/"), strings.TrimRight(to, "/")
	if from == "" || to == "" || from == to {
		return data
	}
	data = rewriteExact(data, []byte(from), []byte(to))
	escapedFrom := strings.ReplaceAll(from, "/", `\/`)
	if escapedFrom != from {
		data = rewriteExact(data, []byte(escapedFrom), []byte(strings.ReplaceAll(to, "/", `\/`)))
	}
	return data
}

func rewriteExact(data, from, to []byte) []byte {
	if !bytes.Contains(data, from) {
		return data
	}
	out := make([]byte, 0, len(data))
	for {
		index := bytes.Index(data, from)
		if index < 0 {
			return append(out, data...)
		}
		end := index + len(from)
		if end < len(data) && pathByte(data[end]) {
			out = append(out, data[:end]...)
		} else {
			out = append(append(out, data[:index]...), to...)
		}
		data = data[end:]
	}
}

// pathByte reports whether b continues a path component.
func pathByte(b byte) bool {
	return b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z' || b >= '0' && b <= '9' || b == '.' || b == '_' || b == '-'
}

// WriteFileAtomic writes data to path through a temporary file.
func WriteFileAtomic(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	temp, err := os.CreateTemp(filepath.Dir(path), ".jara-import-")
	if err != nil {
		return err
	}
	name := temp.Name()
	defer os.Remove(name)
	if _, err = temp.Write(data); err != nil {
		temp.Close()
		return err
	}
	if err = temp.Chmod(0600); err != nil {
		temp.Close()
		return err
	}
	if err = temp.Close(); err != nil {
		return err
	}
	return os.Rename(name, path)
}
