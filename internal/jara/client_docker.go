package jara

import (
	"flag"
	"fmt"
	"os"
	"time"

	"krystof.liskovec/jara/internal/textutil"
)

func dockerCommand(c Config, args []string) error {
	if len(args) == 0 || args[0] != "cleanup" {
		return fmt.Errorf("use `jara docker cleanup --help`")
	}
	f := flag.NewFlagSet("docker cleanup", flag.ContinueOnError)
	remoteName := f.String("remote", c.Default, "Jara remote")
	olderThan := f.String("older-than", "24h", "minimum time a container has been stopped")
	if err := f.Parse(args[1:]); err != nil {
		return err
	}
	if f.NArg() != 0 {
		return fmt.Errorf("unexpected docker cleanup argument %q", f.Arg(0))
	}
	if *remoteName == "" {
		return fmt.Errorf("select a Jara host with --remote or configure a default")
	}
	remote, ok := c.Remotes[*remoteName]
	if !ok {
		return fmt.Errorf("unknown remote %q", *remoteName)
	}
	if err := validateRemote(remote); err != nil {
		return err
	}
	age, err := dockerCleanupAge(*olderThan)
	if err != nil {
		return err
	}
	return cleanupDockerOnRemote(remote, *olderThan, age)
}

func dockerCleanupMenu(c Config) error {
	remoteName, err := chooseMenuRemote(c, "Clean stale Docker containers")
	if err != nil || remoteName == "" {
		return err
	}
	remote := c.Remotes[remoteName]
	return cleanupDockerOnRemote(remote, "24h", defaultDockerCleanupAge)
}

func cleanupDockerOnRemote(remote Remote, olderThan string, age time.Duration) error {
	var containers []staleDockerContainer
	if err := rpc(remote, Request{Op: "docker-stale-list", OlderThan: olderThan}, nil, &containers); err != nil {
		return err
	}
	if len(containers) == 0 {
		fmt.Fprintln(os.Stderr, "No exited Docker containers older than", age, "were found.")
		return nil
	}
	fmt.Fprintf(os.Stderr, "Exited Docker containers stopped at least %s ago on this Jara host:\n", age)
	fmt.Fprintln(os.Stderr, "  ID            NAME                          IMAGE                             AGE")
	for _, container := range containers {
		id := container.ID
		if len(id) > 12 {
			id = id[:12]
		}
		fmt.Fprintf(os.Stderr, "  %-12s  %-28s  %-32s  %s\n", textutil.FitText(id, 12), textutil.FitText(container.Name, 28), textutil.FitText(container.Image, 32), container.Age)
	}
	remove, err := confirm("Remove these containers? Their volumes will be kept.")
	if err != nil {
		return err
	}
	if !remove {
		fmt.Fprintln(os.Stderr, "Docker cleanup cancelled.")
		return nil
	}
	ids := make([]string, len(containers))
	for i, container := range containers {
		ids[i] = container.ID
	}
	if err = rpc(remote, Request{Op: "docker-stale-remove", OlderThan: olderThan, Containers: ids}, nil, nil); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "Removed %d stopped Docker container(s); volumes were kept.\n", len(ids))
	return nil
}
