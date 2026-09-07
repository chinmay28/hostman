package api

import (
	"context"
	"net/http"
	"time"

	"github.com/chinmay28/deployer/server/internal/deploy"
	"github.com/chinmay28/deployer/server/internal/store"
)

// withDerived fills in the ports each installation answers on, the address to
// open it at and the version it is running. The database has no column for any
// of them — they are read back out of the health check and the parameters — so
// every response that carries installations goes through here.
func withDerived(list []*store.Installation) []*store.Installation {
	for _, in := range list {
		derive(in)
	}
	return list
}

// derive fills in the fields of one installation that are worked out rather
// than stored.
func derive(in *store.Installation) {
	in.Ports = deploy.InstallationPorts(in)
	in.URL = deploy.InstallationURL(in)
	in.Version = deploy.InstallationVersion(in)
}

func (s *Server) handleListInstallations(w http.ResponseWriter, r *http.Request) {
	list, err := s.DB.ListInstallations(r.Context())
	if err != nil {
		s.writeStoreError(w, err, "list installations")
		return
	}
	writeJSON(w, http.StatusOK, withDerived(list))
}

func (s *Server) handleGetInstallation(w http.ResponseWriter, r *http.Request) {
	in, err := s.installationFromPath(r)
	if err != nil {
		s.writeStoreError(w, err, "get installation")
		return
	}
	derive(in)
	writeJSON(w, http.StatusOK, in)
}

// handleUninstall takes an app off a host for real: it runs the app's uninstall
// command there, streams the log the way a deploy does, and forgets the
// installation once the command has succeeded. Forgetting without running
// anything is the other endpoint, below.
func (s *Server) handleUninstall(w http.ResponseWriter, r *http.Request) {
	in, err := s.installationFromPath(r)
	if err != nil {
		s.writeStoreError(w, err, "uninstall")
		return
	}
	dep, err := s.Runner.Uninstall(r.Context(), in.AppID, in.HostID)
	s.writeStartedDeployment(w, dep, err)
}

// handleForgetInstallation removes HostMan's record of an app on a host and
// nothing else. Whatever the app left on the machine stays there — which is
// what you want for an app removed by hand, and what Uninstall is for
// otherwise.
func (s *Server) handleForgetInstallation(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid installation id")
		return
	}
	if err := s.DB.DeleteInstallation(r.Context(), id); err != nil {
		s.writeStoreError(w, err, "forget installation")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleCheckInstallation(w http.ResponseWriter, r *http.Request) {
	in, err := s.installationFromPath(r)
	if err != nil {
		s.writeStoreError(w, err, "check installation")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	status, detail := s.Health.CheckOne(ctx, in)
	writeJSON(w, http.StatusOK, map[string]string{"healthStatus": status, "healthDetail": detail})
}

// handleRedeploy runs an installed app again on the same host, reusing the
// parameters from last time unless the request overrides them. This is the
// one-tap path in the UI.
func (s *Server) handleRedeploy(w http.ResponseWriter, r *http.Request) {
	in, err := s.installationFromPath(r)
	if err != nil {
		s.writeStoreError(w, err, "redeploy")
		return
	}
	body := deployRequest{Params: map[string]string{}}
	if r.ContentLength > 0 {
		if err := decodeJSON(r, &body); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
	}
	params := map[string]string{}
	for k, v := range in.Params {
		params[k] = v
	}
	for k, v := range body.Params {
		params[k] = v
	}
	s.startDeployment(w, r, in.AppID, in.HostID, params)
}

// overview is everything the dashboard needs in one request, which matters on
// a phone: the hosts, and what is deployed to each of them.
type overview struct {
	Hosts         []hostView            `json:"hosts"`
	Installations []*store.Installation `json:"installations"`
}

func (s *Server) handleOverview(w http.ResponseWriter, r *http.Request) {
	hostList, err := s.DB.ListHosts(r.Context())
	if err != nil {
		s.writeStoreError(w, err, "overview")
		return
	}
	latest, err := s.DB.LatestSamples(r.Context())
	if err != nil {
		s.writeStoreError(w, err, "overview")
		return
	}
	installs, err := s.DB.ListInstallations(r.Context())
	if err != nil {
		s.writeStoreError(w, err, "overview")
		return
	}
	views := make([]hostView, 0, len(hostList))
	for _, h := range hostList {
		views = append(views, hostView{Host: h, Latest: latest[h.ID]})
	}
	writeJSON(w, http.StatusOK, overview{Hosts: views, Installations: withDerived(installs)})
}

func (s *Server) installationFromPath(r *http.Request) (*store.Installation, error) {
	id, err := pathID(r)
	if err != nil {
		return nil, store.ErrNotFound
	}
	return s.DB.GetInstallation(r.Context(), id)
}
