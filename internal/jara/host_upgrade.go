package jara

import (
	"bufio"
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path"
	"regexp"
	"strings"
	"time"
)

type deploymentUpgradeOptions struct {
	force            bool
	forceDowngrade   bool
	keepCodexVersion bool
	assumeYes        bool
}

func deploymentUpgradeCommand(c Config, args []string) error {
	name := ""
	var options deploymentUpgradeOptions
	for _, arg := range args {
		switch arg {
		case "--force":
			options.force = true
		case "--force-downgrade":
			options.forceDowngrade = true
		case "--keep-codex-version":
			options.keepCodexVersion = true
		case "-y":
			options.assumeYes = true
		default:
			if name != "" {
				return fmt.Errorf("use jara deployment upgrade [NAME] [-y] [--force] [--force-downgrade] [--keep-codex-version]")
			}
			name = arg
		}
	}
	if len(deploymentBundle()) == 0 {
		return fmt.Errorf("this Jara CLI does not include the source bundle; reinstall it with install.sh")
	}
	if name == "" {
		if _, ok := c.HostDeployments[c.Default]; ok {
			name = c.Default
		} else if len(c.HostDeployments) == 1 {
			for candidate := range c.HostDeployments {
				name = candidate
			}
		} else {
			name = c.Default
		}
	}
	if !nameRE.MatchString(name) {
		return fmt.Errorf("choose a guided deployment with `jara deployment upgrade NAME`")
	}
	deployment, ok := c.HostDeployments[name]
	if !ok {
		if _, connected := c.Remotes[name]; !connected {
			return fmt.Errorf("unknown remote %q; run jara setup to connect it", name)
		}
		return fmt.Errorf("%q is connected, but this computer has no saved Docker host connection for upgrades; run jara setup, enter remote name %q, and enable host management", name, name)
	}
	if !validGuidedDeploymentDirectory(deploymentName(name, deployment), deployment.Directory) {
		return fmt.Errorf("saved deployment directory is invalid; refusing to upgrade it")
	}
	if err := validateRemote(deployment.Admin); err != nil {
		return fmt.Errorf("invalid saved host connection: %w", err)
	}
	return upgradeGuidedHost(name, deployment, deploymentBundle(), Version, options, os.Stdout)
}

