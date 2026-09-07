package api

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/chinmay28/deployer/server/internal/store"
)

// summaryWindow is how far back the CPU and memory ranges reach. It matches
// how long the poller keeps samples, so the summary covers everything there is.
const summaryWindow = 24 * time.Hour

// hostView is a host plus its most recent telemetry.
type hostView struct {
	*store.Host
	Latest *store.Sample `json:"latest"`
}

func (s *Server) handleListHosts(w http.ResponseWriter, r *http.Request) {
	list, err := s.DB.ListHosts(r.Context())
	if err != nil {
		s.writeStoreError(w, err, "list hosts")
		return
	}
	latest, err := s.DB.LatestSamples(r.Context())
	if err != nil {
		s.writeStoreError(w, err, "list hosts")
		return
	}
	views := make([]hostView, 0, len(list))
	for _, h := range list {
		views = append(views, hostView{Host: h, Latest: latest[h.ID]})
	}
	writeJSON(w, http.StatusOK, views)
}

func (s *Server) handleGetHost(w http.ResponseWriter, r *http.Request) {
	h, err := s.hostFromPath(r)
	if err != nil {
		s.writeStoreError(w, err, "get host")
		return
	}
	latest, err := s.DB.LatestSamples(r.Context())
	if err != nil {
		s.writeStoreError(w, err, "get host")
		return
	}
	writeJSON(w, http.StatusOK, hostView{Host: h, Latest: latest[h.ID]})
}

// hostInput is the editable shape of a host.
type hostInput struct {
	Name     string `json:"name"`
	Address  string `json:"address"`
	Port     int    `json:"port"`
	Username string `json:"username"`
}

// normalize trims input and applies defaults, returning a validation message.
func (in *hostInput) normalize() string {
	in.Name = strings.TrimSpace(in.Name)
	in.Address = strings.TrimSpace(in.Address)
	in.Username = strings.TrimSpace(in.Username)
	if in.Port == 0 {
		in.Port = 22
	}
	switch {
	case in.Name == "":
		return "name is required"
	case in.Address == "":
		return "address is required (hostname like nakedpi.local, or an IP)"
	case in.Username == "":
		return "username is required"
	case in.Port < 1 || in.Port > 65535:
		return "port must be between 1 and 65535"
	}
	return ""
}

func (s *Server) handleCreateHost(w http.ResponseWriter, r *http.Request) {
	var in hostInput
	if err := decodeJSON(r, &in); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if msg := in.normalize(); msg != "" {
		writeError(w, http.StatusBadRequest, msg)
		return
	}
	h, err := s.DB.CreateHost(r.Context(), &store.Host{
		Name: in.Name, Address: in.Address, Port: in.Port, Username: in.Username,
	})
	if err != nil {
		s.writeStoreError(w, err, "create host")
		return
	}
	// Check reachability right away so the UI can show the result immediately.
	go s.probeInBackground(h)
	writeJSON(w, http.StatusCreated, hostView{Host: h})
}

func (s *Server) handleUpdateHost(w http.ResponseWriter, r *http.Request) {
	h, err := s.hostFromPath(r)
	if err != nil {
		s.writeStoreError(w, err, "update host")
		return
	}
	in := hostInput{Name: h.Name, Address: h.Address, Port: h.Port, Username: h.Username}
	if err := decodeJSON(r, &in); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if msg := in.normalize(); msg != "" {
		writeError(w, http.StatusBadRequest, msg)
		return
	}
	h.Name, h.Address, h.Port, h.Username = in.Name, in.Address, in.Port, in.Username
	if err := s.DB.UpdateHost(r.Context(), h); err != nil {
		s.writeStoreError(w, err, "update host")
		return
	}
	writeJSON(w, http.StatusOK, hostView{Host: h})
}

