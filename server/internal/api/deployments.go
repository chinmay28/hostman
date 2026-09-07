package api

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/chinmay28/deployer/server/internal/deploy"
	"github.com/chinmay28/deployer/server/internal/store"
)

// deployRequest is the body of a deploy call: which host, and the parameter
// values the user confirmed.
type deployRequest struct {
	HostID int64             `json:"hostId"`
	Params map[string]string `json:"params"`
}

func (s *Server) handleDeployApp(w http.ResponseWriter, r *http.Request) {
	appID, err := pathID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid app id")
		return
	}
	var req deployRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if req.HostID <= 0 {
		writeError(w, http.StatusBadRequest, "hostId is required")
		return
	}
	s.startDeployment(w, r, appID, req.HostID, req.Params)
}

// startDeployment is shared by the deploy and redeploy endpoints.
func (s *Server) startDeployment(w http.ResponseWriter, r *http.Request, appID, hostID int64, params map[string]string) {
	dep, err := s.Runner.Start(r.Context(), appID, hostID, params)
	s.writeStartedDeployment(w, dep, err)
}

// writeStartedDeployment turns whatever the runner said into a response. An
// uninstall goes through the same path as a deploy — same runner, same
// deployment record, same log — so it answers with the same codes.
func (s *Server) writeStartedDeployment(w http.ResponseWriter, dep *store.Deployment, err error) {
	switch {
	case err == nil:
		writeJSON(w, http.StatusAccepted, dep)
	case errors.Is(err, deploy.ErrAlreadyRunning),
		errors.Is(err, deploy.ErrNoUninstallCommand),
		errors.Is(err, deploy.ErrCannotUninstallSelf):
		writeError(w, http.StatusConflict, err.Error())
	case errors.Is(err, store.ErrNotFound):
		writeError(w, http.StatusNotFound, "app or host not found")
	case strings.Contains(err.Error(), "missing required parameter"),
		strings.Contains(err.Error(), "unknown placeholder"):
		writeError(w, http.StatusBadRequest, err.Error())
	default:
		s.Log.Error("api: start deployment", "err", err)
		writeError(w, http.StatusInternalServerError, "could not start the deployment")
	}
}

func (s *Server) handleListDeployments(w http.ResponseWriter, r *http.Request) {
	f := store.DeploymentFilter{}
	if v := r.URL.Query().Get("appId"); v != "" {
		f.AppID, _ = strconv.ParseInt(v, 10, 64)
	}
	if v := r.URL.Query().Get("hostId"); v != "" {
		f.HostID, _ = strconv.ParseInt(v, 10, 64)
	}
	if v := r.URL.Query().Get("limit"); v != "" {
		f.Limit, _ = strconv.Atoi(v)
	}
	list, err := s.DB.ListDeployments(r.Context(), f)
	if err != nil {
		s.writeStoreError(w, err, "list deployments")
		return
	}
	writeJSON(w, http.StatusOK, list)
}

func (s *Server) handleGetDeployment(w http.ResponseWriter, r *http.Request) {
	dep, err := s.deploymentFromPath(r)
	if err != nil {
		s.writeStoreError(w, err, "get deployment")
		return
	}
	// A running deployment's log lives in memory until it finishes.
	if run := s.Runner.Active(dep.ID); run != nil {
		backlog, _, cancel := run.Subscribe()
		cancel()
		dep.Log = string(backlog)
	}
	writeJSON(w, http.StatusOK, dep)
}

func (s *Server) handleCancelDeployment(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid deployment id")
		return
	}
	if err := s.Runner.Cancel(id); err != nil {
		writeError(w, http.StatusConflict, "that deployment is not running")
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]string{"status": "canceling"})
}

// heartbeat keeps proxies and phones from dropping an idle stream.
const heartbeat = 20 * time.Second

// handleDeploymentStream streams a deployment's output as Server-Sent Events.
// It replays everything so far, then follows along live; when the deployment is
// already finished it sends the stored log and closes.
func (s *Server) handleDeploymentStream(w http.ResponseWriter, r *http.Request) {
	dep, err := s.deploymentFromPath(r)
	if err != nil {
		s.writeStoreError(w, err, "stream deployment")
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "streaming unsupported")
		return
	}

	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	h.Set("Connection", "keep-alive")
	// Disable buffering in any reverse proxy sitting in front of HostMan.
	h.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)

	run := s.Runner.Active(dep.ID)
	if run == nil {
		writeSSELog(w, dep.Log)
		s.writeSSEStatus(w, r, dep.ID)
		flusher.Flush()
		return
	}

	backlog, updates, unsubscribe := run.Subscribe()
	defer unsubscribe()

	writeSSELog(w, string(backlog))
	flusher.Flush()

	ticker := time.NewTicker(heartbeat)
	defer ticker.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-ticker.C:
			fmt.Fprint(w, ": keepalive\n\n")
			flusher.Flush()
		case chunk, open := <-updates:
			if !open {
				s.writeSSEStatus(w, r, dep.ID)
				flusher.Flush()
				return
			}
			writeSSELog(w, string(chunk))
			flusher.Flush()
		}
	}
}

// writeSSELog emits output as a log event. Each line becomes its own data
// field, which is how SSE carries multi-line payloads.
func writeSSELog(w http.ResponseWriter, text string) {
	if text == "" {
		return
	}
	fmt.Fprint(w, "event: log\n")
	for _, line := range strings.Split(strings.TrimSuffix(text, "\n"), "\n") {
		fmt.Fprintf(w, "data: %s\n", strings.TrimSuffix(line, "\r"))
	}
	fmt.Fprint(w, "\n")
}

// writeSSEStatus emits the final state of the deployment and ends the stream.
//
// A deployment that is still running when its run ends has been handed over —
// a detached self-update whose HostMan is restarting. Saying "status: running"
// would look terminal to the client, so nothing is sent and the stream simply
// closes; the browser reconnects and picks up whichever process owns it next.
func (s *Server) writeSSEStatus(w http.ResponseWriter, r *http.Request, id int64) {
	dep, err := s.DB.GetDeployment(r.Context(), id)
	if err != nil {
		return
	}
	if dep.Status == store.DeployRunning {
		fmt.Fprint(w, "event: handover\ndata: {}\n\n")
		return
	}
	payload := map[string]any{
		"status":     dep.Status,
		"exitCode":   dep.ExitCode,
		"error":      dep.Error,
		"finishedAt": dep.FinishedAt,
	}
	fmt.Fprint(w, "event: status\n")
	fmt.Fprintf(w, "data: %s\n\n", mustJSON(payload))
}

func (s *Server) deploymentFromPath(r *http.Request) (*store.Deployment, error) {
	id, err := pathID(r)
	if err != nil {
		return nil, store.ErrNotFound
	}
	return s.DB.GetDeployment(r.Context(), id)
}
