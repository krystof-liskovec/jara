package agentrouter

import (
	"encoding/base64"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"krystof.liskovec/jara/internal/protocol"
	"krystof.liskovec/jara/internal/textutil"
)

// Custom slash commands are the prompt files Codex and Claude Code already
// use: ~/.codex/prompts/*.md, ~/.claude/commands/**/*.md, and the project's
// .claude/commands/**/*.md. Jara expands them itself, so each works with
// either provider. Project commands override personal ones of the same name.

const (
	customCommandLimit     = 200
	customCommandBodyLimit = 64 * 1024
)

func LoadCustomCommands(params json.RawMessage) []protocol.CustomCommand {
	var request struct {
		CWD string `json:"cwd"`
	}
	_ = json.Unmarshal(params, &request)
	home, _ := os.UserHomeDir()
	codexHome := textutil.FirstNonEmpty(os.Getenv("CODEX_HOME"), filepath.Join(home, ".codex"))
	claudeHome := textutil.FirstNonEmpty(os.Getenv("CLAUDE_CONFIG_DIR"), filepath.Join(home, ".claude"))
	byName := map[string]protocol.CustomCommand{}
	readCustomCommands(filepath.Join(codexHome, "prompts"), "codex", false, byName)
	readCustomCommands(filepath.Join(claudeHome, "commands"), "claude", true, byName)
	if request.CWD != "" {
		readCustomCommands(filepath.Join(request.CWD, ".claude", "commands"), "project", true, byName)
	}
	commands := make([]protocol.CustomCommand, 0, len(byName))
	for _, command := range byName {
		commands = append(commands, command)
	}
	sort.Slice(commands, func(i, j int) bool { return commands[i].Name < commands[j].Name })
	return commands
}

func readCustomCommands(root, source string, nested bool, into map[string]protocol.CustomCommand) {
	_ = filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil || len(into) >= customCommandLimit {
			return filepath.SkipDir
		}
		if entry.IsDir() {
			if path != root && !nested {
				return filepath.SkipDir
			}
			return nil
		}
		relative, relErr := filepath.Rel(root, path)
		name, isMarkdown := strings.CutSuffix(filepath.ToSlash(relative), ".md")
		if relErr != nil || !isMarkdown {
			return nil
		}
		name = strings.ReplaceAll(name, "/", ":")
		if !protocol.ValidCustomCommandName(name) {
			return nil
		}
		info, statErr := entry.Info()
		if statErr != nil || info.Size() > customCommandBodyLimit {
			return nil
		}
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			return nil
		}
		command := parseCustomCommand(string(data))
		command.Name, command.Source = name, source
		into[name] = command
		return nil
	})
}

// parseCustomCommand reads optional YAML-style front matter (description,
// argument-hint) and keeps the rest as the prompt.
func parseCustomCommand(text string) protocol.CustomCommand {
	text = strings.TrimPrefix(strings.ReplaceAll(text, "\r\n", "\n"), "\ufeff")
	var command protocol.CustomCommand
	if rest, ok := strings.CutPrefix(text, "---\n"); ok {
		if header, body, found := strings.Cut(rest, "\n---"); found {
			for _, line := range strings.Split(header, "\n") {
				key, value, _ := strings.Cut(line, ":")
				value = strings.Trim(strings.TrimSpace(value), `"'`)
				switch strings.TrimSpace(key) {
				case "description":
					command.Description = value
				case "argument-hint":
					command.ArgumentHint = value
				}
			}
			text = strings.TrimPrefix(body, "\n")
			if newline := strings.IndexByte(text, '\n'); newline >= 0 && strings.TrimSpace(text[:newline]) == "" {
				text = text[newline+1:]
			}
		}
	}
	command.Body = strings.TrimSpace(text)
	if command.Description == "" {
		first, _, _ := strings.Cut(command.Body, "\n")
		command.Description = textutil.FitText(strings.TrimLeft(first, "# "), 80)
	}
	return command
}

// turnImages converts image inputs (data URLs) into Claude content blocks.
func turnImages(params json.RawMessage) []map[string]any {
	var request struct {
		Input []struct {
			Type string `json:"type"`
			URL  string `json:"url"`
		} `json:"input"`
	}
	_ = json.Unmarshal(params, &request)
	var blocks []map[string]any
	for _, input := range request.Input {
		header, data, ok := strings.Cut(strings.TrimPrefix(input.URL, "data:"), ";base64,")
		if input.Type != "image" || !ok || !strings.HasPrefix(header, "image/") {
			continue
		}
		if _, err := base64.StdEncoding.DecodeString(data); err != nil {
			continue
		}
		blocks = append(blocks, map[string]any{"type": "image", "source": map[string]any{"type": "base64", "media_type": header, "data": data}})
	}
	return blocks
}

// turnAttachments returns the non-text inputs of a turn, such as images.
func turnAttachments(params json.RawMessage) []any {
	var request struct {
		Input []json.RawMessage `json:"input"`
	}
	_ = json.Unmarshal(params, &request)
	var attachments []any
	for _, input := range request.Input {
		var kind struct {
			Type string `json:"type"`
		}
		if json.Unmarshal(input, &kind) == nil && kind.Type != "text" {
			attachments = append(attachments, input)
		}
	}
	return attachments
}
