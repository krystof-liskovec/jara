package jara

import (
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	"gitea.liskovec.dev/krystof.liskovec/jara/internal/fsutil"
)

type gitCredentialTarget struct {
	URL             string
	Host            string
	Path            string
	RewritePrefixes []string
}

type gitCredential struct {
	Username string
	Password string
}

var scpGitOriginRE = regexp.MustCompile(`^(?:([^@/:]+)@)?([^/:]+):(.+)$`)

func isSSHGitOrigin(origin string) bool {
	return strings.HasPrefix(origin, "ssh://") || !strings.Contains(origin, "://") && scpGitOriginRE.MatchString(origin)
}

func parseGitCredentialTarget(origin string) (gitCredentialTarget, error) {
	var target gitCredentialTarget
	var host, rewriteHost, repoPath, sshUser string
	if strings.Contains(origin, "://") {
		u, err := url.Parse(origin)
		if err != nil || u.RawQuery != "" || u.Fragment != "" {
			return target, fmt.Errorf("Git origin must not contain credentials, query parameters or fragments")
		}
		switch u.Scheme {
		case "https":
			if u.User != nil {
				return target, fmt.Errorf("Git origin must not contain credentials")
			}
			host, rewriteHost, repoPath = u.Host, u.Host, strings.TrimPrefix(u.Path, "/")
		case "ssh":
			if u.Port() != "" && u.Port() != "22" {
				return target, fmt.Errorf("SSH origin uses a custom port; use its HTTPS origin to configure Git credentials")
			}
			host, rewriteHost, repoPath = u.Hostname(), u.Host, strings.TrimPrefix(u.Path, "/")
			if u.User != nil {
				sshUser = u.User.Username()
			}
		default:
			return target, fmt.Errorf("Git credentials require an HTTPS or SSH origin")
		}
	} else {
		matched := scpGitOriginRE.FindStringSubmatch(origin)
		if matched == nil {
			return target, fmt.Errorf("Git origin must be an HTTPS URL, SSH URL or scp-style SSH URL")
		}
		sshUser, host, repoPath = matched[1], matched[2], matched[3]
		rewriteHost = host
	}
	host = strings.ToLower(host)
	rewriteHost = strings.ToLower(rewriteHost)
	if host == "" || strings.ContainsAny(host, "\r\n\x00 /\\") || repoPath == "" || strings.ContainsAny(repoPath, "\r\n\x00") {
		return target, fmt.Errorf("Git origin has an invalid host or repository path")
	}
	u := url.URL{Scheme: "https", Host: host, Path: "/" + strings.TrimPrefix(repoPath, "/")}
	target = gitCredentialTarget{URL: u.String(), Host: host, Path: strings.TrimPrefix(repoPath, "/")}
	if sshUser == "" {
		sshUser = "git"
	}
	if !regexp.MustCompile(`^[A-Za-z0-9._-]+$`).MatchString(sshUser) {
		return target, fmt.Errorf("Git SSH origin has an invalid username")
	}
	target.RewritePrefixes = []string{"ssh://" + rewriteHost + "/", "ssh://" + sshUser + "@" + rewriteHost + "/"}
	if rewriteHost == host {
		target.RewritePrefixes = append(target.RewritePrefixes, host+":", sshUser+"@"+host+":")
	}
	return target, nil
}

func credentialInput(target gitCredentialTarget, username, password string) string {
	return "protocol=https\nhost=" + target.Host + "\npath=" + target.Path + "\nusername=" + username + "\npassword=" + password + "\n\n"
}

func instanceCredentialInput(host, username, password string) string {
	return "protocol=https\nhost=" + host + "\nusername=" + username + "\npassword=" + password + "\n\n"
}

func parseCredentialFields(input string) map[string]string {
	values := make(map[string]string)
	for _, line := range strings.Split(input, "\n") {
		key, value, ok := strings.Cut(line, "=")
		if ok {
			values[key] = value
		}
	}
	return values
}

