package agentrouter

import (
	"encoding/json"
	"os"
	"path/filepath"

	"gitea.liskovec.dev/krystof.liskovec/jara/internal/fsutil"
	"gitea.liskovec.dev/krystof.liskovec/jara/internal/protocol"
)

// modelPrefs are host-wide model choices that span providers. The default
// model also decides which agent a new conversation starts with; the
// fallback model takes over when the current provider hits its usage limit.
type modelPrefs struct {
	Default  string `json:"default,omitempty"`
	Fallback string `json:"fallback,omitempty"`
}

func modelPrefsPath() string { return filepath.Join(fsutil.DataRoot(), "models.json") }

func LoadModelPrefs() modelPrefs {
	var prefs modelPrefs
	data, err := os.ReadFile(modelPrefsPath())
	if err != nil || json.Unmarshal(data, &prefs) != nil {
		return modelPrefs{}
	}
	if !protocol.ValidModelID(prefs.Default) {
		prefs.Default = ""
	}
	if !protocol.ValidModelID(prefs.Fallback) {
		prefs.Fallback = ""
	}
	return prefs
}

func saveModelPrefs(prefs modelPrefs) error { return fsutil.WriteJSON(modelPrefsPath(), prefs) }
