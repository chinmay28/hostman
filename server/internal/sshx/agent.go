package sshx

import (
	"bytes"
	"crypto/rand"
	"errors"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
)

// ErrAgentReadOnly is the answer to anything that would change what the agent
// holds. It serves HostMan's one key, and only this process decides that.
var ErrAgentReadOnly = errors.New("hostman's agent holds one key and does not take instructions")

// identityAgent serves HostMan's key over the agent protocol. It is what a
// host sees when HostMan forwards its agent: a key it can ask for signatures
// with, never one it can read. The private half stays in this process, and
// the host's access to it ends with the session that forwarded it.
type identityAgent struct {
	signer ssh.Signer
}

// Agent presents the identity as an SSH agent, for forwarding to a host.
func (id *Identity) Agent() agent.ExtendedAgent { return identityAgent{signer: id.Signer} }

func (a identityAgent) List() ([]*agent.Key, error) {
	pub := a.signer.PublicKey()
	return []*agent.Key{{Format: pub.Type(), Blob: pub.Marshal(), Comment: KeyComment}}, nil
}

func (a identityAgent) Sign(key ssh.PublicKey, data []byte) (*ssh.Signature, error) {
	return a.SignWithFlags(key, data, 0)
}

// SignWithFlags signs with the one key, after checking it is the key being
// asked for. The flags choose an RSA hash and mean nothing to the ed25519
// key HostMan generates; they are honoured where the signer can, and ignored
// where it cannot, rather than refused.
func (a identityAgent) SignWithFlags(key ssh.PublicKey, data []byte, flags agent.SignatureFlags) (*ssh.Signature, error) {
	if !bytes.Equal(key.Marshal(), a.signer.PublicKey().Marshal()) {
		return nil, errors.New("the agent does not hold that key")
	}
	if flags != 0 {
		if as, ok := a.signer.(ssh.AlgorithmSigner); ok {
			algo := ""
			switch {
			case flags&agent.SignatureFlagRsaSha512 != 0:
				algo = ssh.KeyAlgoRSASHA512
			case flags&agent.SignatureFlagRsaSha256 != 0:
				algo = ssh.KeyAlgoRSASHA256
			}
			if algo != "" && key.Type() == ssh.KeyAlgoRSA {
				return as.SignWithAlgorithm(rand.Reader, data, algo)
			}
		}
	}
	return a.signer.Sign(rand.Reader, data)
}

func (a identityAgent) Signers() ([]ssh.Signer, error) { return []ssh.Signer{a.signer}, nil }

func (identityAgent) Add(agent.AddedKey) error   { return ErrAgentReadOnly }
func (identityAgent) Remove(ssh.PublicKey) error { return ErrAgentReadOnly }
func (identityAgent) RemoveAll() error           { return ErrAgentReadOnly }
func (identityAgent) Lock([]byte) error          { return ErrAgentReadOnly }
func (identityAgent) Unlock([]byte) error        { return ErrAgentReadOnly }
func (identityAgent) Extension(string, []byte) ([]byte, error) {
	return nil, agent.ErrExtensionUnsupported
}