func (s *Server) handleDeleteHost(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid host id")
		return
	}
	if err := s.DB.DeleteHost(r.Context(), id); err != nil {
		s.writeStoreError(w, err, "delete host")
		return
	}
	// SQLite reuses row ids, so what is held in memory about this host goes
	// with it rather than waiting to be inherited by the next one. A shell is
	// a live SSH connection as well as a memory: forgetting the host without
	// ending it would leave a terminal typing at a machine HostMan no longer
	// admits to knowing.
	s.Hosts.Forget(id)
	s.Shells.CloseHost(id)
	s.Claude.CloseHost(id)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleTestHost(w http.ResponseWriter, r *http.Request) {
	h, err := s.hostFromPath(r)
	if err != nil {
		s.writeStoreError(w, err, "test host")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	writeJSON(w, http.StatusOK, s.Hosts.Test(ctx, h))
}

// provisionInput carries the one-time password for setting a host up. It is
// used for the duration of the request and then dropped: nothing here is
// written to the database or the log.
type provisionInput struct {
	Password string `json:"password"`
}

// handleProvisionHost does by SSH what the two commands in Settings do by hand:
// authorize HostMan's key and grant passwordless sudo. Like a test, a failure
// to set the host up is a 200 with the reasons in the body — the request itself
// worked, the host is just not ready.
func (s *Server) handleProvisionHost(w http.ResponseWriter, r *http.Request) {
	h, err := s.hostFromPath(r)
	if err != nil {
		s.writeStoreError(w, err, "provision host")
		return
	}
	var in provisionInput
	if err := decodeJSON(r, &in); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if in.Password == "" {
		writeError(w, http.StatusBadRequest, "a password is required to set the host up")
		return
	}
	// Long enough for a slow handshake, the two setup commands, and the probe
	// that verifies them — the probe alone sleeps a second to average CPU.
	ctx, cancel := context.WithTimeout(r.Context(), 90*time.Second)
	defer cancel()

	result := s.Hosts.Provision(ctx, h, in.Password)
	in.Password = ""
	s.Log.Info("api: provisioned host", "host", h.Name, "ok", result.OK, "sudo", result.SudoOK)
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) handleHostMetrics(w http.ResponseWriter, r *http.Request) {
	h, err := s.hostFromPath(r)
	if err != nil {
		s.writeStoreError(w, err, "host metrics")
		return
	}
	// Someone is looking at this host: poll it more often for a while.
	s.Poller.Watch(h.ID)

	minutes := 60
	if v := r.URL.Query().Get("minutes"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 24*60 {
			minutes = n
		}
	}
	samples, err := s.DB.SamplesSince(r.Context(), h.ID, time.Now().Add(-time.Duration(minutes)*time.Minute))
	if err != nil {
		s.writeStoreError(w, err, "host metrics")
		return
	}
	// The summary covers the whole day whatever window the samples cover: it is
	// the answer to "is this normal for the machine?", which an hour of history
	// cannot give.
	summary, err := s.DB.SummarySince(r.Context(), h.ID, time.Now().Add(-summaryWindow))
	if err != nil {
		s.writeStoreError(w, err, "host metrics")
		return
	}
	// The process snapshot rides along with the metrics the screen is already
	// polling for: it comes from the same probe, so asking for it separately
	// would be a second SSH session for something already in hand. It is null
	// until the first probe since HostMan started, which is seconds away.
	writeJSON(w, http.StatusOK, map[string]any{
		"hostId":    h.ID,
		"minutes":   minutes,
		"samples":   samples,
		"summary":   summary,
		"processes": s.Hosts.Processes(h.ID),
	})
}

func (s *Server) hostFromPath(r *http.Request) (*store.Host, error) {
	id, err := pathID(r)
	if err != nil {
		return nil, store.ErrNotFound
	}
	return s.DB.GetHost(r.Context(), id)
}

// probeInBackground samples a host outside the request lifetime.
func (s *Server) probeInBackground(h *store.Host) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := s.Hosts.Probe(ctx, h); err != nil {
		s.Log.Debug("api: initial probe failed", "host", h.Name, "err", err)
	}
}
