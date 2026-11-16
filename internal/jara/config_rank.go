package jara

import (
	"fmt"
	"sort"
)

// Remote hosts are ranked. The first is the default remote and the primary
// host; the others are standbys in order. A remote is added at the end, and
// `jara remote use` or `jara remote order` reorders them. Configurations
// from before ranking start with the default, then the rest by name.

// rankedRemotes lists the configured remotes, highest priority first.
func (c *Config) rankedRemotes() []string {
	seen := map[string]bool{}
	var ranked []string
	add := func(name string) {
		if _, ok := c.Remotes[name]; ok && !seen[name] {
			seen[name] = true
			ranked = append(ranked, name)
		}
	}
	add(c.Default)
	for _, name := range c.Order {
		add(name)
	}
	var rest []string
	for name := range c.Remotes {
		if !seen[name] {
			rest = append(rest, name)
		}
	}
	sort.Strings(rest)
	for _, name := range rest {
		add(name)
	}
	return ranked
}

// rankRemotes puts names first, in that order, ahead of the remaining
// remotes, and makes the first the default.
func (c *Config) rankRemotes(names []string) error {
	seen := map[string]bool{}
	for _, name := range names {
		if _, ok := c.Remotes[name]; !ok {
			return fmt.Errorf("unknown remote %q", name)
		}
		if seen[name] {
			return fmt.Errorf("remote %q is listed twice", name)
		}
		seen[name] = true
	}
	order := append([]string{}, names...)
	for _, name := range c.rankedRemotes() {
		if !seen[name] {
			order = append(order, name)
		}
	}
	c.Order = order
	if len(order) > 0 {
		c.Default = order[0]
	}
	return nil
}

// appendRemote ranks a newly added remote last; a remote that was already
// ranked keeps its place.
func (c *Config) appendRemote(name string) {
	for _, ranked := range c.Order {
		if ranked == name {
			return
		}
	}
	var order []string
	for _, ranked := range c.rankedRemotes() {
		if ranked != name {
			order = append(order, ranked)
		}
	}
	c.Order = append(order, name)
}

// remoteOrderCommand is `jara remote order [NAME...]`.
func remoteOrderCommand(c *Config, names []string) error {
	if len(names) == 0 {
		for i, name := range c.rankedRemotes() {
			role := "standby"
			if i == 0 {
				role = "primary, default"
			}
			fmt.Printf("%d. %s\t%s\t(%s)\n", i+1, name, c.Remotes[name].Host, role)
		}
		return nil
	}
	if err := c.rankRemotes(names); err != nil {
		return err
	}
	return saveConfig(*c)
}
