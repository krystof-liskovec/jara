package jara

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"
)

func remoteRPC(q Request) error {
	switch q.Op {
	case "codex-args-show":
		args, err := configuredCodexArgs(nil)
		if err != nil {
			return err
		}
		return json.NewEncoder(os.Stdout).Encode(args)
	case "codex-args-set":
		raw, err := io.ReadAll(io.LimitReader(os.Stdin, 65537))
		if err != nil {
			return err
		}
		args, err := parseCodexArgs(raw)
		if err != nil {
			return err
		}
		return saveCodexArgsOverride(args)
	case "codex-args-clear":
		return clearCodexArgsOverride()
	case "image-upload":
		return receiveClipboardImage(os.Stdin, os.Stdout)
	case "editor-snapshot", "editor-apply":
		gate, err := lockLifecycle()
		if err != nil {
			return err
		}
		defer gate()
		if err = admitWorkspace(); err != nil {
			return err
		}
		root, err := editorConfigPath(q.ID)
		if err != nil {
			return err
		}
		if _, err = exec.LookPath(q.ID); err != nil {
			return fmt.Errorf("%s is not installed; select it in the host environment and rebuild", q.ID)
		}
		if err = os.MkdirAll(root, 0700); err != nil {
			return err
		}
		if q.Op == "editor-snapshot" {
			files, err := snapshot(root)
			if err != nil {
				return err
			}
			return json.NewEncoder(os.Stdout).Encode(files)
		}
		var u Update
		if err = json.NewDecoder(io.LimitReader(os.Stdin, maxTransfer*2)).Decode(&u); err != nil {
			return err
		}
		return applyUpdate(root, u)
	case "doctor":
		d := Diagnosis{Protocol: Protocol, Version: Version, Root: dataRoot(), OK: true}
		for _, tool := range []string{"git", "tmux", "codex", "ssh", "mise"} {
			if _, err := exec.LookPath(tool); err != nil {
				d.OK = false
				d.Checks = append(d.Checks, "MISSING "+tool+": rebuild the environment with this tool")
			} else {
				d.Checks = append(d.Checks, "OK "+tool)
			}
		}
		if err := os.MkdirAll(dataRoot(), 0700); err != nil {
			d.OK = false
			d.Checks = append(d.Checks, "storage: "+err.Error())
		} else {
			f, e := os.CreateTemp(dataRoot(), ".doctor-")
			if e != nil {
				d.OK = false
				d.Checks = append(d.Checks, "storage not writable: "+e.Error())
			} else {
				f.Close()
				os.Remove(f.Name())
				d.Checks = append(d.Checks, "OK writable storage")
			}
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		auth := exec.CommandContext(ctx, "codex", "login", "status")
		if err := auth.Run(); err != nil {
			d.Checks = append(d.Checks, "SETUP Codex authentication: ssh to the environment and run codex login --device-auth")
		}
		probe, err := os.MkdirTemp(dataRoot(), ".sandbox-check-")
		if err != nil {
			return err
		}
		defer os.RemoveAll(probe)
		sandboxCtx, sandboxCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer sandboxCancel()
		sandbox := exec.CommandContext(sandboxCtx, "codex", "sandbox", "-c", "sandbox_mode=\"workspace-write\"", "--", "/bin/sh", "-c", "printf jara-sandbox-ok > check.txt")
		sandbox.Dir = probe
		if err = sandbox.Run(); err != nil {
			d.OK = false
			d.Checks = append(d.Checks, "SETUP Codex sandbox failed: check that seccomp permits nested sandboxing, AppArmor permits mount propagation, and the Docker host allows unprivileged user namespaces (see docs/verification.md)")
		} else {
			d.Checks = append(d.Checks, "OK real Codex sandbox execution")
		}
		if os.Getenv("JARA_DOCKER") == "1" {
			dockerCtx, dockerCancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer dockerCancel()
			info, e := exec.CommandContext(dockerCtx, "docker", "info", "--format", "{{json .SecurityOptions}}").Output()
			if e != nil || !strings.Contains(string(info), "name=rootless") {
				d.OK = false
				d.Checks = append(d.Checks, "SETUP rootless Docker unavailable: check the docker sidecar and shared socket")
			} else {
				d.Checks = append(d.Checks, "OK rootless Docker daemon")
			}
		}
		return json.NewEncoder(os.Stdout).Encode(d)
	case "info":
		w, err := workspaceInfo(q.ID)
		if err != nil {
			return err
		}
		return json.NewEncoder(os.Stdout).Encode(w)
	case "create":
		unlock, err := lockWorkspace(q.ID)
		if err != nil {
			return err
		}
		defer unlock()
		p, _ := workspacePath(q.ID)
		if _, err = os.Stat(p); !errors.Is(err, os.ErrNotExist) {
			if existing, e := workspaceInfo(q.ID); e == nil && !existing.Ready && existing.URL == q.URL {
				fmt.Fprintln(os.Stderr, "Retrying unfinished workspace initialization.")
				return nil
			}
			return fmt.Errorf("destination already exists; no files changed: %s", p)
		}
		if q.URL != "" {
			if err = validateOrigin(q.URL); err != nil {
				return err
			}
			cmd := exec.Command("git", "clone", "--no-checkout", "--", q.URL, p)
			cmd.Stdout = os.Stderr
			cmd.Stderr = os.Stderr
			cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
			if err = cmd.Run(); err != nil {
				return fmt.Errorf("clone failed; configure Git credentials on the remote and retry: %w", err)
			}
		} else if err = os.MkdirAll(p, 0700); err != nil {
			return err
		}
		return writeJSON(p+".json", Workspace{ID: q.ID, Name: q.Name, Path: p, URL: q.URL, Branch: q.Branch, Created: time.Now().UTC().Format(time.RFC3339)})
	case "checkout":
		unlock, err := lockWorkspace(q.ID)
		if err != nil {
			return err
		}
		defer unlock()
		w, err := workspaceInfo(q.ID)
		if err != nil {
			return err
		}
		temp, err := os.CreateTemp(dataRoot(), "bundle-*")
		if err != nil {
			return err
		}
		defer os.Remove(temp.Name())
		if _, err = io.Copy(temp, os.Stdin); err != nil {
			temp.Close()
			return err
		}
		if err = temp.Close(); err != nil {
			return err
		}
		if err = runGit(w.Path, "fetch", temp.Name(), "HEAD"); err != nil {
			return err
		}
		if q.Commit == "" || strings.HasPrefix(q.Commit, "-") {
			return fmt.Errorf("invalid commit")
		}
		if q.Branch == "" {
			err = runGit(w.Path, "checkout", "--detach", q.Commit)
		} else {
			if err = runGit(w.Path, "check-ref-format", "--branch", q.Branch); err != nil {
				return err
			}
			err = runGit(w.Path, "checkout", "-B", q.Branch, q.Commit)
		}
		if err != nil {
			return err
		}
		w.Ready = true
		return writeJSON(w.Path+".json", w)
	case "snapshot":
		w, err := workspaceInfo(q.ID)
		if err != nil {
			return err
		}
		files, err := snapshotWithIgnored(w.Path, q.IncludeIgnored)
		if err != nil {
			return err
		}
		return json.NewEncoder(os.Stdout).Encode(files)
	case "manifest":
		w, err := workspaceInfo(q.ID)
		if err != nil {
			return err
		}
		files, err := manifestWithIgnored(w.Path, q.IncludeIgnored)
		if err != nil {
			return err
		}
		return json.NewEncoder(os.Stdout).Encode(files)
	case "read-files":
		w, err := workspaceInfo(q.ID)
		if err != nil {
			return err
		}
		var names []string
		if err = json.NewDecoder(io.LimitReader(os.Stdin, maxTransfer)).Decode(&names); err != nil {
			return err
		}
		files, err := readSnapshotFiles(w.Path, names)
		if err != nil {
			return err
		}
		return json.NewEncoder(os.Stdout).Encode(files)
	case "exists":
		w, err := workspaceInfo(q.ID)
		if err != nil {
			return err
		}
		var paths []string
		if err = json.NewDecoder(io.LimitReader(os.Stdin, maxTransfer)).Decode(&paths); err != nil {
			return err
		}
		present, err := existingPaths(w.Path, paths)
		if err != nil {
			return err
		}
		return json.NewEncoder(os.Stdout).Encode(present)
	case "guard":
		if _, err := workspaceInfo(q.ID); err != nil {
			return err
		}
		ss, err := sessions()
		if err != nil {
			return err
		}
		if !q.Force {
			for _, s := range ss {
				if s.Workspace == q.ID {
					return fmt.Errorf("running agent in workspace; stop it or explicitly use --force")
				}
			}
		}
		return nil
	case "apply":
		unlock, err := lockWorkspace(q.ID)
		if err != nil {
			return err
		}
		defer unlock()
		w, err := workspaceInfo(q.ID)
		if err != nil {
			return err
		}
		ss, err := sessions()
		if err != nil {
			return err
		}
		if !q.Force {
			for _, s := range ss {
				if s.Workspace == q.ID {
					return fmt.Errorf("running agent in workspace; detach is insufficient, stop it or explicitly use --force")
				}
			}
		}
		var update Update
		if err = json.NewDecoder(io.LimitReader(os.Stdin, maxTransfer*2)).Decode(&update); err != nil {
			return err
		}
		if err = applyUpdate(w.Path, update); err != nil {
			return err
		}
		if !w.Ready {
			w.Ready = true
			return writeJSON(w.Path+".json", w)
		}
		return nil
	default:
		return fmt.Errorf("unknown RPC operation")
	}
}
