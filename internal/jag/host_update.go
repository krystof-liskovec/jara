package jag

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"
)

type hostUpdater struct {
	Project, Compose, EnvFile, ExtraCompose string
	run                                     func(time.Duration, []string, bool) ([]byte, error)
	latest                                  func() (string, error)
	log                                     io.Writer
}

func hostCommand(args []string) error {
	if len(args) == 0 || args[0] != "update" {
		return fmt.Errorf("use jag host update --project NAME [--compose FILE] [--env-file FILE] [--check]")
	}
	if runtime.GOOS != "linux" {
		return fmt.Errorf("run host update on the Linux Docker host")
	}
	f := flag.NewFlagSet("host update", flag.ContinueOnError)
	project := f.String("project", "", "existing Compose project name (required)")
	compose := f.String("compose", "deploy/compose.yaml", "Compose file")
	extra := f.String("compose-extra", "", "optional Compose overlay (e.g. deploy/compose.docker.yaml)")
	env := f.String("env-file", "deploy/.env", "deployment env file")
	check := f.Bool("check", false, "report available stable update without building or deploying")
	version := f.String("version", "", "explicit stable version; default: npm latest")
	if err := f.Parse(args[1:]); err != nil {
		return err
	}
	if f.NArg() != 0 {
		return fmt.Errorf("unexpected update argument")
	}
	if !regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,62}$`).MatchString(*project) {
		return fmt.Errorf("supply the existing Compose --project name using lowercase letters, digits, underscores or hyphens")
	}
	cp, err := filepath.Abs(*compose)
	if err != nil {
		return err
	}
	ep, err := filepath.Abs(*env)
	if err != nil {
		return err
	}
	if *version != "" {
		if _, err = parseRelease(*version); err != nil {
			return err
		}
	}
	extraPath := ""
	if *extra != "" {
		extraPath, err = filepath.Abs(*extra)
		if err != nil {
			return err
		}
	}
	u := hostUpdater{ExtraCompose: extraPath, Project: *project, Compose: cp, EnvFile: ep, run: runHostCommand, latest: latestCodex, log: os.Stdout}
	return u.update(*version, *check)
}

func runHostCommand(timeout time.Duration, args []string, stream bool) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, args[0], args[1:]...)
	var stdout, stderr bytes.Buffer
	if stream {
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
	} else {
		cmd.Stdout = &stdout
		cmd.Stderr = &stderr
	}
	err := cmd.Run()
	if err != nil {
		if ctx.Err() != nil {
			return nil, fmt.Errorf("command timed out: %w", ctx.Err())
		}
		return nil, fmt.Errorf("%s failed: %w: %s", args[0], err, strings.TrimSpace(stderr.String()))
	}
	return stdout.Bytes(), nil
}

func (u hostUpdater) compose(override string, args ...string) []string {
	a := []string{"docker", "compose", "--project-name", u.Project, "--env-file", u.EnvFile, "--file", u.Compose}
	if u.ExtraCompose != "" {
		a = append(a, "--file", u.ExtraCompose)
	}
	if override != "" {
		a = append(a, "--file", override)
	}
	return append(a, args...)
}
func (u hostUpdater) container() (string, error) {
	b, err := u.run(20*time.Second, u.compose("", "ps", "-q", "agent"), false)
	if err != nil {
		return "", err
	}
	id := strings.TrimSpace(string(b))
	if !regexp.MustCompile(`^[a-f0-9]{12,64}$`).MatchString(id) {
		return "", fmt.Errorf("expected one running agent container in project %s; deploy it before enabling updates", u.Project)
	}
	return id, nil
}
func (u hostUpdater) maintenance(container, action, lease string) (maintenanceStatus, error) {
	var s maintenanceStatus
	a := []string{"docker", "exec", "--user", "agent", container, "jag", "_maintenance", action}
	if lease != "" {
		a = append(a, lease)
	}
	b, err := u.run(20*time.Second, a, false)
	if err != nil {
		return s, fmt.Errorf("maintenance gate failed; deploy the current Jag image before enabling updates: %w", err)
	}
	if action != "end" {
		err = json.Unmarshal(b, &s)
	}
	return s, err
}
func (u hostUpdater) installed(container string) (string, error) {
	b, err := u.run(20*time.Second, []string{"docker", "exec", "--user", "agent", container, "codex", "--version"}, false)
	if err != nil {
		return "", err
	}
	return installedRelease(string(b))
}
func installedRelease(output string) (string, error) {
	p := strings.Fields(output)
	if len(p) != 2 || p[0] != "codex-cli" {
		return "", fmt.Errorf("unexpected codex --version output")
	}
	if _, err := parseRelease(p[1]); err != nil {
		return "", err
	}
	return p[1], nil
}
func parseRelease(s string) ([3]int, error) {
	var v [3]int
	if !regexp.MustCompile(`^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$`).MatchString(s) {
		return v, fmt.Errorf("expected a stable x.y.z Codex version")
	}
	for i, p := range strings.Split(s, ".") {
		n, err := strconv.Atoi(p)
		if err != nil {
			return v, err
		}
		v[i] = n
	}
	return v, nil
}
func newerRelease(target, current string) (bool, error) {
	a, err := parseRelease(target)
	if err != nil {
		return false, err
	}
	b, err := parseRelease(current)
	if err != nil {
		return false, err
	}
	for i := 0; i < 3; i++ {
		if a[i] != b[i] {
			return a[i] > b[i], nil
		}
	}
	return false, nil
}
func latestCodex() (string, error) {
	client := http.Client{Timeout: 15 * time.Second}
	res, err := client.Get("https://registry.npmjs.org/@openai%2fcodex/latest")
	if err != nil {
		return "", err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return "", fmt.Errorf("npm release check returned HTTP %d", res.StatusCode)
	}
	var doc struct{ Version string }
	if err = json.NewDecoder(io.LimitReader(res.Body, 2<<20)).Decode(&doc); err != nil {
		return "", err
	}
	if _, err = parseRelease(doc.Version); err != nil {
		return "", fmt.Errorf("npm latest is not a stable release: %w", err)
	}
	return doc.Version, nil
}

func replaceCodexPin(data []byte, version string) ([]byte, error) {
	if _, err := parseRelease(version); err != nil {
		return nil, err
	}
	re := regexp.MustCompile(`(?m)^(?:export[ \t]+)?CODEX_VERSION[ \t]*=[^\r\n]*`)
	if len(re.FindAllIndex(data, -1)) != 1 {
		return nil, fmt.Errorf("deployment env file must contain exactly one CODEX_VERSION assignment")
	}
	return re.ReplaceAllLiteral(data, []byte("CODEX_VERSION="+version)), nil
}

func codexPin(data []byte) (string, error) {
	re := regexp.MustCompile(`(?m)^(?:export[ \t]+)?CODEX_VERSION[ \t]*=([^\r\n]*)`)
	m := re.FindAllSubmatch(data, -1)
	if len(m) != 1 {
		return "", fmt.Errorf("env file must contain exactly one CODEX_VERSION assignment")
	}
	value := strings.TrimSpace(strings.SplitN(string(m[0][1]), "#", 2)[0])
	value = strings.Trim(value, "\"'")
	if _, err := parseRelease(value); err != nil {
		return "", err
	}
	return value, nil
}
func writeEnvPin(p string, original, newData []byte) error {
	current, err := os.ReadFile(p)
	if err != nil {
		return err
	}
	if sha256.Sum256(current) != sha256.Sum256(original) {
		return fmt.Errorf("deployment env file changed during update; refusing to overwrite it")
	}
	info, err := os.Stat(p)
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(p), ".jag-env-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if err = f.Chmod(info.Mode().Perm()); err == nil {
		_, err = f.Write(newData)
	}
	if e := f.Close(); err == nil {
		err = e
	}
	if err != nil {
		return err
	}
	return os.Rename(f.Name(), p)
}

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
			if value == "JAG_DOCKER=1" {
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
	tag := "jag-update-" + u.Project + ":" + target + "-" + hex.EncodeToString(raw[:4])
	override, err := os.CreateTemp(filepath.Dir(u.Compose), ".jag-update-*.json")
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
	smoke = append(smoke, "--entrypoint", "/bin/sh", tag, "-ec", `test "$(codex --version)" = "codex-cli `+target+`"; jag version; mkdir -p /home/agent/update-check; cd /home/agent/update-check; codex sandbox -c 'sandbox_mode="workspace-write"' -- /bin/sh -c 'printf update-ok > inside; if (printf forbidden > /home/agent/outside) 2>/dev/null; then exit 73; fi'; test "$(cat inside)" = update-ok; test ! -e /home/agent/outside`)
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
	rollbackTag := "jag-update-" + u.Project + ":previous-" + hex.EncodeToString(raw[:4])
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
		out, e := u.run(30*time.Second, []string{"docker", "exec", "--user", "agent", id, "jag", "_rpc", token(Request{Op: "doctor"})}, false)
		if e != nil {
			return e
		}
		if e = json.Unmarshal(out, &d); e != nil {
			return e
		}
		if !d.OK || d.Protocol != Protocol {
			return fmt.Errorf("deployed candidate failed Jag diagnostics")
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
