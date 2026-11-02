package jara

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

type providerCredential struct {
	Host  string `json:"host"`
	Token string `json:"token"`
}

type RepositoryProvider struct {
	Name string `json:"name"`
	Host string `json:"host"`
}

type RemoteRepository struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	CloneURL    string `json:"clone_url"`
	WebURL      string `json:"web_url,omitempty"`
	Private     bool   `json:"private,omitempty"`
}

func providerCredentialPath(provider string) string {
	return filepath.Join(dataRoot(), "auth", provider+"-repository-browser.json")
}

func saveProviderCredential(provider, host, secret string) error {
	if provider != "gitlab" && provider != "gitea" {
		return nil
	}
	return writeJSON(providerCredentialPath(provider), providerCredential{Host: host, Token: secret})
}

func readProviderCredential(provider string) (providerCredential, error) {
	var credential providerCredential
	if provider != "gitlab" && provider != "gitea" {
		return credential, fmt.Errorf("unsupported repository provider %q", provider)
	}
	data, err := os.ReadFile(providerCredentialPath(provider))
	if err != nil {
		if os.IsNotExist(err) {
			return credential, fmt.Errorf("configure %s access first with `jara auth %s`", provider, provider)
		}
		return credential, err
	}
	if err = json.Unmarshal(data, &credential); err != nil {
		return credential, fmt.Errorf("read saved %s access: %w", provider, err)
	}
	if credential.Host == "" || credential.Token == "" {
		return credential, fmt.Errorf("saved %s access is incomplete; run `jara auth %s` again", provider, provider)
	}
	if err = validateProvider(provider, credential.Host); err != nil {
		return credential, fmt.Errorf("saved %s host is invalid: %w", provider, err)
	}
	return credential, nil
}

func remoteRepositoryProviders() []RepositoryProvider {
	var providers []RepositoryProvider
	for _, name := range []string{"gitlab", "gitea"} {
		credential, err := readProviderCredential(name)
		if err == nil {
			providers = append(providers, RepositoryProvider{Name: name, Host: credential.Host})
		}
	}
	return providers
}

func remoteRepositories(provider string) ([]RemoteRepository, error) {
	if provider != "gitlab" && provider != "gitea" {
		return nil, fmt.Errorf("unsupported repository provider %q", provider)
	}
	credential, err := readProviderCredential(provider)
	if err != nil {
		return nil, err
	}
	switch provider {
	case "gitlab":
		return gitLabRepositories(credential)
	case "gitea":
		return giteaRepositories(credential)
	default:
		return nil, fmt.Errorf("unsupported repository provider %q", provider)
	}
}

func gitLabRepositories(credential providerCredential) ([]RemoteRepository, error) {
	base := "https://" + credential.Host + "/api/v4/projects"
	var repositories []RemoteRepository
	for page := 1; page <= 20; page++ {
		endpoint, _ := url.Parse(base)
		query := endpoint.Query()
		query.Set("membership", "true")
		query.Set("simple", "true")
		query.Set("per_page", "100")
		query.Set("order_by", "last_activity_at")
		query.Set("page", strconv.Itoa(page))
		endpoint.RawQuery = query.Encode()
		var projects []struct {
			PathWithNamespace string `json:"path_with_namespace"`
			HTTPURLToRepo     string `json:"http_url_to_repo"`
			WebURL            string `json:"web_url"`
			Description       string `json:"description"`
			Visibility        string `json:"visibility"`
		}
		next, err := providerGet("gitlab", credential, endpoint.String(), &projects)
		if err != nil {
			return nil, err
		}
		for _, project := range projects {
			if project.PathWithNamespace != "" && project.HTTPURLToRepo != "" {
				repositories = append(repositories, RemoteRepository{Name: project.PathWithNamespace, Description: project.Description, CloneURL: project.HTTPURLToRepo, WebURL: project.WebURL, Private: project.Visibility == "private"})
			}
		}
		if next == "" {
			return repositories, nil
		}
	}
	return repositories, fmt.Errorf("GitLab returned more than 2,000 repositories; narrow your access token's project scope")
}

