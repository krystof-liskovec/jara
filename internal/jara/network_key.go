package jara

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"strings"
)

// A join code carries a secret from which both sides derive the same SSH
// key pair: the inviting device authorizes its public half on a host for a
// few minutes, restricted to `jara _join`, and the joining device logs in
// with the private half. The code also pins the host's SSH keys, so the
// joining device trusts the network it receives.

const joinTokenPrefix = "jara1."

// joinToken is what `jara network invite` prints and `jara join` takes.
type joinToken struct {
	Name     string   `json:"n"` // the host's name in the network
	Host     string   `json:"h"`
	Port     int      `json:"p,omitempty"`
	User     string   `json:"u,omitempty"`
	HostKeys []string `json:"k"`
	Secret   []byte   `json:"s"`
}

func (t joinToken) encode() string {
	data, _ := json.Marshal(t)
	return joinTokenPrefix + base64.RawURLEncoding.EncodeToString(data)
}

func decodeJoinToken(text string) (joinToken, error) {
	var t joinToken
	raw, ok := strings.CutPrefix(strings.TrimSpace(text), joinTokenPrefix)
	if !ok {
		return t, fmt.Errorf("this is not a Jara join code (it starts with %s)", joinTokenPrefix)
	}
	data, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil || json.Unmarshal(data, &t) != nil {
		return t, fmt.Errorf("the join code is damaged; copy it again")
	}
	if len(t.Secret) < 16 || !nameRE.MatchString(t.Name) || t.Host == "" || len(t.HostKeys) == 0 {
		return t, fmt.Errorf("the join code is incomplete")
	}
	for _, key := range t.HostKeys {
		if !peerKeyRE.MatchString(key) {
			return t, fmt.Errorf("the join code has an invalid host key")
		}
	}
	return t, nil
}

// joinKey derives the invitation's key pair from its secret.
func joinKey(secret []byte) ed25519.PrivateKey {
	seed := sha256.Sum256(append([]byte("jara-join-v1\x00"), secret...))
	return ed25519.NewKeyFromSeed(seed[:])
}

func sshString(b *bytes.Buffer, data []byte) {
	_ = binary.Write(b, binary.BigEndian, uint32(len(data)))
	b.Write(data)
}

func ed25519PublicBlob(public ed25519.PublicKey) []byte {
	var blob bytes.Buffer
	sshString(&blob, []byte("ssh-ed25519"))
	sshString(&blob, public)
	return blob.Bytes()
}

// authorizedKey renders a public key as an authorized_keys entry.
func authorizedKey(public ed25519.PublicKey) string {
	return "ssh-ed25519 " + base64.StdEncoding.EncodeToString(ed25519PublicBlob(public))
}

// inviteID names an invitation by its public key.
func inviteID(key string) string {
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:8])
}

// openSSHPrivateKey encodes an unencrypted key in OpenSSH's format.
func openSSHPrivateKey(key ed25519.PrivateKey, comment string) []byte {
	public := key.Public().(ed25519.PublicKey)
	var private bytes.Buffer
	_ = binary.Write(&private, binary.BigEndian, uint32(0x6a617261)) // check bytes, twice
	_ = binary.Write(&private, binary.BigEndian, uint32(0x6a617261))
	sshString(&private, []byte("ssh-ed25519"))
	sshString(&private, public)
	sshString(&private, key)
	sshString(&private, []byte(comment))
	for pad := byte(1); private.Len()%8 != 0; pad++ {
		private.WriteByte(pad)
	}
	var out bytes.Buffer
	out.WriteString("openssh-key-v1\x00")
	sshString(&out, []byte("none"))
	sshString(&out, []byte("none"))
	sshString(&out, nil)
	_ = binary.Write(&out, binary.BigEndian, uint32(1))
	sshString(&out, ed25519PublicBlob(public))
	sshString(&out, private.Bytes())
	return pem.EncodeToMemory(&pem.Block{Type: "OPENSSH PRIVATE KEY", Bytes: out.Bytes()})
}
