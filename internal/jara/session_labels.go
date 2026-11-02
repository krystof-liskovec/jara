package jara

import (
	"encoding/base64"
	"strings"
)

func sessionRecency(session Session) int64 {
	if session.LastAttach != 0 {
		return session.LastAttach
	}
	return session.CreatedAt
}

func sessionMenuLabel(request Request) string {
	switch {
	case request.Background:
		prompt := strings.ReplaceAll(menuText(request.Prompt), "|", "/")
		prompt = strings.Join(strings.Fields(prompt), " ")
		if prompt == "" {
			return "Background Codex run"
		}
		return "Run: " + fitMenu(prompt, 70)
	case request.ResumeLatest:
		return "Resume latest Codex"
	case request.Worktree:
		return "Worktree session"
	default:
		return ""
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
