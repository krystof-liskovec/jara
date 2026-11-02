package jara

import (
	"flag"
	"fmt"
	"net/url"
	"os"
	"path"
	"strings"
)

func cloneRepositoryCommand(c Config, args []string) error {
	f := flag.NewFlagSet("clone", flag.ContinueOnError)
	remoteName := f.String("remote", c.Default, "remote Jara host")
	if err := f.Parse(args); err != nil {
		return err
	}
	if f.NArg() != 1 {
		return fmt.Errorf("usage: jara clone [--remote NAME] REPOSITORY_URL")
	}
	origin, err := validateRepositoryCloneURL(f.Arg(0))
	if err != nil {
		return err
	}
	if *remoteName == "" {
		return fmt.Errorf("select a Jara host with --remote or configure a default using `jara remote use NAME`")
	}
	remote, ok := c.Remotes[*remoteName]
	if !ok {
		return fmt.Errorf("unknown remote %q", *remoteName)
	}
	if err = validateRemote(remote); err != nil {
		return err
	}
	return cloneAndOpenRepository(remote, origin)
}

func browseRepositories(c Config) error {
	remoteName, err := chooseMenuRemote(c, "Browse repositories")
	if err != nil || remoteName == "" {
		return err
	}
	remote := c.Remotes[remoteName]
	var providers []RepositoryProvider
	if err = rpc(remote, Request{Op: "repository-providers"}, nil, &providers); err != nil {
		return err
	}
	if len(providers) == 0 {
		return fmt.Errorf("no GitLab or Gitea token is configured on %s; configure one with `jara auth gitlab --remote %s --host HOST --token-stdin` or `jara auth gitea --remote %s --url HTTPS_URL --token-stdin`", remoteName, remoteName, remoteName)
	}
	providerItems := make([]menuItem, len(providers))
	for i, provider := range providers {
		providerItems[i] = menuItem{Title: strings.ToUpper(provider.Name), Detail: provider.Host, Kind: "remote", Action: provider.Name}
	}
	selected, err := selectMenu("Repository provider", providerItems)
	if err != nil {
		return err
	}
	provider := providers[selected]
	var repositories []RemoteRepository
	if err = rpc(remote, Request{Op: "repositories", Provider: provider.Name}, nil, &repositories); err != nil {
		return err
	}
	if len(repositories) == 0 {
		return fmt.Errorf("no repositories are available through %s on %s", provider.Name, provider.Host)
	}
	items := make([]menuItem, 0, len(repositories))
	for _, repository := range repositories {
		detail := repository.Description
		if repository.Private {
			if detail != "" {
				detail += " · "
			}
			detail += "private"
		}
		if detail == "" {
			detail = provider.Host
		}
		items = append(items, menuItem{Title: repository.Name, Detail: detail, Kind: "project", Action: repository.CloneURL})
	}
	selected, err = selectMenu("Repositories on "+provider.Host, items)
	if err != nil {
		return err
	}
	origin, err := validateRepositoryCloneURL(items[selected].Action)
	if err != nil {
		return fmt.Errorf("provider returned an invalid clone URL: %w", err)
	}
	return cloneAndOpenRepository(remote, origin)
}

func cloneAndOpenRepository(remote Remote, origin string) error {
	name := repositoryName(origin)
	id := workspaceID("url-clone", origin)
	if err := rpc(remote, Request{Op: "clone", ID: id, Name: name, URL: origin}, nil, nil); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "Opening %s on the Jara host.\n", name)
	return openRemote(remote, Request{ID: id})
}

func repositoryName(origin string) string {
	u, err := url.Parse(origin)
	if err != nil {
		return "repository"
	}
	name := strings.TrimSuffix(path.Base(strings.TrimRight(u.Path, "/")), ".git")
	if name == "" || name == "." || name == "/" {
		return "repository"
	}
	return name
}
