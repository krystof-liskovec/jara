package jara

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
)

const Version = "0.1.0-dev"
const Protocol = 1

type Remote struct {
	Host     string `json:"host"`
	Port     int    `json:"port,omitempty"`
	User     string `json:"user,omitempty"`
	Identity string `json:"identity,omitempty"`
	Editor   string `json:"editor,omitempty"`
}
type Binding struct {
	Remote    string `json:"remote"`
	Workspace string `json:"workspace"`
}
type Config struct {
	Default        string              `json:"default,omitempty"`
	Editor         string              `json:"editor,omitempty"`
	Remotes        map[string]Remote   `json:"remotes"`
	Bindings       map[string]Binding  `json:"bindings"`
	Associations   map[string]Binding  `json:"associations"`
	EditorCommands map[string][]string `json:"editor_commands,omitempty"`
	original       [32]byte
}

var nameRE = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]{0,63}$`)

func configPath() (string, error) {
	if p := os.Getenv("JARA_CONFIG"); p != "" {
		return p, nil
	}
	d, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(d, "jara", "config.json"), nil
}
func readConfig() (Config, error) {
	c := Config{Remotes: map[string]Remote{}, Bindings: map[string]Binding{}, Associations: map[string]Binding{}}
	p, err := configPath()
	if err != nil {
		return c, err
	}
	b, err := os.ReadFile(p)
	if errors.Is(err, os.ErrNotExist) {
		return c, nil
	}
	if err != nil {
		return c, err
	}
	if err = json.Unmarshal(b, &c); err != nil {
		return c, fmt.Errorf("read %s: %w", p, err)
	}
	c.original = sha256.Sum256(b)
	if c.Remotes == nil {
		c.Remotes = map[string]Remote{}
	}
	if c.Bindings == nil {
		c.Bindings = map[string]Binding{}
	}
	if c.Associations == nil {
		c.Associations = map[string]Binding{}
	}
	return c, nil
}
func saveConfig(c Config) error {
	p, err := configPath()
	if err != nil {
		return err
	}
	if err = os.MkdirAll(filepath.Dir(p), 0700); err != nil {
		return err
	}
	lock := p + ".lock"
	if err = os.Mkdir(lock, 0700); err != nil {
		return fmt.Errorf("configuration is being updated (or stale lock %s); retry: %w", lock, err)
	}
	defer os.Remove(lock)
	current, err := os.ReadFile(p)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	var hash [32]byte
	if err == nil {
		hash = sha256.Sum256(current)
	}
	if hash != c.original {
		return fmt.Errorf("configuration changed concurrently; retry the command")
	}
	return writeJSON(p, c)
}
func writeJSON(p string, v any) error {
	if err := os.MkdirAll(filepath.Dir(p), 0700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(p), ".jara-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if err = f.Chmod(0600); err == nil {
		_, err = f.Write(append(b, '\n'))
	}
	if e := f.Close(); err == nil {
		err = e
	}
	if err != nil {
		return err
	}
	return os.Rename(f.Name(), p)
}
func validateRemote(r Remote) error {
	if r.Host == "" || r.Host[0] == '-' {
		return fmt.Errorf("host or SSH alias is required")
	}
	for _, s := range []string{r.Host, r.User} {
		for _, ch := range s {
			if ch <= 32 || ch == 127 {
				return fmt.Errorf("host and user must not contain whitespace or control characters")
			}
		}
	}
	if r.Port < 0 || r.Port > 65535 {
		return fmt.Errorf("port must be between 1 and 65535")
	}
	return nil
}