func upgradeGuidedHost(name string, saved HostDeployment, bundle []byte, desiredVersion string, options deploymentUpgradeOptions, log io.Writer) error {
	name = deploymentName(name, saved)
	if len(bundle) == 0 {
		return fmt.Errorf("deployment source bundle is empty")
	}
	if !nameRE.MatchString(name) || !validGuidedDeploymentDirectory(name, saved.Directory) {
		return fmt.Errorf("invalid guided deployment metadata")
	}
	project := "jara-" + strings.ToLower(name)
	runner := hostSSHRunner{remote: saved.Admin}
	homeData, err := runner.shell(20*time.Second, `printf '%s' "$HOME"`, nil, false)
	if err != nil {
		return fmt.Errorf("find Docker host home: %w", err)
	}
	home := strings.TrimSpace(string(homeData))
	if !path.IsAbs(home) || strings.ContainsAny(home, "\r\n\x00") {
		return fmt.Errorf("Docker host returned an invalid home directory")
	}
	root := path.Join(home, saved.Directory)
	lock := path.Join(home, ".jara-deploy-"+name+".upgrade.lock")
	if _, err = runner.shell(20*time.Second, "mkdir -m 700 -- "+quote(lock), nil, false); err != nil {
		return fmt.Errorf("another deployment upgrade may be running; if not, inspect and remove %s before retrying: %w", lock, err)
	}
	keepLock := false
	defer func() {
		if keepLock {
			fmt.Fprintln(log, "WARNING: deployment upgrade lock retained for recovery:", lock)
			return
		}
		if _, e := runner.shell(20*time.Second, "rm -f -- "+quote(path.Join(lock, "owner"))+" && rmdir -- "+quote(lock), nil, false); e != nil {
			fmt.Fprintln(log, "WARNING: could not release deployment upgrade lock:", e)
		}
	}()
	if _, err = runner.shell(10*time.Second, "printf '%s\\n' "+quote(desiredVersion)+" > "+quote(path.Join(lock, "owner")), nil, false); err != nil {
		return fmt.Errorf("record deployment upgrade lock owner: %w", err)
	}

	envPath := path.Join(root, "deploy/.env")
	manifestPath := path.Join(root, "deploy/.jara-native.json")
	keysPath := path.Join(root, "deploy/authorized_keys")
	envBefore, err := readHostFile(runner, envPath)
	if err != nil {
		return fmt.Errorf("read guided deployment settings: %w", err)
	}
	manifestBefore, err := readHostFile(runner, manifestPath)
	if err != nil {
		return fmt.Errorf("read guided deployment manifest: %w", err)
	}
	keysBefore, err := readHostFile(runner, keysPath)
	if err != nil {
		return fmt.Errorf("read authorized keys: %w", err)
	}
	var spec nativeDeployment
	if err = json.Unmarshal(manifestBefore, &spec); err != nil {
		return fmt.Errorf("decode guided deployment manifest: %w", err)
	}
	if err = spec.validate(project); err != nil {
		return err
	}
	settings, err := parseGeneratedEnvironment(envBefore)
	if err != nil {
		return err
	}
	if settings["JARA_DOCKER"] == "1" {
		return fmt.Errorf("client-side Jara upgrade does not support the optional Docker-in-Docker deployment")
	}
	codexVersion, err := codexPin(envBefore)
	if err != nil {
		return err
	}
	targetCodexVersion := codexVersion
	if !options.keepCodexVersion {
		targetCodexVersion, err = newestStableCodex(codexVersion, latestCodex)
		if err != nil {
			return err
		}
	}
	claudeAdded := addMissingClaudePin(settings, latestClaudeCode, log)
	runDocker := func(timeout time.Duration, args []string, stream bool) ([]byte, error) {
		return runner.run(timeout, "", args, nil, stream)
	}
	container, err := nativeContainer(runDocker, spec.Container)
	if err != nil {
		return err
	}
	apparmorProfile, err := nativeAppArmorOption(runDocker, container)
	if err != nil {
		return fmt.Errorf("inspect Docker security options: %w", err)
	}
	label, err := runDocker(20*time.Second, []string{"docker", "inspect", "--format", `{{index .Config.Labels "io.jara.project"}}`, container}, false)
	if err != nil || strings.TrimSpace(string(label)) != spec.Project {
		return fmt.Errorf("container %s is not the guided deployment recorded in its manifest", spec.Container)
	}
	gate := hostUpdater{Project: project, run: runDocker}
	installedCodex, err := gate.installed(container)
	if err != nil {
		return err
	}
	if installedCodex != codexVersion {
		return fmt.Errorf("running Codex is %s but deployment settings pin %s; reconcile before upgrading Jara", installedCodex, codexVersion)
	}
	oldJaraVersion, err := remoteJaraVersion(runDocker, container)
	if err != nil {
		return err
	}
	if versionNewer(oldJaraVersion, desiredVersion) {
		if !options.forceDowngrade {
			updated, updateErr := offerServerClientUpdate(runDocker, container, name, desiredVersion, options.assumeYes, log)
			if updateErr != nil {
				return updateErr
			}
			if updated {
				return nil
			}
			return fmt.Errorf("server runs newer Jara %s; refusing downgrade to %s; update this client or pass --force-downgrade", oldJaraVersion, desiredVersion)
		}
		fmt.Fprintf(log, "WARNING: forcing Jara downgrade from %s to %s.\n", oldJaraVersion, desiredVersion)
	}
	busy, err := gate.maintenance(container, "status", "")
	if err != nil {
		return err
	}
	if !admitUpdate(log, busy, options.force) {
		return nil
	}
	fmt.Fprintf(log, "Planned Jara upgrade on %s from %s to %s using this CLI's source bundle. ", saved.Admin.Host, oldJaraVersion, desiredVersion)
	if targetCodexVersion == codexVersion {
		fmt.Fprintf(log, "Codex remains pinned at %s.\n", codexVersion)
	} else {
		fmt.Fprintf(log, "Codex will update from %s to %s.\n", codexVersion, targetCodexVersion)
	}
	promptReader := bufio.NewReader(os.Stdin)
	confirm := func(prompt string, defaultYes bool) (bool, error) {
		if options.assumeYes {
			return true, nil
		}
		return askYes(promptReader, prompt, defaultYes)
	}
	confirmed, err := confirm("Continue?", true)
	if err != nil {
		return err
	}
	if !confirmed {
		return nil
	}
	var nonceBytes [8]byte
	if _, err = rand.Read(nonceBytes[:]); err != nil {
		return err
	}
	nonce := hex.EncodeToString(nonceBytes[:])
	stage := path.Join(home, ".jara-upgrade-"+strings.ToLower(name)+"-"+nonce)
	backup := path.Join(home, ".jara-deploy-"+name+".backup-"+nonce)
	rollbackName := spec.Container + "-rollback-" + nonce[:8]
	candidate := "jara-upgrade-" + project + ":server-" + nonce
	defer func() {
		if _, e := runner.shell(2*time.Minute, "rm -rf -- "+quote(stage), nil, false); e != nil {
			fmt.Fprintln(log, "WARNING: could not remove upgrade staging directory:", stage, e)
		}
	}()
	if _, err = runner.shell(2*time.Minute, "mkdir -m 700 -- "+quote(stage)+" && tar -xzf - -C "+quote(stage), bytes.NewReader(bundle), false); err != nil {
		return fmt.Errorf("transfer current Jara source bundle: %w", err)
	}
	copySettings := strings.Join([]string{
		shellArgs("cp", "-p", "--", envPath, path.Join(stage, "deploy/.env")),
		shellArgs("cp", "-p", "--", manifestPath, path.Join(stage, "deploy/.jara-native.json")),
		shellArgs("cp", "-p", "--", keysPath, path.Join(stage, "deploy/authorized_keys")),
	}, " && ")
	if _, err = runner.shell(30*time.Second, copySettings, nil, false); err != nil {
		return fmt.Errorf("preserve host settings in the candidate source tree: %w", err)
	}
	if targetCodexVersion != codexVersion || claudeAdded {
		updatedEnv := envBefore
		if targetCodexVersion != codexVersion {
			var pinErr error
			if updatedEnv, pinErr = replaceCodexPin(envBefore, targetCodexVersion); pinErr != nil {
				return pinErr
			}
		}
		if claudeAdded {
			updatedEnv = appendEnvSetting(updatedEnv, "CLAUDE_CODE_VERSION", settings["CLAUDE_CODE_VERSION"])
		}
		stageEnv := path.Join(stage, "deploy/.env")
		if _, err = runner.shell(30*time.Second, "cat > "+quote(stageEnv), bytes.NewReader(updatedEnv), false); err != nil {
			return fmt.Errorf("pin the candidate agent versions: %w", err)
		}
	}
	build := nativeBuildArgs(settings, candidate, targetCodexVersion)
	if _, err = runner.run(45*time.Minute, stage, build, nil, true); err != nil {
		return fmt.Errorf("candidate Jara build failed; live container and source unchanged: %w", err)
	}
	seccomp := path.Join(stage, "deploy/seccomp/codex.json")
	if _, err = runner.run(60*time.Second, stage, nativeSmokeArgsWithSeccomp(seccomp, candidate, targetCodexVersion, apparmorProfile), nil, true); err != nil {
		return fmt.Errorf("candidate smoke check failed; live container and source unchanged: %w", err)
	}
	builtVersion, err := runDocker(60*time.Second, []string{"docker", "run", "--rm", "--network=none", "--entrypoint", "/opt/jara/bin/jara", candidate, "version"}, false)
	if err != nil || strings.TrimSpace(string(builtVersion)) != desiredVersion {
		return fmt.Errorf("candidate Jara version does not match this CLI (%s)", desiredVersion)
	}
	envNow, err := readHostFile(runner, envPath)
	if err != nil {
		return err
	}
	manifestNow, err := readHostFile(runner, manifestPath)
	if err != nil {
		return err
	}
	keysNow, err := readHostFile(runner, keysPath)
	if err != nil {
		return err
	}
	if !bytes.Equal(envBefore, envNow) || !bytes.Equal(manifestBefore, manifestNow) || !bytes.Equal(keysBefore, keysNow) {
		return fmt.Errorf("deployment settings changed during build; live container unchanged")
	}
	active, err := nativeContainer(runDocker, spec.Container)
	if err != nil {
		return err
	}
	if active != container {
		return fmt.Errorf("deployment changed during build; refusing to replace it")
	}
	lease := "upgrade-" + nonce
	if admitted, gateErr := gate.beginUpdate(container, lease, options.force, log); gateErr != nil || !admitted {
		return gateErr
	}
	defer func() {
		if id, e := nativeContainer(runDocker, spec.Container); e == nil {
			if _, e = gate.maintenance(id, "end", lease); e != nil {
				fmt.Fprintln(log, "WARNING: could not clear upgrade gate; it expires after ten minutes:", e)
			}
		}
	}()
	envNow, err = readHostFile(runner, envPath)
	if err != nil {
		return err
	}
	manifestNow, err = readHostFile(runner, manifestPath)
	if err != nil {
		return err
	}
	keysNow, err = readHostFile(runner, keysPath)
	if err != nil {
		return err
	}
	if !bytes.Equal(envBefore, envNow) || !bytes.Equal(manifestBefore, manifestNow) || !bytes.Equal(keysBefore, keysNow) {
		return fmt.Errorf("deployment settings changed before replacement; live container unchanged")
	}
	active, err = nativeContainer(runDocker, spec.Container)
	if err != nil {
		return err
	}
	if active != container {
		return fmt.Errorf("deployment changed before replacement; refusing to replace it")
	}
	oldImageData, err := runDocker(20*time.Second, []string{"docker", "inspect", "--format", "{{.Image}}", container}, false)
	if err != nil {
		return err
	}
	oldImage := strings.TrimSpace(string(oldImageData))
	if !regexp.MustCompile(`^sha256:[a-f0-9]{64}$`).MatchString(oldImage) {
		return fmt.Errorf("cannot determine rollback image")
	}
	rollbackTag := "jara-upgrade-" + project + ":previous-" + nonce[:8]
	if _, err = runDocker(20*time.Second, []string{"docker", "tag", oldImage, rollbackTag}, false); err != nil {
		return err
	}
	if _, err = runDocker(2*time.Minute, []string{"docker", "stop", spec.Container}, true); err != nil {
		if recoveryErr := restoreOldContainer(runDocker, gate, spec, rollbackName, oldJaraVersion, codexVersion); recoveryErr != nil {
			keepLock = true
			return fmt.Errorf("could not stop previous container: %v; recovery failed: %w; recovery lock: %s", err, recoveryErr, lock)
		}
		return err
	}
	if _, err = runDocker(20*time.Second, []string{"docker", "rename", spec.Container, rollbackName}, false); err != nil {
		if recoveryErr := restoreOldContainer(runDocker, gate, spec, rollbackName, oldJaraVersion, codexVersion); recoveryErr != nil {
			keepLock = true
			return fmt.Errorf("could not rename previous container: %v; recovery failed: %w; recovery lock: %s", err, recoveryErr, lock)
		}
		return err
	}
	if _, err = runner.shell(30*time.Second, "mv -- "+quote(root)+" "+quote(backup)+" && mv -- "+quote(stage)+" "+quote(root), nil, false); err != nil {
		return rollbackGuidedUpgrade(runner, runDocker, gate, spec, root, backup, rollbackName, oldJaraVersion, codexVersion, err, &keepLock, lock)
	}
	newArgs := nativeRunArgs(spec, settings, candidate, root, apparmorProfile)
	if _, err = runDocker(2*time.Minute, newArgs, true); err == nil {
		err = waitNativeHealth(runDocker, spec.Container)
	}
	if err == nil {
		err = verifyNativeCandidate(runDocker, gate, spec.Container, targetCodexVersion)
	}
	if err == nil {
		var actual string
		actual, err = remoteJaraVersion(runDocker, spec.Container)
		if err == nil && actual != desiredVersion {
			err = fmt.Errorf("running Jara version is %s, expected %s", actual, desiredVersion)
		}
	}
	if err != nil {
		return rollbackGuidedUpgrade(runner, runDocker, gate, spec, root, backup, rollbackName, oldJaraVersion, codexVersion, err, &keepLock, lock)
	}
	previousRemoved := true
	if _, err = runDocker(20*time.Second, []string{"docker", "rm", rollbackName}, false); err != nil {
		previousRemoved = false
		fmt.Fprintln(log, "WARNING: previous stopped container remains as", rollbackName)
	}
	if previousRemoved {
		if _, err = runner.shell(2*time.Minute, "rm -rf -- "+quote(backup), nil, false); err != nil {
			fmt.Fprintln(log, "WARNING: previous source tree remains at", backup)
		}
	} else {
		fmt.Fprintln(log, "WARNING: previous source tree retained for recovery at", backup)
	}
	if targetCodexVersion == codexVersion {
		fmt.Fprintf(log, "Upgraded Jara %s → %s on %s. Codex remains %s.\n", oldJaraVersion, desiredVersion, saved.Admin.Host, codexVersion)
	} else {
		fmt.Fprintf(log, "Upgraded Jara %s → %s and Codex %s → %s on %s.\n", oldJaraVersion, desiredVersion, codexVersion, targetCodexVersion, saved.Admin.Host)
	}
	if previousRemoved {
		removeImage, promptErr := confirm("Remove previous image "+rollbackTag+"?", false)
		if promptErr != nil {
			fmt.Fprintf(log, "Could not read cleanup choice; previous image retained as %s: %v\n", rollbackTag, promptErr)
			return nil
		}
		if removeImage {
			if _, err = runDocker(20*time.Second, []string{"docker", "image", "rm", rollbackTag}, false); err != nil {
				fmt.Fprintf(log, "WARNING: could not remove previous image %s: %v\n", rollbackTag, err)
				return nil
			}
			fmt.Fprintf(log, "Removed previous image %s.\n", rollbackTag)
			return nil
		}
	}
	fmt.Fprintf(log, "Previous image retained as %s.\n", rollbackTag)
	return nil
}

