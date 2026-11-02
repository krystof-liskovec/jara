package jara

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

type clientBuildInfo struct {
	Version string `json:"version"`
	GOOS    string `json:"goos"`
	GOARCH  string `json:"goarch"`
}

func streamClientBinary() error {
	binary, err := os.ReadFile("/opt/jara/bin/jara-client")
	if err != nil {
		return fmt.Errorf("client binary unavailable: %w", err)
	}
	_, err = os.Stdout.Write(binary)
	return err
}

func offerServerClientUpdate(run func(time.Duration, []string, bool) ([]byte, error), container, deployment, currentVersion string, log io.Writer) (bool, error) {
	request := token(Request{Op: "client-version"})
	data, err := run(20*time.Second, []string{"docker", "exec", "--user", "1001", container, "jara", "_rpc", request}, false)
	if err != nil {
		return false, nil
	}
	var build clientBuildInfo
	if json.Unmarshal(data, &build) != nil || build.GOOS != runtime.GOOS || build.GOARCH != runtime.GOARCH || !versionNewer(build.Version, currentVersion) {
		return false, nil
	}
	yes, err := confirmWithReader(bufio.NewReader(os.Stdin), fmt.Sprintf("Server has a newer Jara client (%s). Install it before upgrading?", build.Version))
	if err != nil || !yes {
		return false, err
	}
	binary, err := run(30*time.Second, []string{"docker", "exec", "--user", "1001", container, "cat", "/opt/jara/bin/jara-client"}, false)
	if err != nil {
		return false, fmt.Errorf("download server's Jara client: %w", err)
	}
	if err = installClientBinary(binary, build.Version); err != nil {
		return false, err
	}
	fmt.Fprintf(log, "Run `jara deployment upgrade %s` again with the updated client.\n", deployment)
	return true, nil
}

func versionNewer(candidate, current string) bool {
	parts := func(s string) ([]int, string, bool) {
		base, suffix, _ := strings.Cut(strings.TrimPrefix(s, "v"), "-")
		fields := strings.Split(base, ".")
		if len(fields) != 3 {
			return nil, "", false
		}
		values := make([]int, 3)
		for i, field := range fields {
			value, err := strconv.Atoi(field)
			if err != nil || value < 0 {
				return nil, "", false
			}
			values[i] = value
		}
		return values, suffix, true
	}
	a, ap, aok := parts(candidate)
	b, bp, bok := parts(current)
	if !aok || !bok {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return a[i] > b[i]
		}
	}
	return ap == "" && bp != ""
}

func installClientBinary(binary []byte, version string) error {
	if len(binary) < 4 || !bytes.HasPrefix(binary, []byte{0x7f, 'E', 'L', 'F'}) {
		return fmt.Errorf("server returned an invalid Linux Jara binary")
	}
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	executable, err = filepath.EvalSymlinks(executable)
	if err != nil {
		return err
	}
	dir := filepath.Dir(executable)
	temp, err := os.CreateTemp(dir, ".jara-update-*")
	if err != nil {
		return fmt.Errorf("cannot write beside %s: %w", executable, err)
	}
	tempPath := temp.Name()
	defer os.Remove(tempPath)
	if err = temp.Chmod(0755); err == nil {
		_, err = temp.Write(binary)
	}
	if closeErr := temp.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	check := exec.Command(tempPath, "version")
	output, err := check.Output()
	if err != nil || strings.TrimSpace(string(output)) != version {
		return fmt.Errorf("downloaded client failed version verification")
	}
	if err = os.Rename(tempPath, executable); err != nil {
		return fmt.Errorf("replace %s: %w", executable, err)
	}
	fmt.Fprintf(os.Stderr, "Updated Jara client to %s. Restart Jara to use it.\n", version)
	return nil
}
