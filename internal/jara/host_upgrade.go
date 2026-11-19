package jara

import (
	"bufio"
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path"
	"regexp"
	"strings"
	"time"
)

func upgradeGuidedHost(name string, saved HostDeployment, bundle []byte, desiredVersion string, options deploymentUpgradeOptions, log upgradeReporter) error {
	name = deploymentName(name, saved)
	if len(bundle) == 0 {
		return fmt.Errorf("deployment source bundle is empty")
	}
	if !nameRE.MatchString(name) || !validGuidedDeploymentDirectory(name, saved.Directory) {
		return fmt.Errorf("invalid guided deployment metadata")
	}
	project := "jara-" + strings.ToLower(name)
	log.stage("Checking deployment", 0.04, 15*time.Second)
	runner := hostSSHRunner{remote: saved.Admin, output: log.commandOutput()}
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
	stagePrefix := path.Join(home, ".jara-upgrade-"+strings.ToLower(name)+"-")
	deployLock, err := acquireRemoteDeployLock(runner, lock, stagePrefix, "Jara upgrade to "+desiredVersion, log)
	if err != nil {
		return err
	}
	keepLock := false
	defer func() {
		if keepLock {
			if e := deployLock.record(deployLockRecovery, ""); e != nil {
				fmt.Fprintln(log, "WARNING:", e)
			}
			fmt.Fprintln(log, "WARNING: deployment upgrade lock retained for recovery:", lock)
			return
		}
		if e := deployLock.release(); e != nil {
			fmt.Fprintln(log, "WARNING: could not release deployment upgrade lock:", e)
		}
	}()

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
	var claudeNotes bytes.Buffer
	claudeAdded := addMissingClaudePin(settings, latestClaudeCode, &claudeNotes)
	claudeTarget := ""
	if !claudeAdded && !options.keepClaudeVersion {
		claudeTarget = newerClaudePin(settings, latestClaudeCode, &claudeNotes)
	}
	var plan strings.Builder
	for _, line := range strings.SplitAfter(claudeNotes.String(), "\n") {
		if strings.HasPrefix(line, "WARNING") {
			fmt.Fprint(log, line)
		} else {
			plan.WriteString(line)
		}
	}
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
			var updated bool
			var updateErr error
			log.prompt(func() {
				updated, updateErr = offerServerClientUpdate(runDocker, container, name, desiredVersion, options.assumeYes, os.Stdout)
			})
			if updateErr != nil {
				return updateErr
			}
			if updated {
				log.skip("installed the server's newer client; run the upgrade again")
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
		log.skip("deferred while the deployment is in use")
		return nil
	}
	fmt.Fprintf(&plan, "Planned Jara upgrade on %s from %s to %s using this CLI's source bundle. ", saved.Admin.Host, oldJaraVersion, desiredVersion)
	if targetCodexVersion == codexVersion {
		fmt.Fprintf(&plan, "Codex remains pinned at %s.\n", codexVersion)
	} else {
		fmt.Fprintf(&plan, "Codex will update from %s to %s.\n", codexVersion, targetCodexVersion)
	}
	promptReader := bufio.NewReader(os.Stdin)
	confirm := func(prompt string, defaultYes bool) (yes bool, err error) {
		if options.assumeYes {
			return true, nil
		}
		log.prompt(func() { yes, err = askYes(promptReader, prompt, defaultYes) })
		return yes, err
	}
	if options.assumeYes {
		log.detail(plan.String())
	} else {
		log.prompt(func() { fmt.Print(plan.String()) })
	}
	confirmed, err := confirm("Continue?", true)
	if err != nil {
		return err
	}
	if !confirmed {
		log.skip("cancelled")
		return nil
	}
	log.stage("Uploading source", 0.08, 20*time.Second)
	var nonceBytes [8]byte
	if _, err = rand.Read(nonceBytes[:]); err != nil {
		return err
	}
	nonce := hex.EncodeToString(nonceBytes[:])
	stage := stagePrefix + nonce
	backup := path.Join(home, ".jara-deploy-"+name+".backup-"+nonce)
	rollbackName := spec.Container + "-rollback-" + nonce[:8]
	candidate := "jara-upgrade-" + project + ":server-" + nonce
	if err = deployLock.record(deployLockPrepare, stage); err != nil {
		return err
	}
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
	if targetCodexVersion != codexVersion || claudeAdded || claudeTarget != "" {
		updatedEnv := envBefore
		if targetCodexVersion != codexVersion {
			var pinErr error
			if updatedEnv, pinErr = replaceCodexPin(envBefore, targetCodexVersion); pinErr != nil {
				return pinErr
			}
		}
		if claudeAdded || claudeTarget != "" {
			updatedEnv = setEnvSetting(updatedEnv, "CLAUDE_CODE_VERSION", settings["CLAUDE_CODE_VERSION"])
		}
		stageEnv := path.Join(stage, "deploy/.env")
		if _, err = runner.shell(30*time.Second, "cat > "+quote(stageEnv), bytes.NewReader(updatedEnv), false); err != nil {
			return fmt.Errorf("pin the candidate agent versions: %w", err)
		}
	}
	log.stage("Building image", 0.80, 8*time.Minute)
	build := nativeBuildArgs(settings, candidate, targetCodexVersion)
	if _, err = runner.run(45*time.Minute, stage, build, nil, true); err != nil {
		return fmt.Errorf("candidate Jara build failed; live container and source unchanged: %w", err)
	}
	log.stage("Testing image", 0.86, 30*time.Second)
	seccomp := path.Join(stage, "deploy/seccomp/codex.json")
	if _, err = runner.run(60*time.Second, stage, nativeSmokeArgsWithSeccomp(seccomp, candidate, targetCodexVersion, apparmorProfile), nil, true); err != nil {
		return fmt.Errorf("candidate smoke check failed; live container and source unchanged: %w", err)
	}
	builtVersion, err := runDocker(60*time.Second, []string{"docker", "run", "--rm", "--network=none", "--entrypoint", "/opt/jara/bin/jara", candidate, "version"}, false)
	if err != nil || strings.TrimSpace(string(builtVersion)) != desiredVersion {
		return fmt.Errorf("candidate Jara version does not match this CLI (%s)", desiredVersion)
	}
	if err = checkCandidateBuild(runDocker, candidate); err != nil {
		return err
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
	log.stage("Replacing container", 0.92, 30*time.Second)
	hold := holdInterrupts(log, "Interrupt received; finishing or rolling back the container replacement before stopping.")
	defer hold.release()
	lease := "upgrade-" + nonce
	if admitted, gateErr := gate.beginUpdate(container, lease, options.force, log); gateErr != nil || !admitted {
		if gateErr == nil {
			log.skip("deferred while the deployment is in use")
		}
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
	if hold.interrupted() {
		return fmt.Errorf("upgrade interrupted; live container unchanged")
	}
	if err = deployLock.record(deployLockReplacing, ""); err != nil {
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
	rollback := func(cause error) error {
		log.stage("Rolling back", 0.99, time.Minute)
		return rollbackGuidedUpgrade(runner, runDocker, gate, spec, root, backup, rollbackName, oldJaraVersion, codexVersion, cause, &keepLock, lock)
	}
	if _, err = runner.shell(30*time.Second, "mv -- "+quote(root)+" "+quote(backup)+" && mv -- "+quote(stage)+" "+quote(root), nil, false); err != nil {
		return rollback(err)
	}
	log.stage("Starting new version", 0.98, time.Minute)
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
		return rollback(err)
	}
	log.stage("Cleaning up", 1, 10*time.Second)
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
	if err = deployLock.record(deployLockCleanup, ""); err != nil {
		fmt.Fprintln(log, "WARNING:", err)
	}
	hold.release()
	summary := "Jara " + oldJaraVersion + " → " + desiredVersion
	if targetCodexVersion != codexVersion {
		summary += ", Codex " + codexVersion + " → " + targetCodexVersion
	}
	if claudeAdded {
		summary += ", Claude Code " + settings["CLAUDE_CODE_VERSION"] + " added"
	} else if claudeTarget != "" {
		summary += ", Claude Code → " + claudeTarget
	}
	log.finish(summary)
	if previousRemoved && !hold.interrupted() {
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
			if !options.assumeYes {
				fmt.Fprintf(log, "Removed previous image %s.\n", rollbackTag)
			}
			return nil
		}
	}
	fmt.Fprintf(log, "Previous image retained as %s.\n", rollbackTag)
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