func giteaRepositories(credential providerCredential) ([]RemoteRepository, error) {
	base, err := url.Parse(credential.Host)
	if err != nil {
		return nil, fmt.Errorf("saved Gitea URL is invalid")
	}
	base.Path = strings.TrimRight(base.Path, "/") + "/api/v1/user/repos"
	var repositories []RemoteRepository
	for page := 1; page <= 20; page++ {
		endpoint := *base
		query := endpoint.Query()
		query.Set("limit", "50")
		query.Set("page", strconv.Itoa(page))
		endpoint.RawQuery = query.Encode()
		var repos []struct {
			FullName    string `json:"full_name"`
			CloneURL    string `json:"clone_url"`
			HTMLURL     string `json:"html_url"`
			Description string `json:"description"`
			Private     bool   `json:"private"`
		}
		_, err = providerGet("gitea", credential, endpoint.String(), &repos)
		if err != nil {
			return nil, err
		}
		for _, repo := range repos {
			if repo.FullName != "" && repo.CloneURL != "" {
				repositories = append(repositories, RemoteRepository{Name: repo.FullName, Description: repo.Description, CloneURL: repo.CloneURL, WebURL: repo.HTMLURL, Private: repo.Private})
			}
		}
		if len(repos) < 50 {
			return repositories, nil
		}
	}
	return repositories, fmt.Errorf("Gitea returned more than 1,000 repositories; narrow your access token's repository scope")
}

func providerGet(provider string, credential providerCredential, endpoint string, result any) (string, error) {
	ctxClient := &http.Client{Timeout: 20 * time.Second, CheckRedirect: func(request *http.Request, via []*http.Request) error {
		if len(via) > 0 && (!strings.EqualFold(request.URL.Host, via[0].URL.Host) || request.URL.Scheme != via[0].URL.Scheme) {
			return http.ErrUseLastResponse
		}
		return nil
	}}
	request, err := http.NewRequest(http.MethodGet, endpoint, nil)
	if err != nil {
		return "", fmt.Errorf("build %s repository request: %w", provider, err)
	}
	request.Header.Set("Accept", "application/json")
	if provider == "gitlab" {
		request.Header.Set("PRIVATE-TOKEN", credential.Token)
	} else {
		request.Header.Set("Authorization", "token "+credential.Token)
	}
	response, err := ctxClient.Do(request)
	if err != nil {
		return "", fmt.Errorf("contact %s: %w", provider, err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
		return "", fmt.Errorf("%s repository request failed with HTTP %d; verify the token and host with `jara auth %s`", provider, response.StatusCode, provider)
	}
	if err = json.NewDecoder(io.LimitReader(response.Body, 8<<20)).Decode(result); err != nil {
		return "", fmt.Errorf("decode %s repository response: %w", provider, err)
	}
	return response.Header.Get("X-Next-Page"), nil
}

func validateRepositoryCloneURL(origin string) (string, error) {
	origin = strings.TrimSpace(origin)
	if origin == "" || strings.HasPrefix(origin, "-") || strings.ContainsAny(origin, "\x00\r\n") {
		return "", fmt.Errorf("provide a valid HTTPS or SSH repository URL")
	}
	if isSSHGitOrigin(origin) {
		target, err := parseGitCredentialTarget(origin)
		if err != nil {
			return "", err
		}
		return target.URL, nil
	}
	u, err := url.Parse(origin)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.Path == "" || u.Path == "/" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return "", fmt.Errorf("repository URL must use HTTPS or standard SSH and must not embed credentials, query or fragment")
	}
	return u.String(), nil
}

func remoteCloneRepository(q Request) error {
	if !nameRE.MatchString(q.ID) {
		return fmt.Errorf("invalid workspace ID")
	}
	origin, err := validateRepositoryCloneURL(q.URL)
	if err != nil {
		return err
	}
	unlock, err := lockWorkspace(q.ID)
	if err != nil {
		return err
	}
	defer unlock()
	destination, err := workspacePath(q.ID)
	if err != nil {
		return err
	}
	if _, err = os.Lstat(destination); err == nil {
		if existing, infoErr := workspaceInfo(q.ID); infoErr == nil && existing.Ready && existing.URL == origin {
			return nil
		}
		return fmt.Errorf("destination already exists and does not match this repository: %s", destination)
	} else if !os.IsNotExist(err) {
		return err
	}
	cmd := exec.Command("git", "clone", "--", origin, destination)
	cmd.Stdout, cmd.Stderr = os.Stderr, os.Stderr
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	if err = cmd.Run(); err != nil {
		_ = os.RemoveAll(destination)
		return fmt.Errorf("clone failed (%w); verify the URL and configure Git access on the remote with `jara auth gitlab`, `jara auth gitea`, or `jara auth git`", err)
	}
	branch, _ := gitOutput(destination, "symbolic-ref", "--short", "-q", "HEAD")
	workspace := Workspace{ID: q.ID, Name: q.Name, Path: destination, URL: origin, Branch: branch, Created: time.Now().UTC().Format(time.RFC3339), Ready: true}
	if err = writeJSON(destination+".json", workspace); err != nil {
		_ = os.RemoveAll(destination)
		return err
	}
	return json.NewEncoder(os.Stdout).Encode(workspace)
}
