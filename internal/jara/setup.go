package jara

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

func keygen(args []string) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	f := flag.NewFlagSet("keygen", flag.ContinueOnError)
	out := f.String("out", filepath.Join(home, ".ssh", "id_ed25519_jara"), "new private key path")
	if err = f.Parse(args); err != nil {
		return err
	}
	if f.NArg() != 0 {
		return fmt.Errorf("unexpected keygen argument")
	}
	if _, err = os.Lstat(*out); !os.IsNotExist(err) {
		return fmt.Errorf("key path already exists or cannot be checked; choose a new --out path")
	}
	if err = os.MkdirAll(filepath.Dir(*out), 0700); err != nil {
		return err
	}
	if err = interactive(exec.Command("ssh-keygen", "-t", "ed25519", "-f", *out, "-C", "jara")); err != nil {
		return err
	}
	b, err := os.ReadFile(*out + ".pub")
	if err != nil {
		return err
	}
	fmt.Println("Add this PUBLIC key to the host's deploy/authorized_keys, then register with --identity", *out)
	fmt.Print(string(b))
	return nil
}

func editorImport(c Config, args []string) error {
	if len(args) < 2 || args[0] != "import" || (args[1] != "vim" && args[1] != "nvim") {
		return fmt.Errorf("use jara editor import vim|nvim --source DIR [--remote NAME] [--force]")
	}
	editor := args[1]
	f := flag.NewFlagSet("editor import", flag.ContinueOnError)
	source := f.String("source", "", "configuration directory")
	name := f.String("remote", c.Default, "remote")
	force := f.Bool("force", false, "replace conflicting configuration files")
	if err := f.Parse(args[2:]); err != nil {
		return err
	}
	if *source == "" || f.NArg() != 0 {
		return fmt.Errorf("--source configuration directory is required")
	}
	root, err := filepath.Abs(*source)
	if err != nil {
		return err
	}
	info, err := os.Stat(root)
	if err != nil || !info.IsDir() {
		return fmt.Errorf("source must be a configuration directory")
	}
	r, ok := c.Remotes[*name]
	if !ok {
		return fmt.Errorf("unknown remote")
	}
	files, err := snapshot(root)
	if err != nil {
		return err
	}
	var dest Snapshot
	if err = rpc(r, Request{Op: "editor-snapshot", ID: editor}, nil, &dest); err != nil {
		return err
	}
	u, err := planUpdate(files, dest, Snapshot{}, Snapshot{}, *force)
	if err != nil {
		return err
	}
	payload, err := json.Marshal(u)
	if err != nil {
		return err
	}
	if err = rpc(r, Request{Op: "editor-apply", ID: editor}, bytes.NewReader(payload), nil); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "Imported %s configuration; plugins are not installed automatically. Open the editor to check plugin dependencies.\n", editor)
	return nil
}

func editorConfigPath(editor string) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	switch editor {
	case "nvim":
		return filepath.Join(home, ".config", "nvim"), nil
	case "vim":
		return filepath.Join(home, ".vim"), nil
	}
	return "", fmt.Errorf("unsupported remote editor")
}
