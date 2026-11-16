package jara

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"krystof.liskovec/jara/internal/protocol"
)

const defaultChatRuntime = "codex"

// chatRuntime describes process launch and configuration ownership for Jara's
// built-in conversation UI. It does not abstract the app-server protocol used
// by chatLoop; that protocol remains Codex-specific.
type chatRuntime struct {
	name                 string
	binary               string
	authOwner            string
	protocol             string
	configurationOptions []string
	serverArgs           func(chatRuntime, []string) ([]string, error)
}

var codexChatRuntime = chatRuntime{
	name:      "codex",
	binary:    "/usr/local/bin/codex",
	authOwner: "Codex CLI",
	protocol:  "Codex app-server",
	configurationOptions: []string{
		"model",
		"sandbox_mode",
		"approval_policy",
		"web_search",
		"configuration overrides",
		"feature toggles",
	},
	serverArgs: codexAppServerArgs,
}

func resolveChatRuntime(name string) (chatRuntime, error) {
	if name == "" {
		name = defaultChatRuntime
	}
	if name == codexChatRuntime.name {
		return codexChatRuntime, nil
	}
	return chatRuntime{}, fmt.Errorf("chat runtime provider %q is unsupported; available provider: %s", name, codexChatRuntime.name)
}

func (p chatRuntime) command(extra []string) (string, []string, error) {
	if p.binary == "" {
		return "", nil, fmt.Errorf("chat runtime provider %q has no executable", p.name)
	}
	args, err := p.serverArgs(p, extra)
	if err != nil {
		return "", nil, err
	}
	if p.binary == protocol.CodexBinary {
		return protocol.CodexExecutable(), args, nil
	}
	return p.binary, args, nil
}

func (p chatRuntime) supportsConfiguration(option string) bool {
	return slices.Contains(p.configurationOptions, option)
}

func codexAppServerArgs(provider chatRuntime, args []string) ([]string, error) {
	out := []string{"app-server", "--listen", "stdio://"}
	keys := map[string]string{
		"--model": "model", "-m": "model",
		"--sandbox": "sandbox_mode", "-s": "sandbox_mode",
		"--ask-for-approval": "approval_policy", "-a": "approval_policy",
	}
	for i := 0; i < len(args); i++ {
		flag, value, has := strings.Cut(args[i], "=")
		if !has && (flag == "--dangerously-bypass-approvals-and-sandbox" || flag == "--yolo") {
			if !provider.supportsConfiguration("sandbox_mode") || !provider.supportsConfiguration("approval_policy") {
				return nil, fmt.Errorf("chat runtime provider %q cannot configure full access", provider.name)
			}
			out = append(out, "-c", `sandbox_mode="danger-full-access"`, "-c", `approval_policy="never"`)
			continue
		}
		if flag == "--search" {
			if !provider.supportsConfiguration("web_search") {
				return nil, fmt.Errorf("chat runtime provider %q does not support web search", provider.name)
			}
			out = append(out, "-c", `web_search="live"`)
			continue
		}
		key, mapped := keys[flag]
		passthrough := flag == "-c" || flag == "--config" || flag == "--enable" || flag == "--disable"
		if !mapped && !passthrough {
			return nil, fmt.Errorf("Codex argument %q is unsupported by --interface jara; use --interface codex or a -c configuration override", flag)
		}
		if mapped && !provider.supportsConfiguration(key) {
			return nil, fmt.Errorf("chat runtime provider %q does not support %s", provider.name, key)
		}
		if (flag == "-c" || flag == "--config") && !provider.supportsConfiguration("configuration overrides") {
			return nil, fmt.Errorf("chat runtime provider %q does not support configuration overrides", provider.name)
		}
		if (flag == "--enable" || flag == "--disable") && !provider.supportsConfiguration("feature toggles") {
			return nil, fmt.Errorf("chat runtime provider %q does not support feature toggles", provider.name)
		}
		if !has {
			i++
			if i >= len(args) {
				return nil, fmt.Errorf("missing value for %s", flag)
			}
			value = args[i]
		}
		if mapped {
			encoded, _ := json.Marshal(value)
			out = append(out, "-c", key+"="+string(encoded))
		} else {
			out = append(out, flag, value)
		}
	}
	return out, nil
}
