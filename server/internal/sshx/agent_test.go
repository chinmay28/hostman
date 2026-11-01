package sshx

import (
	"context"
	"errors"
	"net"
	"path/filepath"
	"testing"

	"golang.org/x/crypto/ssh/agent"

	"github.com/chinmay28/hostman/server/internal/store"
)

func testIdentity(t *testing.T) *Identity {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "hostman.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	id, err := EnsureIdentity(context.Background(), db)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

// serveAgent puts the identity's agent on one end of a pipe and returns a
// client on the other, the way a forwarded agent is reached from a host.
func serveAgent(t *testing.T, id *Identity) agent.ExtendedAgent {
	t.Helper()
	server, client := net.Pipe()
	go agent.ServeAgent(id.Agent(), server)
	t.Cleanup(func() { client.Close(); server.Close() })
	return agent.NewClient(client)
}

// A host that is lent the agent sees exactly one key, HostMan's, and a
// signature it asks for is one the public key accepts.
func TestAgentServesTheOneKey(t *testing.T) {
	id := testIdentity(t)
	remote := serveAgent(t, id)

	keys, err := remote.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(keys) != 1 {
		t.Fatalf("the agent lists %d keys, want 1", len(keys))
	}
	if keys[0].String() != id.AuthorizedKey() {
		t.Errorf("listed key = %q, want HostMan's own %q", keys[0].String(), id.AuthorizedKey())
	}

	data := []byte("the host wants this signed")
	sig, err := remote.Sign(id.Signer.PublicKey(), data)
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}
	if err := id.Signer.PublicKey().Verify(data, sig); err != nil {
		t.Errorf("the signature does not verify against HostMan's key: %v", err)
	}
	// The same through the flagged form, which is what a newer ssh sends.
	if _, err := remote.SignWithFlags(id.Signer.PublicKey(), data, agent.SignatureFlagRsaSha256); err != nil {
		t.Errorf("SignWithFlags on an ed25519 key: %v, want the flags ignored", err)
	}
}

// Only HostMan's key is signed with. A host asking for a signature with some
// other key is told no, rather than handed one with the wrong key.
func TestAgentRefusesOtherKeys(t *testing.T) {
	id := testIdentity(t)
	other := testIdentity(t)
	remote := serveAgent(t, id)

	if _, err := remote.Sign(other.Signer.PublicKey(), []byte("x")); err == nil {
		t.Error("signing with a key the agent does not hold should fail")
	}
}

// The agent is read-only: a host it is forwarded to cannot add a key of its
// own, drop HostMan's, or lock it, whatever it asks.
func TestAgentIsReadOnly(t *testing.T) {
	id := testIdentity(t)
	a := id.Agent()

	for name, call := range map[string]func() error{
		"Add":       func() error { return a.Add(agent.AddedKey{}) },
		"Remove":    func() error { return a.Remove(id.Signer.PublicKey()) },
		"RemoveAll": a.RemoveAll,
		"Lock":      func() error { return a.Lock(nil) },
		"Unlock":    func() error { return a.Unlock(nil) },
	} {
		if err := call(); !errors.Is(err, ErrAgentReadOnly) {
			t.Errorf("%s = %v, want ErrAgentReadOnly", name, err)
		}
	}
	// Over the wire the refusal arrives as a failure rather than a crash.
	remote := serveAgent(t, id)
	if err := remote.RemoveAll(); err == nil {
		t.Error("RemoveAll over the wire should fail")
	}
	if keys, _ := remote.List(); len(keys) != 1 {
		t.Errorf("after a refused RemoveAll the agent lists %d keys, want the one it had", len(keys))
	}
	if _, err := remote.Extension("query", nil); err == nil {
		t.Error("extensions should be unsupported")
	}
}