func storeGitCredential(destination, username, password string) error {
	target, err := parseGitCredentialTarget(destination)
	if err != nil {
		return err
	}
	if !strings.HasPrefix(destination, "https://") {
		return fmt.Errorf("stored Git credentials must use HTTPS")
	}
	root, err := filepath.Abs(fsutil.DataRoot())
	if err != nil {
		return err
	}
	authDir := filepath.Join(root, "auth")
	if err = os.MkdirAll(authDir, 0700); err != nil {
		return err
	}
	if err = os.Chmod(authDir, 0700); err != nil {
		return err
	}
	credentialFile := filepath.Join(authDir, "git-credentials")
	file, err := os.OpenFile(credentialFile, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	if err = file.Chmod(0600); err != nil {
		file.Close()
		return err
	}
	if err = file.Close(); err != nil {
		return err
	}
	if err = removeLegacyPathCredentials(target.Host, credentialFile); err != nil {
		return err
	}
	helper := "store --file=" + quote(credentialFile)
	helperKey := "credential.https://" + target.Host + ".helper"
	if err = runGitConfig("--global", "--replace-all", helperKey, ""); err != nil {
		return fmt.Errorf("configure Git credential helper")
	}
	if err = runGitConfig("--global", "--add", helperKey, helper); err != nil {
		return err
	}
	if err = runGitConfig("--global", "credential.https://"+target.Host+".useHttpPath", "false"); err != nil {
		return err
	}
	for _, prefix := range target.RewritePrefixes {
		if err = ensureGitConfig("url.https://"+target.Host+"/.insteadOf", prefix); err != nil {
			return err
		}
	}
	input := instanceCredentialInput(target.Host, username, password)
	cmd := exec.Command("git", "credential-store", "--file="+credentialFile, "erase")
	cmd.Stdin = strings.NewReader(instanceCredentialInput(target.Host, "", ""))
	if err = cmd.Run(); err != nil {
		return fmt.Errorf("replace stored HTTPS Git credential")
	}
	cmd = exec.Command("git", "credential-store", "--file="+credentialFile, "store")
	cmd.Stdin = strings.NewReader(input)
	if err = cmd.Run(); err != nil {
		return fmt.Errorf("store HTTPS Git credential")
	}
	if err = os.Chmod(credentialFile, 0600); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "Git credentials for %s are now shared by repositories on this instance.\n", target.Host)
	return nil
}

func removeLegacyPathCredentials(host, credentialFile string) error {
	cmd := exec.Command("git", "config", "--global", "--get-regexp", `^credential\.`)
	output, err := cmd.Output()
	if err != nil {
		if exit, ok := err.(*exec.ExitError); ok && exit.ExitCode() == 1 {
			return nil
		}
		return fmt.Errorf("read Git credential configuration")
	}
	managedHelper := "store --file=" + quote(credentialFile)
	prefix := "credential.https://" + host + "/"
	legacy := make(map[string]gitCredentialTarget)
	pathSetting := make(map[string]bool)
	for _, line := range strings.Split(strings.TrimSpace(string(output)), "\n") {
		key, value, ok := strings.Cut(line, " ")
		if !ok || !strings.HasPrefix(key, prefix) {
			continue
		}
		if strings.HasSuffix(key, ".useHttpPath") && strings.EqualFold(value, "true") {
			pathSetting[strings.TrimSuffix(key, ".useHttpPath")] = true
		}
		if strings.HasSuffix(key, ".helper") && value == managedHelper {
			url := strings.TrimSuffix(strings.TrimPrefix(key, "credential."), ".helper")
			target, parseErr := parseGitCredentialTarget(url)
			if parseErr == nil && target.Host == host {
				legacy[url] = target
			}
		}
	}
	for url, target := range legacy {
		if err = runGitConfig("--global", "--unset-all", "credential."+url+".helper"); err != nil {
			return fmt.Errorf("remove old repository-scoped Git credential helper")
		}
		cmd = exec.Command("git", "credential-store", "--file="+credentialFile, "erase")
		cmd.Stdin = strings.NewReader(credentialInput(target, "", ""))
		if err = cmd.Run(); err != nil {
			return fmt.Errorf("remove old repository-scoped Git credential")
		}
		if pathSetting[url] {
			if err = runGitConfig("--global", "--replace-all", "credential."+url+".useHttpPath", "false"); err != nil {
				return fmt.Errorf("migrate Git credential path setting")
			}
		}
	}
	return nil
}

func ensureGitConfig(key, value string) error {
	cmd := exec.Command("git", "config", "--global", "--get-all", key)
	output, err := cmd.Output()
	if err == nil {
		for _, existing := range strings.Split(strings.TrimSpace(string(output)), "\n") {
			if existing == value {
				return nil
			}
		}
	} else if exit, ok := err.(*exec.ExitError); !ok || exit.ExitCode() != 1 {
		return fmt.Errorf("read Git configuration %s", key)
	}
	if err = runGitConfig("--global", "--add", key, value); err != nil {
		return fmt.Errorf("configure Git %s", key)
	}
	return nil
}

func runGitConfig(args ...string) error {
	cmd := exec.Command("git", append([]string{"config"}, args...)...)
	if err := cmd.Run(); err != nil {
		return err
	}
	return nil
}
