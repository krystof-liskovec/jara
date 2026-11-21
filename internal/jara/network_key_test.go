package jara

import (
	"crypto/ed25519"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestJoinKeyIsAValidOpenSSHKey(t *testing.T) {
	if _, err := exec.LookPath("ssh-keygen"); err != nil {
		t.Skip("ssh-keygen is not installed")
	}
	key := joinKey([]byte("0123456789abcdef"))
	path := filepath.Join(t.TempDir(), "join")
	if err := os.WriteFile(path, openSSHPrivateKey(key, "jara-join"), 0600); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command("ssh-keygen", "-y", "-f", path).CombinedOutput()
	if err != nil {
		t.Fatalf("ssh-keygen rejected the key: %v: %s", err, out)
	}
	want := authorizedKey(key.Public().(ed25519.PublicKey))
	if got := strings.Join(strings.Fields(string(out))[:2], " "); got != want {
		t.Fatalf("public key = %q, want %q", got, want)
	}
	if !peerKeyRE.MatchString(want) {
		t.Fatalf("authorized key %q fails validation", want)
	}
}

func TestJoinTokenRoundTrip(t *testing.T) {
	token := joinToken{Name: "work", Host: "work.tailnet", Port: 2222, User: "agent", HostKeys: []string{"ssh-ed25519 AAAA"}, Secret: []byte("0123456789abcdef")}
	decoded, err := decodeJoinToken("  " + token.encode() + "\n")
	if err != nil || decoded.Host != token.Host || decoded.Port != 2222 || string(decoded.Secret) != string(token.Secret) {
		t.Fatalf("decoded = %+v %v", decoded, err)
	}
	for _, bad := range []string{"", "jara1.!!", "jara2.abc", joinToken{Name: "work", Host: "h", HostKeys: []string{"x"}, Secret: token.Secret}.encode()} {
		if _, err := decodeJoinToken(bad); err == nil {
			t.Errorf("accepted %q", bad)
		}
	}
}