func rollbackGuidedUpgrade(runner hostSSHRunner, run func(time.Duration, []string, bool) ([]byte, error), gate hostUpdater, spec nativeDeployment, root, backup, rollbackName, oldJaraVersion, codexVersion string, updateErr error, keepLock *bool, lock string) error {
	if _, inspectErr := run(20*time.Second, []string{"docker", "container", "inspect", spec.Container}, false); inspectErr == nil {
		if _, removeErr := run(20*time.Second, []string{"docker", "rm", "-f", spec.Container}, false); removeErr != nil {
			*keepLock = true
			return fmt.Errorf("upgrade failed: %v; could not remove candidate: %w; recovery lock: %s", updateErr, removeErr, lock)
		}
	}
	restoreSource := "if [ -d " + quote(backup) + " ]; then if [ -e " + quote(root) + " ]; then rm -rf -- " + quote(root) + "; fi; mv -- " + quote(backup) + " " + quote(root) + "; fi"
	if _, err := runner.shell(2*time.Minute, restoreSource, nil, false); err != nil {
		*keepLock = true
		return fmt.Errorf("upgrade failed: %v; previous source tree could not be restored: %w; recovery lock: %s", updateErr, err, lock)
	}
	if err := restoreOldContainer(run, gate, spec, rollbackName, oldJaraVersion, codexVersion); err != nil {
		*keepLock = true
		return fmt.Errorf("upgrade failed: %v; previous container could not be restored: %w; recovery lock: %s", updateErr, err, lock)
	}
	return fmt.Errorf("Jara upgrade failed; previous container and source restored: %w", updateErr)
}

