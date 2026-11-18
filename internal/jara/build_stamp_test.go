package jara

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func TestVersionOutputStaysPlainAndCandidatesMatchTheBuild(t *testing.T) {
	saved := buildStampData
	t.Cleanup(func() { buildStampData = saved })
	buildStampData = []byte(`{"time":1795030517,"source":"4d9a0c554309b519dd22475ba4de33c035d9903c"}`)
	if versionText(nil) != Version {
		t.Fatalf("plain %q", versionText(nil))
	}
	if got := versionText([]string{"--build"}); !strings.HasPrefix(got, Version+" (built ") || !strings.HasSuffix(got, ", source 4d9a0c554309)") {
		t.Fatalf("build %q", got)
	}
	candidate := func(output string, err error) func(time.Duration, []string, bool) ([]byte, error) {
		return func(time.Duration, []string, bool) ([]byte, error) { return []byte(output), err }
	}
	if err := checkCandidateBuild(candidate(versionText([]string{"--build"}), nil), "img"); err != nil {
		t.Fatal(err)
	}
	for _, other := range []func(time.Duration, []string, bool) ([]byte, error){
		candidate(Version+" (built 2026-11-18 19:00, source 000000000000)", nil), candidate(Version, nil), candidate("", errors.New("no --build")),
	} {
		if checkCandidateBuild(other, "img") == nil {
			t.Fatal("accepted a candidate from another source")
		}
	}
	buildStampData = []byte(`{}`) // a plain go build checks nothing more
	if checkCandidateBuild(candidate("", errors.New("x")), "img") != nil || versionText([]string{"--build"}) != Version+" (unknown build)" {
		t.Fatal("unknown build")
	}
}
