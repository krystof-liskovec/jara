package jara

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

func (u hostUpdater) update(target string, check bool) error {
	// Separate projects/paths get independent locks; never launch two updates of one env file.
	lock := u.EnvFile + ".update.lock"
	f, err := os.OpenFile(lock, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	release, err := lockUpdateFile(f)
	if err != nil {
		f.Close()
		return err
	}
	defer func() { release(); f.Close() }()
	original, err := os.ReadFile(u.EnvFile)
	if err != nil {
		return err
	}
	composeOriginal, err := os.ReadFile(u.Compose)
	if err != nil {
		return err
	}
	var extraOriginal []byte
	if u.ExtraCompose != "" {
		extraOriginal, err = os.ReadFile(u.ExtraCompose)
		if err != nil {
			return err
		}
	}
	container, err := u.container()
	if err != nil {
		return err
	}
	if u.ExtraCompose == "" {
		environment, e := u.run(20*time.Second, []string{"docker", "inspect", "--format", "{{json .Config.Env}}", container}, false)
		if e != nil {
			return e
		}
		var values []string
		if e = json.Unmarshal(environment, &values); e != nil {
			return fmt.Errorf("inspect deployment environment: %w", e)
		}
		for _, value := range values {
			if value == "JARA_DOCKER=1" {
				return fmt.Errorf("Docker-enabled deployment requires --compose-extra deploy/compose.docker.yaml so updates retain its daemon configuration")
			}
		}
	}
	current, err := u.installed(container)
	if err != nil {
		return err
	}
	configured, err := codexPin(original)
	if err != nil {
		return err
	}
	if configured != current {
		return fmt.Errorf("running Codex is %s but env file pins %s; reconcile this deployment before automatic updates", current, configured)
	}
	if target == "" {
		target, err = u.latest()
		if err != nil {
			return fmt.Errorf("check latest Codex: %w", err)
		}
	}
	newer, err := newerRelease(target, current)
	if err != nil {
		return err
	}
	if !newer {
		fmt.Fprintf(u.log, "Codex %s is current (requested %s); no changes.\n", current, target)
		return nil
	}
	fmt.Fprintf(u.log, "Codex update available: %s → %s.\n", current, target)
	if check {
		return nil
	}
	pin, err := replaceCodexPin(original, target)
	if err != nil {
		return err
	}
	busy, err := u.maintenance(container, "status", "")
	if err != nil {
		return err
	}
	if busy.Busy {
		fmt.Fprintln(u.log, "Deferred:", busy.Reason)
		return nil
	}
	var config struct {
		Services map[string]struct {
			SecurityOpt []string `json:"security_opt"`
		}
	}
	b, err := u.run(20*time.Second, u.compose("", "config", "--format", "json"), false)
	if err != nil {
		return err
	}
	if err = json.Unmarshal(b, &config); err != nil {
		return err
	}
	raw := make([]byte, 12)
	if _, err = rand.Read(raw); err != nil {
		return err
	}
	lease := "update-" + hex.EncodeToString(raw)
	tag := "jara-update-" + u.Project + ":" + target + "-" + hex.EncodeToString(raw[:4])
	override, err := os.CreateTemp(filepath.Dir(u.Compose), ".jara-update-*.json")
	if err != nil {
		return err
	}
	overridePath := override.Name()
	override.Close()
	defer os.Remove(overridePath)
	writeOverride := func(image string) error {
		return writeJSON(overridePath, map[string]any{"services": map[string]any{"agent": map[string]any{"image": image, "build": map[string]any{"args": map[string]string{"CODEX_VERSION": target}}}}})
	}
	if err = writeOverride(tag); err != nil {
		return err
	}
	if _, err = u.run(45*time.Minute, u.compose(overridePath, "build", "agent"), true); err != nil {
		return fmt.Errorf("candidate build failed; live environment unchanged: %w", err)
	}
	smoke := []string{"docker", "run", "--rm", "--network=none", "--user", "agent"}
	for _, opt := range config.Services["agent"].SecurityOpt {
		if strings.HasPrefix(opt, "seccomp=") {
			p := strings.TrimPrefix(opt, "seccomp=")
			if p != "unconfined" && !filepath.IsAbs(p) {
				p = filepath.Join(filepath.Dir(u.Compose), p)
			}
			opt = "seccomp=" + p
		}
		smoke = append(smoke, "--security-opt", opt)
	}
	// No credentials, live mounts, provider tokens, or inference in this container.
	smoke = append(smoke, "--entrypoint", "/bin/sh", tag, "-ec", `test "$(codex --version)" = "codex-cli `+target+`"; jara version; mkdir -p /home/agent/update-check; cd /home/agent/update-check; codex sandbox -c 'sandbox_mode="workspace-write"' -- /bin/sh -c 'printf update-ok > inside; if (printf forbidden > /home/agent/outside) 2>/dev/null; then exit 73; fi'; test "$(cat inside)" = update-ok; test ! -e /home/agent/outside`)
	if _, err = u.run(60*time.Second, smoke, true); err != nil {
		return fmt.Errorf("candidate smoke test failed; live environment unchanged: %w", err)
	}
	// Detect config changes and a newly busy environment after the potentially long build.
	data, err := os.ReadFile(u.EnvFile)
	if err != nil {
		return err
	}
	if !bytes.Equal(data, original) {
		return fmt.Errorf("env file changed during build; live environment unchanged")
	}
	composeNow, err := os.ReadFile(u.Compose)
	if err != nil {
		return err
	}
	if !bytes.Equal(composeNow, composeOriginal) {
		return fmt.Errorf("Compose file changed during build; live environment unchanged")
	}
	if u.ExtraCompose != "" {
		extraNow, e := os.ReadFile(u.ExtraCompose)
		if e != nil {
			return e
		}
		if !bytes.Equal(extraNow, extraOriginal) {
			return fmt.Errorf("Compose overlay changed during build; live environment unchanged")
		}
	}
	active, err := u.container()
	if err != nil {
		return err
	}
	if active != container {
		return fmt.Errorf("deployment changed during build; refusing to replace it")
	}
	busy, err = u.maintenance(container, "begin", lease)
	if err != nil {
		return err
	}
	if busy.Busy {
		fmt.Fprintln(u.log, "Deferred after build:", busy.Reason)
		return nil
	}
	defer func() {
		if id, e := u.container(); e == nil {
			if _, e = u.maintenance(id, "end", lease); e != nil {
				fmt.Fprintln(u.log, "WARNING: could not clear update gate; it expires after ten minutes:", e)
			}
		}
	}()
	old, err := u.run(20*time.Second, []string{"docker", "inspect", "--format", "{{.Image}}", container}, false)
	if err != nil {
		return err
	}
	oldImage := strings.TrimSpace(string(old))
	if !regexp.MustCompile(`^sha256:[a-f0-9]{64}$`).MatchString(oldImage) {
		return fmt.Errorf("cannot determine rollback image")
	}
	rollbackTag := "jara-update-" + u.Project + ":previous-" + hex.EncodeToString(raw[:4])
	if _, err = u.run(20*time.Second, []string{"docker", "tag", oldImage, rollbackTag}, false); err != nil {
		return err
	}
	apply := func() error {
		if _, e := u.run(2*time.Minute, u.compose(overridePath, "up", "-d", "--no-build", "--no-deps", "--pull", "never", "--force-recreate", "--wait", "--wait-timeout", "60", "agent"), true); e != nil {
			return e
		}
		id, e := u.container()
		if e != nil {
			return e
		}
		version, e := u.installed(id)
		if e != nil {
			return e
		}
		if version != target {
			return fmt.Errorf("deployed version does not match candidate")
		}
		var d Diagnosis
		out, e := u.run(30*time.Second, []string{"docker", "exec", "--user", "agent", id, "jara", "_rpc", token(Request{Op: "doctor"})}, false)
		if e != nil {
			return e
		}
		if e = json.Unmarshal(out, &d); e != nil {
			return e
		}
		if !d.OK || d.Protocol != Protocol {
			return fmt.Errorf("deployed candidate failed Jara diagnostics")
		}
		return nil
	}
	deployErr := apply()
	if deployErr == nil {
		deployErr = writeEnvPin(u.EnvFile, original, pin)
	}
	if deployErr != nil {
		if err = writeOverride(rollbackTag); err != nil {
			return fmt.Errorf("update failed: %v; could not prepare rollback: %w", deployErr, err)
		}
		if _, err = u.run(2*time.Minute, u.compose(overridePath, "up", "-d", "--no-build", "--no-deps", "--pull", "never", "--force-recreate", "--wait", "--wait-timeout", "60", "agent"), true); err != nil {
			return fmt.Errorf("update failed: %v; rollback also failed: %w; inspect project %s", deployErr, err, u.Project)
		}
		id, e := u.container()
		if e != nil {
			return fmt.Errorf("update failed: %v; rollback verification failed: %w", deployErr, e)
		}
		v, e := u.installed(id)
		if e != nil || v != current {
			return fmt.Errorf("update failed: %v; rollback version could not be verified", deployErr)
		}
		return fmt.Errorf("update failed; previous image restored, env pin unchanged (data migrations are not rolled back): %w", deployErr)
	}
	fmt.Fprintf(u.log, "Updated Codex %s → %s; saved exact version in %s. Previous image retained as %s.\n", current, target, u.EnvFile, rollbackTag)
	return nil
}
