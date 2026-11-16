package jara

import (
	"strings"
	"testing"
	"time"
)

func TestRemotesAreRankedDefaultFirstThenAdded(t *testing.T) {
	c := Config{Default: "jelly", Remotes: map[string]Remote{"jelly": {Host: "j"}, "zeta": {Host: "z"}, "alpha": {Host: "a"}}}
	if got := strings.Join(c.rankedRemotes(), " "); got != "jelly alpha zeta" {
		t.Fatalf("configuration from before ranking = %s", got)
	}
	c.Remotes["backup"] = Remote{Host: "b"}
	c.appendRemote("backup")
	if got := strings.Join(c.rankedRemotes(), " "); got != "jelly alpha zeta backup" {
		t.Fatalf("an added remote must rank last: %s", got)
	}
	if err := c.rankRemotes([]string{"backup", "zeta"}); err != nil || c.Default != "backup" {
		t.Fatalf("reorder: default %s, %v", c.Default, err)
	}
	if got := strings.Join(c.rankedRemotes(), " "); got != "backup zeta jelly alpha" {
		t.Fatalf("reordered = %s", got)
	}
	if c.rankRemotes([]string{"nope"}) == nil || c.rankRemotes([]string{"zeta", "zeta"}) == nil {
		t.Fatal("unknown or repeated remotes must be refused")
	}
	forgetRemote(&c, "backup")
	if got := strings.Join(c.rankedRemotes(), " "); got != "zeta jelly alpha" || c.Default != "zeta" {
		t.Fatalf("after removing the default: %s (default %s)", got, c.Default)
	}
}

func TestPreferredWaitingDeviceGetsTheHandBack(t *testing.T) {
	now := time.Now().Unix()
	waited := now - 60 // the windows started waiting before the hand-back
	lease := runnerLease{Prefers: []string{"laptop", "desktop"}, Wants: map[string]int64{"desktop": waited, "laptop": waited, "tablet": waited}}
	if lease.yieldsTo("desktop") != "" {
		t.Fatal("devices yield only after the host handed back")
	}
	lease.HandBackAt = now
	if got := lease.yieldsTo("desktop"); got != "laptop" {
		t.Fatalf("desktop yields to %q", got)
	}
	if got := lease.yieldsTo("tablet"); got != "laptop" {
		t.Fatalf("an unranked device yields to %q", got)
	}
	if got := lease.yieldsTo("laptop"); got != "" {
		t.Fatalf("the preferred device yields to %q", got)
	}
	// The laptop's window closed: after the grace the desktop may claim, and
	// the next round forgets the laptop.
	lease.HandBackAt = now - int64(runnerHandBackGrace/time.Second) - 1
	if lease.yieldsTo("desktop") != "" {
		t.Fatal("a device that never came back blocks the others")
	}
	lease.Wants["desktop"] = now // waiting again for the next round
	lease.startWaitRound()
	if _, ok := lease.Wants["laptop"]; ok || lease.HandBackAt != 0 || len(lease.Wants) != 1 {
		t.Fatalf("stale waits kept: %+v", lease)
	}
}
