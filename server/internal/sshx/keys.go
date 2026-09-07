// Package sshx handles HostMan's SSH identity and connections to hosts.
package sshx

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"fmt"
	"strings"

	"golang.org/x/crypto/ssh"
)

// SettingPrivateKey is the settings key holding HostMan's OpenSSH private key.
const SettingPrivateKey = "ssh_private_key"

// KeyComment identifies HostMan's key in a host's authorized_keys file.
const KeyComment = "deployer"

// keyStore is the slice of the database that sshx needs.
type keyStore interface {
	GetSetting(ctx context.Context, key string) (string, bool, error)
	SetSetting(ctx context.Context, key, value string) error
}

// Identity is HostMan's SSH keypair.
type Identity struct {
	Signer ssh.Signer
}

// EnsureIdentity loads HostMan's keypair, generating one on first run.
func EnsureIdentity(ctx context.Context, db keyStore) (*Identity, error) {
	pemBytes, ok, err := db.GetSetting(ctx, SettingPrivateKey)
	if err != nil {
		return nil, err
	}
	if !ok {
		pemBytes, err = generateKey()
		if err != nil {
			return nil, err
		}
		if err := db.SetSetting(ctx, SettingPrivateKey, pemBytes); err != nil {
			return nil, fmt.Errorf("persist ssh key: %w", err)
		}
	}
	signer, err := ssh.ParsePrivateKey([]byte(pemBytes))
	if err != nil {
		return nil, fmt.Errorf("parse ssh key: %w", err)
	}
	return &Identity{Signer: signer}, nil
}

// RotateIdentity generates and stores a fresh keypair, replacing the old one.
// Every host must have the new public key added to authorized_keys afterwards.
func RotateIdentity(ctx context.Context, db keyStore) (*Identity, error) {
	pemBytes, err := generateKey()
	if err != nil {
		return nil, err
	}
	if err := db.SetSetting(ctx, SettingPrivateKey, pemBytes); err != nil {
		return nil, fmt.Errorf("persist ssh key: %w", err)
	}
	signer, err := ssh.ParsePrivateKey([]byte(pemBytes))
	if err != nil {
		return nil, err
	}
	return &Identity{Signer: signer}, nil
}

func generateKey() (string, error) {
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return "", fmt.Errorf("generate ed25519 key: %w", err)
	}
	block, err := ssh.MarshalPrivateKey(priv, KeyComment)
	if err != nil {
		return "", fmt.Errorf("marshal private key: %w", err)
	}
	return string(pem.EncodeToMemory(block)), nil
}

// AuthorizedKey renders the public key as an authorized_keys line.
func (id *Identity) AuthorizedKey() string {
	line := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(id.Signer.PublicKey())))
	return line + " " + KeyComment
}

// Fingerprint returns the SHA256 fingerprint of the public key.
func (id *Identity) Fingerprint() string {
	return ssh.FingerprintSHA256(id.Signer.PublicKey())
}
