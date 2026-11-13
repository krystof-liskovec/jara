package jara

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"krystof.liskovec/jara/internal/protocol"
)

// The persistent session answers jara/git/diff with the workspace's
// uncommitted changes: tracked files against HEAD (staged or not) and
// untracked files, most recently modified first. Outside a Git work tree it
// says so, and the interface keeps showing the agent's own edits.

const (
	chatGitDiffTimeout      = 15 * time.Second
	chatGitDiffFiles        = 100
	chatGitDiffBytes        = 1 << 20
	chatGitUntrackedMaxSize = 256 << 10
	chatGitEmptyTree        = "4b825dc642cb6eb9a060e54bf8d69288fbee4904"
)

type chatGitDiffResult struct {
	Git     bool                  `json:"git"`
	Changes []protocol.FileChange `json:"changes,omitempty"`
	Omitted int                   `json:"omitted,omitempty"` // files left out to keep the reply small
}

// chatGitDiffFlags fix the output format whatever the user's configuration:
// plain a/ and b/ prefixes (not mnemonic or none), no color, no external tool.
var chatGitDiffFlags = []string{"diff", "--no-color", "--no-ext-diff", "--src-prefix=a/", "--dst-prefix=b/"}

func chatGit(ctx context.Context, dir string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "git", append([]string{"-c", "core.quotepath=off"}, args...)...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_OPTIONAL_LOCKS=0", "GIT_TERMINAL_PROMPT=0")
	return cmd.Output()
}

func collectChatGitDiff(cwd string) (chatGitDiffResult, error) {
	ctx, cancel := context.WithTimeout(context.Background(), chatGitDiffTimeout)
	defer cancel()
	top, err := chatGit(ctx, cwd, "rev-parse", "--show-toplevel")
	if err != nil {
		return chatGitDiffResult{}, nil // not a Git work tree, or no Git at all
	}
	root := strings.TrimSpace(string(top))
	base := "HEAD"
	if _, err = chatGit(ctx, root, "rev-parse", "--verify", "-q", "HEAD"); err != nil {
		base = chatGitEmptyTree // no commits yet
	}
	diff, err := chatGit(ctx, root, append(chatGitDiffFlags, "--find-renames", base, "--")...)
	if err != nil {
		return chatGitDiffResult{Git: true}, err
	}
	changes := parseChatGitDiff(string(diff))
	untracked, err := chatGit(ctx, root, "ls-files", "--others", "--exclude-standard", "-z")
	if err != nil {
		return chatGitDiffResult{Git: true}, err
	}
	skipped := 0
	for _, path := range strings.Split(strings.TrimRight(string(untracked), "\x00"), "\x00") {
		if path == "" {
			continue
		}
		if len(changes) >= chatGitDiffFiles*2 {
			skipped++ // reading more would only be cut later
			continue
		}
		change := protocol.FileChange{Path: path, Kind: json.RawMessage(`"add"`)}
		if info, statErr := os.Lstat(filepath.Join(root, path)); statErr == nil && info.Mode().IsRegular() && info.Size() <= chatGitUntrackedMaxSize {
			// --no-index exits 1 when the files differ, which they always do.
			out, diffErr := chatGit(ctx, root, append(chatGitDiffFlags, "--no-index", "--", os.DevNull, path)...)
			var exit *exec.ExitError
			if diffErr == nil || errors.As(diffErr, &exit) && exit.ExitCode() == 1 {
				if parsed := parseChatGitDiff(string(out)); len(parsed) == 1 {
					change.Diff = parsed[0].Diff
				}
			}
		}
		changes = append(changes, change)
	}
	result := limitChatGitDiff(root, changes)
	result.Omitted += skipped
	return result, nil
}

