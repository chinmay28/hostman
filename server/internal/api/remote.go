package api

import (
	"context"
	"net/http"
	"time"

	"github.com/chinmay28/deployer/server/internal/hostops"
)

// A remote session is a browser running on the host, driven from the phone. The
// endpoints are deliberately few: what state is it in, set it up, start or stop
// it, remove it. Everything else a session needs — its journal, its unit file —
// is a service like any other and is already served by the service endpoints.

// remoteSetupTimeout bounds writing a session onto a host. It is an ordinary
// operation, not a long one: the packages are installed by a detached job the
// host gets on with by itself, exactly as a self-update is, so what this waits
// for is a handful of files being written.
const remoteSetupTimeout = 60 * time.Second

// remoteView is a session plus the one thing only the server can work out: the
// address to open it at. The session's own fields are inlined, so the screen
// sees one object.
type remoteView struct {
	*hostops.RemoteSession
	// URL is where noVNC answers on this host, ready to connect.
	URL string `json:"url"`
}

// handleRemoteSession reports on a host's remote session: what is installed,
// how far a setup got, whether it is running, and what has been downloaded.
//
// It is safe to ask for repeatedly — nothing here writes — which matters
// because the screen watching an install is the one place in HostMan that
// polls a host on a timer rather than on a tap.
func (s *Server) handleRemoteSession(w http.ResponseWriter, r *http.Request) {
	h, err := s.hostFromPath(r)
	if err != nil {
		s.writeStoreError(w, err, "read remote session")
		return
	}
	ctx, cancel := opContext(r)
	defer cancel()

	session, err := s.Ops.RemoteStatus(ctx, h)
	if err != nil {
		s.writeOpError(w, err, "read remote session")
		return
	}
	writeJSON(w, http.StatusOK, remoteView{session, hostops.RemoteURL(h.Address, session)})
}

// handleRemoteSetup installs a remote session on a host, or reconfigures the
// one already there. It is idempotent, and it keeps the password and the
// browser profile: setting up again to change a screen size should not sign
// anybody out of anything.
func (s *Server) handleRemoteSetup(w http.ResponseWriter, r *http.Request) {
	h, err := s.hostFromPath(r)
	if err != nil {
		s.writeStoreError(w, err, "set up remote session")
		return
	}
	var in hostops.RemoteSetup
	if r.ContentLength > 0 {
		if err := decodeJSON(r, &in); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
	}
	ctx, cancel := context.WithTimeout(r.Context(), remoteSetupTimeout)
	defer cancel()

	session, err := s.Ops.SetupRemote(ctx, h, in)
	if err != nil {
		s.writeOpError(w, err, "set up remote session")
		return
	}
	s.Log.Info("api: setting up a remote session", "host", h.Name, "geometry", session.Geometry)
	writeJSON(w, http.StatusOK, remoteView{session, hostops.RemoteURL(h.Address, session)})
}

type remoteActionInput struct {
	// Action is "start" or "stop".
	Action string `json:"action"`
	// URL is the page to open, on a start. An empty one keeps the last page.
	URL string `json:"url"`
}

// handleRemoteAction starts or stops the session. Starting takes the page to
// open with it, so opening a site is one round trip rather than a URL typed
// into a browser over VNC with a phone keyboard.
func (s *Server) handleRemoteAction(w http.ResponseWriter, r *http.Request) {
	h, err := s.hostFromPath(r)
	if err != nil {
		s.writeStoreError(w, err, "remote session")
		return
	}
	var in remoteActionInput
	if err := decodeJSON(r, &in); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	// Starting waits for systemd, so it gets systemd's own patience.
	ctx, cancel := context.WithTimeout(r.Context(), unitTimeout)
	defer cancel()

	var session *hostops.RemoteSession
	switch in.Action {
	case "start":
		session, err = s.Ops.StartRemote(ctx, h, in.URL)
	case "stop":
		session, err = s.Ops.StopRemote(ctx, h)
	default:
		writeError(w, http.StatusBadRequest, "a remote session can be started or stopped")
		return
	}
	if err != nil {
		s.writeOpError(w, err, "remote session "+in.Action)
		return
	}
	s.Log.Info("api: remote session", "host", h.Name, "action", in.Action)
	writeJSON(w, http.StatusOK, remoteView{session, hostops.RemoteURL(h.Address, session)})
}

// handleRemoteRemove takes the session off the host. `?purge=true` takes the
// browser profile with it, and with the profile every site it was signed into —
// which is why it is asked for rather than assumed.
func (s *Server) handleRemoteRemove(w http.ResponseWriter, r *http.Request) {
	h, err := s.hostFromPath(r)
	if err != nil {
		s.writeStoreError(w, err, "remove remote session")
		return
	}
	purge := r.URL.Query().Get("purge") == "true"
	ctx, cancel := context.WithTimeout(r.Context(), unitTimeout)
	defer cancel()

	if err := s.Ops.RemoveRemote(ctx, h, purge); err != nil {
		s.writeOpError(w, err, "remove remote session")
		return
	}
	s.Log.Info("api: removed the remote session", "host", h.Name, "purged", purge)
	w.WriteHeader(http.StatusNoContent)
}
