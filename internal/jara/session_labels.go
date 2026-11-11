package jara

import (
	"encoding/base64"
	"strings"

	"gitea.liskovec.dev/krystof.liskovec/jara/internal/protocol"
	"gitea.liskovec.dev/krystof.liskovec/jara/internal/textutil"
)

func sessionRecency(session Session) int64 {
	if session.LastAttach != 0 {
		return session.LastAttach
	}
	return session.CreatedAt
}

func sessionMenuLabel(request Request) string {
	agent := protocol.AgentDisplayName(request.Runtime)
	prefix := ""
	if request.Runtime == protocol.AgentClaude {
		prefix = agent + " "
	}
	switch {
	case request.Handoff:
		return "Handoff to " + agent
	case request.Background:
		prompt := strings.ReplaceAll(textutil.SanitizeText(request.Prompt), "|", "/")
		prompt = strings.Join(strings.Fields(prompt), " ")
		if prompt == "" {
			return "Background " + agent + " run"
		}
		return prefix + "Run: " + textutil.FitText(prompt, 70)
	case request.ResumeLatest:
		return "Resume latest " + agent
	case request.Worktree:
		return prefix + "Worktree session"
	default:
		return strings.TrimSpace(prefix)
	}
}

func encodeSessionMenuLabel(label string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(label))
}

func decodeSessionMenuLabel(encoded string) string {
	decoded, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		return ""
	}
	return string(decoded)
}