// limitChatGitDiff orders files by modification time, newest first, and
// keeps the reply within its file and byte budgets.
func limitChatGitDiff(root string, changes []protocol.FileChange) chatGitDiffResult {
	modified := make(map[string]time.Time, len(changes))
	for _, change := range changes {
		if info, err := os.Lstat(filepath.Join(root, change.Path)); err == nil {
			modified[change.Path] = info.ModTime()
		}
	}
	sort.SliceStable(changes, func(i, j int) bool { return modified[changes[i].Path].After(modified[changes[j].Path]) })
	result := chatGitDiffResult{Git: true}
	size := 0
	for _, change := range changes {
		if len(result.Changes) >= chatGitDiffFiles || size+len(change.Diff) > chatGitDiffBytes {
			result.Omitted++
			continue
		}
		size += len(change.Diff)
		result.Changes = append(result.Changes, change)
	}
	return result
}

// parseChatGitDiff splits `git diff` output into one change per file.
func parseChatGitDiff(diff string) []protocol.FileChange {
	var changes []protocol.FileChange
	var current *protocol.FileChange
	var body strings.Builder
	finish := func() {
		if current != nil {
			current.Diff = body.String()
			changes = append(changes, *current)
		}
		body.Reset()
	}
	for _, line := range strings.SplitAfter(diff, "\n") {
		trimmed := strings.TrimRight(line, "\n")
		switch {
		case strings.HasPrefix(trimmed, "diff --git "):
			finish()
			current = &protocol.FileChange{Path: chatGitHeaderPath(trimmed), Kind: json.RawMessage(`"update"`)}
		case current == nil:
		case strings.HasPrefix(trimmed, "new file mode"):
			current.Kind = json.RawMessage(`"add"`)
		case strings.HasPrefix(trimmed, "deleted file mode"):
			current.Kind = json.RawMessage(`"delete"`)
		case strings.HasPrefix(trimmed, "rename from "):
			current.OldPath = strings.TrimPrefix(trimmed, "rename from ")
			current.Kind = json.RawMessage(`"rename"`)
		case strings.HasPrefix(trimmed, "rename to "):
			current.Path = strings.TrimPrefix(trimmed, "rename to ")
		case strings.HasPrefix(trimmed, "+++ b/"):
			current.Path = strings.TrimPrefix(trimmed, "+++ b/")
		case strings.HasPrefix(trimmed, "Binary files "):
			body.WriteString("Binary file changed\n")
		case strings.HasPrefix(trimmed, "@@"), strings.HasPrefix(trimmed, "+") && !strings.HasPrefix(trimmed, "+++"),
			strings.HasPrefix(trimmed, "-") && !strings.HasPrefix(trimmed, "---"), strings.HasPrefix(trimmed, " "),
			strings.HasPrefix(trimmed, `\`):
			body.WriteString(line)
		}
	}
	finish()
	return changes
}

// chatGitHeaderPath reads the new path from "diff --git a/X b/Y", which the
// "+++ b/" or "rename to" line later confirms for unusual names.
func chatGitHeaderPath(header string) string {
	rest := strings.TrimPrefix(header, "diff --git ")
	if index := strings.LastIndex(rest, " b/"); index >= 0 {
		return rest[index+3:]
	}
	return rest
}

// handleChatGitDiffRequest answers jara/git/diff without holding up the
// client's other messages.
func handleChatGitDiffRequest(id json.RawMessage, cwd string, client *chatDaemonClient) {
	go func() {
		result, err := collectChatGitDiff(cwd)
		response := map[string]any{"id": id, "result": result}
		if err != nil {
			response = map[string]any{"id": id, "error": map[string]any{"code": -32000, "message": "git diff failed: " + strings.TrimSpace(chatGitErrorText(err))}}
		}
		if line, marshalErr := json.Marshal(response); marshalErr == nil {
			_ = client.write(line)
		}
	}()
}

func chatGitErrorText(err error) string {
	var exit *exec.ExitError
	if errors.As(err, &exit) && len(bytes.TrimSpace(exit.Stderr)) > 0 {
		return string(exit.Stderr)
	}
	return err.Error()
}
