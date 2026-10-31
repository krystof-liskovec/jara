package jara

import (
	"bytes"
	"context"
	"crypto/sha256"
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
		return fmt.Errorf("use jara host update --project NAME [--native] [--compose FILE] [--env-file FILE] [--check]")
	}
	if runtime.GOOS != "linux" {
		return fmt.Errorf("run host update on the Linux Docker host")
	}
	f := flag.NewFlagSet("host update", flag.ContinueOnError)
	project := f.String("project", "", "deployment name (required)")
	native := f.Bool("native", false, "use the native Docker deployment manifest")
	compose := f.String("compose", envDefault("JARA_COMPOSE_FILE", "deploy/compose.yaml"), "Compose file")
	extra := f.String("compose-extra", os.Getenv("JARA_COMPOSE_EXTRA"), "optional Compose overlay (e.g. deploy/compose.docker.yaml)")
	env := f.String("env-file", envDefault("JARA_ENV_FILE", "deploy/.env"), "deployment env file")
	manifest := f.String("native-manifest", envDefault("JARA_NATIVE_MANIFEST", "deploy/.jara-native.json"), "native Docker deployment manifest")
	check := f.Bool("check", false, "report available stable update without building or deploying")
	version := f.String("version", "", "explicit stable version; default: npm latest")
	if err := f.Parse(args[1:]); err != nil {
		return err
	}
	if f.NArg() != 0 {
		return fmt.Errorf("unexpected update argument")
	}
	if !regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,62}$`).MatchString(*project) {
		return fmt.Errorf("supply the deployment project name using lowercase letters, digits, underscores or hyphens")
	}
	cp, err := filepath.Abs(*compose)
	if err != nil {
		return err
	}
	ep, err := filepath.Abs(*env)
	if err != nil {
		return err
	}
	mp, err := filepath.Abs(*manifest)
	if err != nil {
		return err
	}
	if _, statErr := os.Stat(mp); *native || statErr == nil {
		if statErr != nil {
			return fmt.Errorf("native deployment manifest not found: %s", mp)
		}
		return updateNativeDeployment(*project, mp, ep, *version, *check, runHostCommand, latestCodex, os.Stdout)
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

func envDefault(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
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
	a := []string{"docker", "exec", "--user", "agent", container, "jara", "_maintenance", action}
	if lease != "" {
		a = append(a, lease)
	}
	b, err := u.run(20*time.Second, a, false)
	if err != nil {
		return s, fmt.Errorf("maintenance gate failed; deploy the current Jara image before enabling updates: %w", err)
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
	f, err := os.CreateTemp(filepath.Dir(p), ".jara-env-*")
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