func restoreOldContainer(run func(time.Duration, []string, bool) ([]byte, error), gate hostUpdater, spec nativeDeployment, rollbackName, oldJaraVersion, codexVersion string) error {
	if _, err := run(20*time.Second, []string{"docker", "container", "inspect", spec.Container}, false); err != nil {
		if _, rollbackErr := run(20*time.Second, []string{"docker", "container", "inspect", rollbackName}, false); rollbackErr != nil {
			return fmt.Errorf("previous container is missing under both expected names")
		}
		if _, err = run(20*time.Second, []string{"docker", "rename", rollbackName, spec.Container}, false); err != nil {
			return fmt.Errorf("rename previous container back: %w", err)
		}
	}
	running, err := run(20*time.Second, []string{"docker", "inspect", "--format", "{{.State.Running}}", spec.Container}, false)
	if err != nil {
		return fmt.Errorf("inspect previous container state: %w", err)
	}
	if strings.TrimSpace(string(running)) != "true" {
		if _, err = run(2*time.Minute, []string{"docker", "start", spec.Container}, true); err != nil {
			return fmt.Errorf("restart previous container: %w", err)
		}
	}
	id, err := nativeContainer(run, spec.Container)
	if err != nil {
		return fmt.Errorf("check restored container: %w", err)
	}
	version, versionErr := remoteJaraVersion(run, id)
	codex, codexErr := gate.installed(id)
	if versionErr != nil || version != oldJaraVersion || codexErr != nil || codex != codexVersion {
		return fmt.Errorf("restored container versions could not be verified")
	}
	return nil
}

func readHostFile(runner hostSSHRunner, file string) ([]byte, error) {
	return runner.shell(20*time.Second, shellArgs("cat", "--", file), nil, false)
}

func remoteJaraVersion(run func(time.Duration, []string, bool) ([]byte, error), container string) (string, error) {
	data, err := run(20*time.Second, []string{"docker", "exec", "--user", "1001", container, "jara", "version"}, false)
	if err != nil {
		return "", err
	}
	version := strings.TrimSpace(string(data))
	if version == "" || strings.ContainsAny(version, "\r\n") {
		return "", fmt.Errorf("unexpected Jara version output")
	}
	return version, nil
}
