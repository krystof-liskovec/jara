// Package fsutil holds file helpers shared by Jara's packages.
package fsutil

import (
	"encoding/json"
	"os"
	"path/filepath"
)

func WriteJSON(p string, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	if err = os.MkdirAll(filepath.Dir(p), 0700); err != nil {
		return err
	}
	return WritePrivateFile(p, append(b, '\n'))
}

// WritePrivateFile replaces p atomically with data readable only by its owner.
func WritePrivateFile(p string, data []byte) error {
	f, err := os.CreateTemp(filepath.Dir(p), ".jara-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if err = f.Chmod(0600); err == nil {
		_, err = f.Write(data)
	}
	if e := f.Close(); err == nil {
		err = e
	}
	if err != nil {
		return err
	}
	return os.Rename(f.Name(), p)
}

func DataRoot() string {
	if p := os.Getenv("JARA_DATA"); p != "" {
		return p
	}
	h, _ := os.UserHomeDir()
	return filepath.Join(h, ".local", "share", "jara")
}
