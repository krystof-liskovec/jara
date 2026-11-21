package jara

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"
)

// deviceLinkCheck is how often the daemon reconciles its links with the
// configuration: allowing or denying calls, and adding or removing remotes.
const deviceLinkCheck = 30 * time.Second

// runDeviceLinks keeps one link to every configured remote host while this
// device accepts calls, until stop is closed.
func runDeviceLinks(stop <-chan struct{}) {
	links := map[string]chan struct{}{} // remote name → its link's stop
	remotes := map[string]Remote{}
	ticker := time.NewTicker(deviceLinkCheck)
	defer ticker.Stop()
	for {
		want := map[string]Remote{}
		if c, err := readConfig(); err == nil && c.DeviceCallable {
			for name, remote := range c.Remotes {
				want[name] = remote
			}
		}
		for name, linkStop := range links {
			if remote, ok := want[name]; !ok || remote != remotes[name] {
				close(linkStop)
				delete(links, name)
			}
		}
		for name, remote := range want {
			if _, ok := links[name]; !ok {
				linkStop := make(chan struct{})
				links[name], remotes[name] = linkStop, remote
				go deviceLinkLoop(name, remote, linkStop)
			}
		}
		select {
		case <-stop:
			for _, linkStop := range links {
				close(linkStop)
			}
			return
		case <-ticker.C:
		}
	}
}

// deviceLinkLoop keeps the link to one host, reconnecting after network
// changes. A host without device links (an older Jara) is retried rarely.
func deviceLinkLoop(name string, remote Remote, stop <-chan struct{}) {
	device := runnerDeviceName()
	backoff := 5 * time.Second
	for {
		var answer struct {
			Socket string `json:"socket"`
		}
		started := time.Now()
		err := rpc(remote, Request{Op: "device-tunnel", Runner: device}, nil, &answer)
		if err == nil {
			err = runDeviceLinkConnection(remote, device, answer.Socket, stop)
		}
		wait := backoff
		if err != nil && strings.Contains(err.Error(), "unknown") {
			wait = 10 * time.Minute // upgrade the host (jara deployment upgrade)
		}
		if time.Since(started) > time.Minute {
			backoff = 5 * time.Second
		} else {
			backoff = min(backoff*2, time.Minute)
		}
		if err != nil {
			fmt.Fprintf(os.Stderr, "Device link to %s: %v; retrying in %s.\n", name, err, wait)
		}
		select {
		case <-stop:
			return
		case <-time.After(wait):
		}
	}
}

// runDeviceLinkConnection runs one link: an SSH connection forwarding the
// daemon's remote socket to socket on the host, with `jara _device-link`
// renewing the device's record on every heartbeat.
func runDeviceLinkConnection(remote Remote, device, socket string, stop <-chan struct{}) error {
	if socket == "" {
		return fmt.Errorf("the host returned no socket")
	}
	nonce := make([]byte, 8)
	_, _ = rand.Read(nonce)
	wire := Request{Runner: device, Link: hex.EncodeToString(nonce)}
	args := append(sshArgs(remote, false), "-o", "BatchMode=yes", "-o", "ExitOnForwardFailure=yes",
		"-R", socket+":"+deviceRemoteSocketPath(), "--", remote.Host, shellArgs("jara", "_device-link", token(wire)))
	cmd := exec.Command("ssh", args...)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	var stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = io.Discard, &stderr
	if err = cmd.Start(); err != nil {
		return err
	}
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()
	ticker := time.NewTicker(runnerHeartbeatInterval())
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			if _, err = stdin.Write([]byte("{}\n")); err != nil {
				_ = cmd.Process.Kill()
				<-exited
				return err
			}
		case <-stop:
			stdin.Close()
			_ = cmd.Process.Kill()
			<-exited
			return nil
		case err = <-exited:
			return fmt.Errorf("%v %s", err, strings.TrimSpace(stderr.String()))
		}
	}
}
