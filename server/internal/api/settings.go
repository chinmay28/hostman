package api

import (
	"fmt"
	"net/http"

	"github.com/chinmay28/deployer/server/internal/sshx"
)

// sshKeyView tells the user how to trust HostMan on a new host.
type sshKeyView struct {
	PublicKey   string `json:"publicKey"`
	Fingerprint string `json:"fingerprint"`
	// AuthorizeCommand adds the key to the current user's authorized_keys.
	AuthorizeCommand string `json:"authorizeCommand"`
	// SudoCommand grants the current user passwordless sudo.
	SudoCommand string `json:"sudoCommand"`
}

func (s *Server) sshKeyView() sshKeyView {
	id := s.Hosts.Identity()
	pub := id.AuthorizedKey()
	return sshKeyView{
		PublicKey:   pub,
		Fingerprint: id.Fingerprint(),
		AuthorizeCommand: fmt.Sprintf(
			`mkdir -p ~/.ssh && chmod 700 ~/.ssh && echo '%s' >> ~/.ssh/authorized_keys && chmod 600 ~/.ssh/authorized_keys`,
			pub),
		SudoCommand: `echo "$(whoami) ALL=(ALL) NOPASSWD:ALL" | sudo tee /etc/sudoers.d/deployer >/dev/null && sudo chmod 440 /etc/sudoers.d/deployer`,
	}
}

func (s *Server) handleGetSSHKey(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.sshKeyView())
}

// handleRotateSSHKey replaces HostMan's keypair. Every host must have the new
// public key installed afterwards, so the UI warns before calling this.
func (s *Server) handleRotateSSHKey(w http.ResponseWriter, r *http.Request) {
	id, err := sshx.RotateIdentity(r.Context(), s.DB)
	if err != nil {
		s.Log.Error("api: rotate ssh key", "err", err)
		writeError(w, http.StatusInternalServerError, "could not rotate key")
		return
	}
	s.Hosts.SetIdentity(id)
	s.Log.Warn("ssh identity rotated; hosts need the new public key installed")
	writeJSON(w, http.StatusOK, s.sshKeyView())
}
