package jara

import (
	"fmt"
	"strings"
	"time"
)

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
