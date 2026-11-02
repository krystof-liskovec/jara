package jara

import (
	"encoding/json"
	"fmt"
	"net"
	"path"
	"strings"
	"time"
)

const nativeDeployDirToken = "@JARA_DEPLOY_DIR@"

type nativeDeployment struct {
	Version   int    `json:"version"`
	Name      string `json:"name"`
	Project   string `json:"project"`
	Container string `json:"container"`
	Image     string `json:"image"`
	Bind      string `json:"bind"`
	Port      int    `json:"port"`
	SSHUser   string `json:"ssh_user,omitempty"`
}

func newNativeDeployment(name, bind string, port int) nativeDeployment {
	lower := strings.ToLower(name)
	return nativeDeployment{Version: 1, Name: name, Project: "jara-" + lower,
		Container: "jara-agent-" + lower, Image: "jara-agent-" + lower, Bind: bind, Port: port, SSHUser: "agent"}
}

func (d nativeDeployment) validate(project string) error {
	if d.Version != 1 || !nameRE.MatchString(d.Name) || d.Project != "jara-"+strings.ToLower(d.Name) ||
		d.Container != "jara-agent-"+strings.ToLower(d.Name) || d.Image != d.Container ||
		project != "" && project != d.Project || d.Port < 1 || d.Port > 65535 {
		return fmt.Errorf("invalid native deployment manifest")
	}
	if d.SSHUser != "" && !validSSHUser(d.SSHUser) {
		return fmt.Errorf("native deployment manifest has an invalid SSH username")
	}
	bind := net.ParseIP(d.Bind)
	if bind == nil || bind.To4() == nil {
		return fmt.Errorf("native deployment manifest has an invalid IPv4 bind address")
	}
	return nil
}

func nativeManifestBytes(d nativeDeployment) ([]byte, error) {
	return json.MarshalIndent(d, "", "  ")
}

func nativeBuildArgs(settings map[string]string, image, codexVersion string) []string {
	args := []string{"docker", "build", "--tag", image, "--file", "deploy/Dockerfile"}
	for _, key := range []string{"CODEX_VERSION", "NODE_VERSION", "GO_VERSION", "EDITOR_PACKAGES", "GLAB_VERSION", "TEA_VERSION", "DOCKER_VERSION", "MISE_VERSION"} {
		value := settings[key]
		if key == "CODEX_VERSION" && codexVersion != "" {
			value = codexVersion
		}
		args = append(args, "--build-arg", key+"="+value)
	}
	return append(args, ".")
}

func nativeRunArgs(d nativeDeployment, settings map[string]string, image, deployDir, apparmorProfile string) []string {
	args := []string{"docker", "run", "--detach", "--name", d.Container,
		"--restart", "unless-stopped", "--init", "--security-opt",
		"seccomp=" + path.Join(deployDir, "deploy/seccomp/codex.json"),
		"--publish", fmt.Sprintf("%s:%d:22", d.Bind, d.Port),
		"--volume", d.Project + "-agent-home:/home/agent",
		"--volume", d.Project + "-host-keys:/var/lib/jara/ssh",
		"--volume", path.Join(deployDir, "deploy/authorized_keys") + ":/run/jara/authorized_keys:ro",
		"--volume", "/dev/null:/run/secrets/gitlab_token:ro",
		"--volume", "/dev/null:/run/secrets/gitea_token:ro",
		"--label", "io.jara.managed=true", "--label", "io.jara.name=" + d.Name,
		"--label", "io.jara.project=" + d.Project,
		"--health-cmd", "/usr/sbin/sshd -t && test -r /etc/ssh/authorized_keys/" + nativeSSHUser(d),
		"--health-interval", "30s", "--health-timeout", "5s", "--health-retries", "3"}
	if apparmorProfile != "" {
		args = append(args, "--security-opt", "apparmor="+apparmorProfile)
	}
	for _, key := range []string{"JARA_DOCKER", "JARA_GATEWAY_PORTS", "JARA_CODEX_ARGS", "JARA_TMUX_BACKGROUND", "JARA_MAX_SESSIONS", "JARA_SESSION_IDLE_TIMEOUT", "GITLAB_HOST", "GITEA_URL"} {
		args = append(args, "--env", key+"="+settings[key])
	}
	args = append(args, "--env", "JARA_SSH_USER="+nativeSSHUser(d))
	return append(args, image)
}

func nativeSSHUser(d nativeDeployment) string {
	if d.SSHUser == "" {
		return "agent"
	}
	return d.SSHUser
}

func nativeAppArmorOption(run func(time.Duration, []string, bool) ([]byte, error), container string) (string, error) {
	b, err := run(20*time.Second, []string{"docker", "inspect", "--format", "{{json .HostConfig.SecurityOpt}}", container}, false)
	if err != nil {
		return "", err
	}
	var options []string
	if err := json.Unmarshal(b, &options); err != nil {
		return "", fmt.Errorf("decode Docker security options: %w", err)
	}
	profile := ""
	for _, option := range options {
		name, value, ok := strings.Cut(option, "=")
		if !ok || name != "apparmor" || value == "" {
			continue
		}
		if profile != "" && profile != value {
			return "", fmt.Errorf("container has conflicting AppArmor profiles")
		}
		profile = value
	}
	return profile, nil
}

func renderNativeShellArgs(args []string) string {
	quoted := make([]string, len(args))
	for i, arg := range args {
		if before, after, ok := strings.Cut(arg, nativeDeployDirToken); ok {
			quoted[i] = quote(before) + `"$PWD"` + quote(after)
		} else {
			quoted[i] = quote(arg)
		}
	}
	return strings.Join(quoted, " ")
}
