package jara

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// build_stamp.json names the source this binary was built from and when it
// was bundled; install.sh and host builds fill it in, while a plain go build
// keeps the committed empty one, which compares as unknown.
//
//go:embed build_stamp.json
var buildStampData []byte

type buildStamp struct {
	Time   int64  `json:"time,omitempty"`
	Source string `json:"source,omitempty"`
}

func thisBuild() buildStamp {
	var stamp buildStamp
	_ = json.Unmarshal(buildStampData, &stamp)
	return stamp
}

// olderThan reports whether b is a different, earlier build than other;
// unknown builds are never older.
func (b buildStamp) olderThan(other buildStamp) bool {
	return b.Source != "" && other.Source != "" && b.Source != other.Source && b.Time < other.Time
}

// describe tells when and from what source the build was made, or "".
func (b buildStamp) describe() string {
	if b.Source == "" {
		return ""
	}
	return " (built " + time.Unix(b.Time, 0).Local().Format("2006-01-02 15:04") + ", source " + b.Source[:12] + ")"
}

// versionText is what jara version prints: the version alone, which scripts
// and upgrades read, or with --build also the build stamp.
func versionText(args []string) string {
	if len(args) == 1 && args[0] == "--build" {
		if describe := thisBuild().describe(); describe != "" {
			return Version + describe
		}
		return Version + " (unknown build)"
	}
	return Version
}

// checkCandidateBuild makes sure an upgrade's freshly built Jara comes from
// this client's bundle, when this client knows its build.
func checkCandidateBuild(run func(time.Duration, []string, bool) ([]byte, error), candidate string) error {
	want := thisBuild().Source
	if want == "" {
		return nil
	}
	out, err := run(60*time.Second, []string{"docker", "run", "--rm", "--network=none", "--entrypoint", "/opt/jara/bin/jara", candidate, "version", "--build"}, false)
	if err != nil || !strings.Contains(string(out), "source "+want[:12]) {
		return fmt.Errorf("candidate Jara was not built from this CLI's source (%s)", want[:12])
	}
	return nil
}
