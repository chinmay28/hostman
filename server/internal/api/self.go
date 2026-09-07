package api

import (
	"errors"
	"net/http"
	"strings"

	"github.com/chinmay28/deployer/server/internal/store"
)

// selfView is what Settings needs to show "this machine" and offer an update.
type selfView struct {
	Version string `json:"version"`
	// MachineID is how the home host is recognised; useful when it is not.
	MachineID string `json:"machineId"`
	// Host is the home host, or null if this machine has not been registered.
	Host *store.Host `json:"host"`
	// App is the app that installs HostMan, or null if it was deleted.
	App *store.App `json:"app"`
	// Ref is the version a self-update would build by default.
	Ref string `json:"ref"`
	// Running is the id of a self-update in flight, if there is one.
	Running *int64 `json:"runningDeploymentId"`
	// Ready reports whether an update can be started right now.
	Ready bool `json:"ready"`
	// Blocked explains why not, when Ready is false.
	Blocked string `json:"blocked,omitempty"`
}

func (s *Server) handleGetSelf(w http.ResponseWriter, r *http.Request) {
	view := selfView{Version: s.Version, Ref: s.SelfRef}
	if s.Self != nil {
		view.MachineID = s.Self.MachineID()
	}

	host, err := s.DB.SelfHost(r.Context())
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		s.writeStoreError(w, err, "self")
		return
	}
	view.Host = host

	app, err := s.DB.SelfUpdateApp(r.Context())
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		s.writeStoreError(w, err, "self")
		return
	}
	view.App = app

	if app != nil {
		// The version last deployed is a better default than the build default.
		if host != nil {
			if in, err := s.DB.FindInstallation(r.Context(), app.ID, host.ID); err == nil {
				if ref := in.Params["ref"]; ref != "" {
					view.Ref = ref
				}
			}
		}
		recent, err := s.DB.ListDeployments(r.Context(), store.DeploymentFilter{AppID: app.ID, Limit: 5})
		if err == nil {
			for _, dep := range recent {
				if dep.Status == store.DeployRunning {
					id := dep.ID
					view.Running = &id
					break
				}
			}
		}
	}

	switch {
	case host == nil:
		view.Blocked = "This machine isn't registered as a host yet."
	case app == nil:
		view.Blocked = "The HostMan app was deleted, so there is nothing to update from."
	case view.Running != nil:
		view.Blocked = "An update is already running."
	case host.Status != store.StatusOnline:
		view.Blocked = "HostMan can't reach this machine over SSH yet — authorize its key below."
	case !host.SudoOK:
		view.Blocked = "The SSH user needs passwordless sudo before it can install anything."
	default:
		view.Ready = true
	}
	writeJSON(w, http.StatusOK, view)
}

// handleSelfUpdate starts an update of HostMan itself on the home host.
func (s *Server) handleSelfUpdate(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Ref string `json:"ref"`
	}
	if r.ContentLength > 0 {
		if err := decodeJSON(r, &body); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
	}

	host, err := s.DB.SelfHost(r.Context())
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusConflict,
				"This machine isn't registered as a host yet, so HostMan can't update itself.")
			return
		}
		s.writeStoreError(w, err, "self update")
		return
	}
	app, err := s.DB.SelfUpdateApp(r.Context())
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusConflict,
				"The HostMan app was deleted. Recreate it to update from the UI.")
			return
		}
		s.writeStoreError(w, err, "self update")
		return
	}

	params := map[string]string{}
	if ref := strings.TrimSpace(body.Ref); ref != "" {
		params["ref"] = ref
	}
	s.startDeployment(w, r, app.ID, host.ID, params)
}
