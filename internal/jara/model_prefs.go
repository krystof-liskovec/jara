package jara

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// modelPrefs are host-wide model choices that span providers. The default
// model also decides which agent a new conversation starts with; the
// fallback model takes over when the current provider hits its usage limit.
type modelPrefs struct {
	Default  string `json:"default,omitempty"`
	Fallback string `json:"fallback,omitempty"`
}

func modelPrefsPath() string { return filepath.Join(dataRoot(), "models.json") }

func loadModelPrefs() modelPrefs {
	var prefs modelPrefs
	data, err := os.ReadFile(modelPrefsPath())
	if err != nil || json.Unmarshal(data, &prefs) != nil {
		return modelPrefs{}
	}
	if !validChatModelID(prefs.Default) {
		prefs.Default = ""
	}
	if !validChatModelID(prefs.Fallback) {
		prefs.Fallback = ""
	}
	return prefs
}

func saveModelPrefs(prefs modelPrefs) error { return writeJSON(modelPrefsPath(), prefs) }
