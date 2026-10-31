package jara

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

func updateNativeDeployment(project, manifestPath, envPath, target string, check bool,
	run func(time.Duration, []string, bool) ([]byte, error), latest func() (string, error), log io.Writer) error {
	manifestData, err := os.ReadFile(manifestPath)
	if err != nil {
		return err
	}
	var deployment nativeDeployment
	if err = json.Unmarshal(manifestData, &deployment); err != nil {
		return fmt.Errorf("read native deployment manifest: %w", err)
	}
	if err = deployment.validate(project); err != nil {
		return err
	}
	root := filepath.Dir(filepath.Dir(manifestPath))
	if err = checkHostUpgradeLock(root); err != nil {
		return err
	}
	if target != "" {
		if _, err = parseRelease(target); err != nil {
			return err
		}
	}
	lock := envPath + ".update.lock"
	lockFile, err := os.OpenFile(lock, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	release, err := lockUpdateFile(lockFile)
	if err != nil {
		lockFile.Close()
		return err
	}
	defer func() { release(); lockFile.Close() }()
	originalEnv, err := os.ReadFile(envPath)
	if err != nil {
		return err
	}
	settings, err := parseGeneratedEnvironment(originalEnv)
	if err != nil {
		return err
	}
	if settings["JARA_DOCKER"] == "1" {
		return fmt.Errorf("native host updater does not support the optional Docker-in-Docker deployment")
	}
	current, err := codexPin(originalEnv)
	if err != nil {
		return err
	}
	container, err := nativeContainer(run, deployment.Container)
	if err != nil {
		return err
	}
	apparmorProfile, err := nativeAppArmorOption(run, container)
	if err != nil {
		return fmt.Errorf("inspect Docker security options: %w", err)
	}
	labelFormat := "{{index .Config.Labels \"io.jara.project\"}}"
	label, err := run(20*time.Second, []string{"docker", "inspect", "--format", labelFormat, container}, false)
	if err != nil || strings.TrimSpace(string(label)) != deployment.Project {
		return fmt.Errorf("container %s is not the guided deployment recorded in the manifest", deployment.Container)
	}
	gate := hostUpdater{Project: project, run: run}
	installed, err := gate.installed(container)
	if err != nil {
		return err
	}
	if installed != current {
		return fmt.Errorf("running Codex is %s but env file pins %s; reconcile this deployment before updating", installed, current)
	}
	if target == "" {
		target, err = latest()
		if err != nil {
			return fmt.Errorf("check latest Codex: %w", err)
		}
	}
	newer, err := newerRelease(target, current)
	if err != nil {
		return err
	}
	if !newer {
		fmt.Fprintf(log, "Codex %s is current (requested %s); no changes.\n", current, target)
		return nil
	}
	fmt.Fprintf(log, "Codex update available: %s → %s.\n", current, target)
	if check {
		return nil
	}
	pin, err := replaceCodexPin(originalEnv, target)
	if err != nil {
		return err
	}
	busy, err := gate.maintenance(container, "status", "")
	if err != nil {
		return err
	}
	if busy.Busy {
		fmt.Fprintln(log, "Deferred:", busy.Reason)
		return nil
	}
	raw := make([]byte, 12)
	if _, err = rand.Read(raw); err != nil {
		return err
	}
	candidate := "jara-update-" + project + ":" + target + "-" + hex.EncodeToString(raw[:4])
	buildArgs := nativeBuildArgs(settings, candidate, target)
	if _, err = run(45*time.Minute, buildArgs, true); err != nil {
		return fmt.Errorf("candidate build failed; live container unchanged: %w", err)
	}
	smoke := nativeSmokeArgs(root, candidate, target, apparmorProfile)
	if _, err = run(60*time.Second, smoke, true); err != nil {
		return fmt.Errorf("candidate smoke test failed; live container unchanged: %w", err)
	}
	envNow, err := os.ReadFile(envPath)
	if err != nil {
		return err
	}
	manifestNow, err := os.ReadFile(manifestPath)
	if err != nil {
		return err
	}
	if !bytes.Equal(envNow, originalEnv) || !bytes.Equal(manifestNow, manifestData) {
		return fmt.Errorf("deployment settings changed during build; live container unchanged")
	}
	if err = checkHostUpgradeLock(root); err != nil {
		return err
	}
	active, err := nativeContainer(run, deployment.Container)
	if err != nil {
		return err
	}
	if active != container {
		return fmt.Errorf("deployment changed during build; refusing to replace it")
	}
	lease := "update-" + hex.EncodeToString(raw)
	busy, err = gate.maintenance(container, "begin", lease)
	if err != nil {
		return err
	}
	if busy.Busy {
		fmt.Fprintln(log, "Deferred after build:", busy.Reason)
		return nil
	}
	defer func() {
		if id, e := nativeContainer(run, deployment.Container); e == nil {
			if _, e = gate.maintenance(id, "end", lease); e != nil {
				fmt.Fprintln(log, "WARNING: could not clear update gate; it expires after ten minutes:", e)
			}
		}
	}()
	old, err := run(20*time.Second, []string{"docker", "inspect", "--format", "{{.Image}}", container}, false)
	if err != nil {
		return err
	}
	oldImage := strings.TrimSpace(string(old))
	if !regexp.MustCompile(`^sha256:[a-f0-9]{64}$`).MatchString(oldImage) {
		return fmt.Errorf("cannot determine rollback image")
	}
	rollbackTag := "jara-update-" + project + ":previous-" + hex.EncodeToString(raw[:4])
	if _, err = run(20*time.Second, []string{"docker", "tag", oldImage, rollbackTag}, false); err != nil {
		return err
	}
	rollbackName := deployment.Container + "-rollback-" + hex.EncodeToString(raw[:3])
	if _, err = run(2*time.Minute, []string{"docker", "stop", deployment.Container}, true); err != nil {
		_, _ = run(2*time.Minute, []string{"docker", "start", deployment.Container}, true)
		return err
	}
	if _, err = run(20*time.Second, []string{"docker", "rename", deployment.Container, rollbackName}, false); err != nil {
		_, _ = run(2*time.Minute, []string{"docker", "start", deployment.Container}, true)
		return err
	}
	newArgs := nativeRunArgs(deployment, settings, candidate, root, apparmorProfile)
	if _, err = run(2*time.Minute, newArgs, true); err == nil {
		err = waitNativeHealth(run, deployment.Container)
	}
	if err == nil {
		err = verifyNativeCandidate(run, gate, deployment.Container, target)
	}
	if err == nil {
		err = writeEnvPin(envPath, originalEnv, pin)
	}
	if err != nil {
		if _, inspectErr := run(20*time.Second, []string{"docker", "container", "inspect", deployment.Container}, false); inspectErr == nil {
			if _, removeErr := run(20*time.Second, []string{"docker", "rm", "-f", deployment.Container}, false); removeErr != nil {
				return fmt.Errorf("update failed: %v; could not remove candidate: %w", err, removeErr)
			}
		}
		if _, e := run(20*time.Second, []string{"docker", "rename", rollbackName, deployment.Container}, false); e != nil {
			return fmt.Errorf("update failed: %v; could not restore previous container: %w", err, e)
		}
		if _, e := run(2*time.Minute, []string{"docker", "start", deployment.Container}, true); e != nil {
			return fmt.Errorf("update failed: %v; previous container restart failed: %w", err, e)
		}
		id, e := nativeContainer(run, deployment.Container)
		if e != nil {
			return fmt.Errorf("update failed: %v; rollback verification failed: %w", err, e)
		}
		version, e := gate.installed(id)
		if e != nil || version != current {
			return fmt.Errorf("update failed: %v; rollback version could not be verified", err)
		}
		return fmt.Errorf("update failed; previous container restored and env pin unchanged: %w", err)
	}
	if _, err = run(20*time.Second, []string{"docker", "rm", rollbackName}, false); err != nil {
		fmt.Fprintln(log, "WARNING: previous stopped container remains as", rollbackName)
	}
	fmt.Fprintf(log, "Updated Codex %s → %s; saved exact version in %s. Previous image retained as %s.\n", current, target, envPath, rollbackTag)
	return nil
}

func nativeContainer(run func(time.Duration, []string, bool) ([]byte, error), name string) (string, error) {
	args := []string{"docker", "inspect", "--format", "{{.Id}}", name}
	b, err := run(20*time.Second, args, false)
	if err != nil {
		return "", fmt.Errorf("inspect native Jara container %s: %w", name, err)
	}
	id := strings.TrimSpace(string(b))
	if !regexp.MustCompile(`^[a-f0-9]{64}$`).MatchString(id) {
		return "", fmt.Errorf("expected a running native Jara container named %s", name)
	}
	state, err := run(20*time.Second, []string{"docker", "inspect", "--format", "{{.State.Running}}", name}, false)
	if err != nil || strings.TrimSpace(string(state)) != "true" {
		return "", fmt.Errorf("native Jara container %s is not running", name)
	}
	return id, nil
}

func nativeSmokeArgs(root, image, target, apparmorProfile string) []string {
	seccomp := filepath.Join(root, "deploy", "seccomp", "codex.json")
	return nativeSmokeArgsWithSeccomp(seccomp, image, target, apparmorProfile)
}

func nativeSmokeArgsWithSeccomp(seccomp, image, target, apparmorProfile string) []string {
	args := []string{"docker", "run", "--rm", "--network=none", "--user", "agent", "--security-opt", "seccomp=" + seccomp}
	if apparmorProfile != "" {
		args = append(args, "--security-opt", "apparmor="+apparmorProfile)
	}
	return append(args,
		"--entrypoint", "/bin/sh", image, "-ec", `test "$(codex --version)" = "codex-cli `+target+`"; jara version; mkdir -p /home/agent/update-check; cd /home/agent/update-check; codex sandbox -c 'sandbox_mode="workspace-write"' -- /bin/sh -c 'printf update-ok > inside; if (printf forbidden > /home/agent/outside) 2>/dev/null; then exit 73; fi'; test "$(cat inside)" = update-ok; test ! -e /home/agent/outside`)
}

func waitNativeHealth(run func(time.Duration, []string, bool) ([]byte, error), container string) error {
	for attempt := 0; attempt < 60; attempt++ {
		b, err := run(10*time.Second, []string{"docker", "inspect", "--format", "{{if .State.Health}}{{.State.Health.Status}}{{else}}none{{end}}", container}, false)
		if err != nil {
			return err
		}
		switch strings.TrimSpace(string(b)) {
		case "healthy":
			return nil
		case "unhealthy", "none":
			return fmt.Errorf("candidate container health is %s", strings.TrimSpace(string(b)))
		}
		time.Sleep(time.Second)
	}
	return fmt.Errorf("candidate container did not become healthy within one minute")
}

func verifyNativeCandidate(run func(time.Duration, []string, bool) ([]byte, error), gate hostUpdater, container, target string) error {
	id, err := nativeContainer(run, container)
	if err != nil {
		return err
	}
	version, err := gate.installed(id)
	if err != nil {
		return err
	}
	if version != target {
		return fmt.Errorf("deployed Codex version %s does not match candidate %s", version, target)
	}
	data, err := run(30*time.Second, []string{"docker", "exec", "--user", "agent", id, "jara", "_rpc", token(Request{Op: "doctor"})}, false)
	if err != nil {
		return err
	}
	var diagnosis Diagnosis
	if err = json.Unmarshal(data, &diagnosis); err != nil {
		return err
	}
	if !diagnosis.OK || diagnosis.Protocol != Protocol {
		return fmt.Errorf("deployed candidate failed Jara diagnostics")
	}
	return nil
}
