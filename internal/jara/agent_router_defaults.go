package jara

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Default models live in each agent's own configuration, so Jara, Codex
// terminals, and Claude Code terminals on the host agree on them: Codex's
// config.toml `model` and Claude Code's settings.json `model`.

type defaultModels struct {
	Agent  string `json:"agent"`  // agent new conversations start with
	Codex  string `json:"codex"`  // Codex model ID, empty when unknown
	Claude string `json:"claude"` // Claude UI model ID, e.g. claude/opus
}

func (r *agentRouter) getDefaultModels(id json.RawMessage, params json.RawMessage) error {
	var request struct {
		CWD string `json:"cwd"`
	}
	_ = json.Unmarshal(params, &request)
	result := defaultModels{Agent: r.defaultAgent, Claude: claudeDefaultModel}
	if claudeAvailable() {
		if model := readClaudeDefaultModel(); model != "" {
			result.Claude = claudeModelPrefix + model
		}
	}
	readParams, _ := json.Marshal(map[string]any{"cwd": request.CWD})
	return r.callCodex(codexCall{method: "config/read", callback: func(config, _ json.RawMessage) {
		var read struct {
			Config struct {
				Model string `json:"model"`
			} `json:"config"`
		}
		if json.Unmarshal(config, &read) == nil && read.Config.Model != "" {
			result.Codex = read.Config.Model
			r.reply(id, result)
			return
		}
		// Without a configured model Codex uses its catalog default.
		listParams, _ := json.Marshal(map[string]any{"limit": chatModelPageSize, "includeHidden": false})
		_ = r.callCodex(codexCall{method: "model/list", callback: func(list, _ json.RawMessage) {
			var page struct {
				Data []codexModel `json:"data"`
			}
			_ = json.Unmarshal(list, &page)
			for _, model := range page.Data {
				if model.IsDefault {
					result.Codex = firstNonEmpty(model.Model, model.ID)
					break
				}
			}
			r.reply(id, result)
		}}, listParams)
	}}, readParams)
}

func (r *agentRouter) setDefaultModel(id json.RawMessage, params json.RawMessage) error {
	var request struct {
		Model string `json:"model"`
	}
	_ = json.Unmarshal(params, &request)
	if !validChatModelID(request.Model) {
		r.fail(id, "choose a listed model or a valid model ID")
		return nil
	}
	if agentForModel(request.Model) == agentClaude {
		if err := writeClaudeDefaultModel(claudeCLIModel(request.Model)); err != nil {
			r.fail(id, "could not save Claude Code's default model: "+err.Error())
			return nil
		}
		r.reply(id, map[string]any{"model": request.Model})
		return nil
	}
	writeParams, _ := json.Marshal(map[string]any{"keyPath": "model", "value": request.Model, "mergeStrategy": "replace"})
	return r.callCodex(codexCall{method: "config/value/write", callback: func(_, failure json.RawMessage) {
		if failure != nil {
			r.fail(id, "could not save Codex's default model: "+rpcErrorText(failure))
			return
		}
		r.reply(id, map[string]any{"model": request.Model})
	}}, writeParams)
}

func claudeSettingsPath() string {
	dir := os.Getenv("CLAUDE_CONFIG_DIR")
	if dir == "" {
		home, _ := os.UserHomeDir()
		dir = filepath.Join(home, ".claude")
	}
	return filepath.Join(dir, "settings.json")
}

func readClaudeDefaultModel() string {
	data, err := os.ReadFile(claudeSettingsPath())
	if err != nil {
		return ""
	}
	var settings struct {
		Model string `json:"model"`
	}
	if json.Unmarshal(data, &settings) != nil || !validChatModelID(claudeModelPrefix+settings.Model) {
		return ""
	}
	return settings.Model
}

// writeClaudeDefaultModel sets or, for an empty model, removes the `model`
// key while preserving every other Claude Code setting.
func writeClaudeDefaultModel(model string) error {
	path := claudeSettingsPath()
	settings := map[string]json.RawMessage{}
	data, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	if len(strings.TrimSpace(string(data))) > 0 {
		if err = json.Unmarshal(data, &settings); err != nil {
			return fmt.Errorf("%s is not valid JSON: %w", path, err)
		}
	}
	if model == "" {
		delete(settings, "model")
	} else {
		settings["model"], _ = json.Marshal(model)
	}
	encoded, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return err
	}
	if err = os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	temporary := path + ".jara-tmp"
	if err = os.WriteFile(temporary, append(encoded, '\n'), 0600); err != nil {
		return err
	}
	return os.Rename(temporary, path)
}
