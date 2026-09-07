// Package api exposes HostMan's REST API over HTTP.
package api

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"github.com/chinmay28/deployer/server/internal/claude"
	"github.com/chinmay28/deployer/server/internal/deploy"
	"github.com/chinmay28/deployer/server/internal/hostops"
	"github.com/chinmay28/deployer/server/internal/hosts"
	"github.com/chinmay28/deployer/server/internal/shell"
	"github.com/chinmay28/deployer/server/internal/store"
)

// SelfManager is the slice of selfhost the API needs.
type SelfManager interface {
	MachineID() string
}

// Server holds the dependencies the handlers need.
type Server struct {
	DB     *store.DB
	Hosts  *hosts.Service
	Poller *hosts.Poller
	Runner *deploy.Runner
	Health *deploy.Checker
	// Ops runs administrative work on a host: files, crontabs, restarts.
	Ops *hostops.Service
	// Shells holds the login shells open on hosts. They outlive the requests
	// that made them, which is why they live on the server and not in a handler.
	Shells *shell.Manager
	// Claude holds the Claude Code sessions open on hosts, which outlive
	// their screens the way shells do.
	Claude *claude.Manager
	Log    *slog.Logger
	// Self identifies the machine HostMan runs on; nil disables self-update.
	Self SelfManager
	// Version is the build this binary was made from.
	Version string
	// SelfRef is the git ref a self-update builds from by default.
	SelfRef string
	// Auth is nil when HostMan runs without a PIN.
	Auth *PinAuth
}

// Routes returns the API mux, to be mounted under /api/.
func (s *Server) Routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/health", s.handleHealth)

	mux.HandleFunc("GET /api/session", s.handleSessionStatus)
	mux.HandleFunc("POST /api/session", s.handleLogin)

	mux.HandleFunc("GET /api/settings/ssh", s.handleGetSSHKey)
	mux.HandleFunc("POST /api/settings/ssh/rotate", s.handleRotateSSHKey)

	mux.HandleFunc("GET /api/hosts", s.handleListHosts)
	mux.HandleFunc("POST /api/hosts", s.handleCreateHost)
	mux.HandleFunc("GET /api/hosts/{id}", s.handleGetHost)
	mux.HandleFunc("PATCH /api/hosts/{id}", s.handleUpdateHost)
	mux.HandleFunc("DELETE /api/hosts/{id}", s.handleDeleteHost)
	mux.HandleFunc("POST /api/hosts/{id}/test", s.handleTestHost)
	mux.HandleFunc("POST /api/hosts/{id}/provision", s.handleProvisionHost)
	mux.HandleFunc("GET /api/hosts/{id}/metrics", s.handleHostMetrics)
	mux.HandleFunc("POST /api/hosts/{id}/reboot", s.handleHostReboot)
	mux.HandleFunc("GET /api/hosts/{id}/boot", s.handleHostBoot)
	mux.HandleFunc("POST /api/hosts/{id}/boot/journal", s.handleKeepJournal)

	mux.HandleFunc("GET /api/hosts/{id}/cron", s.handleGetCrontab)
	mux.HandleFunc("PUT /api/hosts/{id}/cron", s.handlePutCrontab)

	mux.HandleFunc("GET /api/hosts/{id}/services", s.handleListServices)
	mux.HandleFunc("POST /api/hosts/{id}/services", s.handleCreateService)
	mux.HandleFunc("DELETE /api/hosts/{id}/services", s.handleDeleteService)
	mux.HandleFunc("GET /api/hosts/{id}/services/unit", s.handleGetService)
	mux.HandleFunc("GET /api/hosts/{id}/services/logs", s.handleServiceLogs)
	mux.HandleFunc("POST /api/hosts/{id}/services/action", s.handleServiceAction)
	mux.HandleFunc("POST /api/hosts/{id}/services/reload", s.handleReloadServices)

	// A shell is opened and listed under its host; watching, typing at and
	// closing one is addressed by the session's own id, because the session is
	// the thing that persists and the screen may be a different phone.
	mux.HandleFunc("GET /api/hosts/{id}/shell", s.handleListShells)
	mux.HandleFunc("POST /api/hosts/{id}/shell", s.handleOpenShell)
	mux.HandleFunc("GET /api/shell/{sid}", s.handleGetShell)
	mux.HandleFunc("DELETE /api/shell/{sid}", s.handleCloseShell)
	mux.HandleFunc("GET /api/shell/{sid}/stream", s.handleShellStream)
	mux.HandleFunc("POST /api/shell/{sid}/input", s.handleShellInput)
	mux.HandleFunc("POST /api/shell/{sid}/resize", s.handleShellResize)

	// Claude Code on a host: getting it there is host administration under
	// the host, and a conversation is a session addressed by its own id, for
	// the same reason a shell is.
	mux.HandleFunc("GET /api/hosts/{id}/claude", s.handleClaudeStatus)
	mux.HandleFunc("POST /api/hosts/{id}/claude/install", s.handleClaudeInstall)
	mux.HandleFunc("POST /api/hosts/{id}/claude/login", s.handleClaudeLogin)
	mux.HandleFunc("POST /api/hosts/{id}/claude/login/code", s.handleClaudeLoginCode)
	mux.HandleFunc("DELETE /api/hosts/{id}/claude/login", s.handleClaudeCancelLogin)
	mux.HandleFunc("POST /api/hosts/{id}/claude/key", s.handleClaudeKey)
	mux.HandleFunc("GET /api/hosts/{id}/claude/sessions", s.handleListClaudeSessions)
	mux.HandleFunc("POST /api/hosts/{id}/claude/sessions", s.handleOpenClaudeSession)
	mux.HandleFunc("GET /api/claude/{sid}", s.handleGetClaudeSession)
	mux.HandleFunc("DELETE /api/claude/{sid}", s.handleCloseClaudeSession)
	mux.HandleFunc("GET /api/claude/{sid}/stream", s.handleClaudeStream)
	mux.HandleFunc("POST /api/claude/{sid}/message", s.handleClaudeMessage)
	mux.HandleFunc("POST /api/claude/{sid}/answer", s.handleClaudeAnswer)
	mux.HandleFunc("POST /api/claude/{sid}/model", s.handleClaudeModel)
	mux.HandleFunc("POST /api/claude/{sid}/mode", s.handleClaudeMode)
	mux.HandleFunc("POST /api/claude/{sid}/interrupt", s.handleClaudeInterrupt)

	mux.HandleFunc("GET /api/hosts/{id}/remote", s.handleRemoteSession)
	mux.HandleFunc("POST /api/hosts/{id}/remote", s.handleRemoteSetup)
	mux.HandleFunc("DELETE /api/hosts/{id}/remote", s.handleRemoteRemove)
	mux.HandleFunc("POST /api/hosts/{id}/remote/action", s.handleRemoteAction)

	// The downloader is deluge on the host. Adding a torrent is a POST to the
	// collection because that is what it is — one more torrent — and the daemon
	// itself is set up and taken away under /setup, so the two cannot be
	// confused for one another.
	mux.HandleFunc("GET /api/hosts/{id}/torrents", s.handleTorrents)
	mux.HandleFunc("POST /api/hosts/{id}/torrents", s.handleTorrentAdd)
	mux.HandleFunc("POST /api/hosts/{id}/torrents/action", s.handleTorrentAction)
	mux.HandleFunc("POST /api/hosts/{id}/torrents/setup", s.handleTorrentSetup)
	mux.HandleFunc("DELETE /api/hosts/{id}/torrents/setup", s.handleTorrentRemove)

	mux.HandleFunc("GET /api/hosts/{id}/files", s.handleListFiles)
	mux.HandleFunc("DELETE /api/hosts/{id}/files", s.handleRemoveFile)
	mux.HandleFunc("GET /api/hosts/{id}/files/usage", s.handleFileUsage)
	mux.HandleFunc("GET /api/hosts/{id}/files/content", s.handleReadFile)
	mux.HandleFunc("PUT /api/hosts/{id}/files/content", s.handleWriteFile)
	mux.HandleFunc("POST /api/hosts/{id}/files/mkdir", s.handleMkdir)
	mux.HandleFunc("POST /api/hosts/{id}/files/rename", s.handleRenameFile)
	mux.HandleFunc("POST /api/hosts/{id}/files/chmod", s.handleChmodFile)

	mux.HandleFunc("GET /api/apps", s.handleListApps)
	mux.HandleFunc("POST /api/apps", s.handleCreateApp)
	mux.HandleFunc("GET /api/apps/{id}", s.handleGetApp)
	mux.HandleFunc("PATCH /api/apps/{id}", s.handleUpdateApp)
	mux.HandleFunc("DELETE /api/apps/{id}", s.handleDeleteApp)
	mux.HandleFunc("POST /api/apps/{id}/deploy", s.handleDeployApp)

	mux.HandleFunc("GET /api/installations", s.handleListInstallations)
	mux.HandleFunc("GET /api/installations/{id}", s.handleGetInstallation)
	mux.HandleFunc("DELETE /api/installations/{id}", s.handleForgetInstallation)
	mux.HandleFunc("POST /api/installations/{id}/uninstall", s.handleUninstall)
	mux.HandleFunc("POST /api/installations/{id}/check", s.handleCheckInstallation)
	mux.HandleFunc("POST /api/installations/{id}/redeploy", s.handleRedeploy)

	mux.HandleFunc("GET /api/deployments", s.handleListDeployments)
	mux.HandleFunc("GET /api/deployments/{id}", s.handleGetDeployment)
	mux.HandleFunc("GET /api/deployments/{id}/stream", s.handleDeploymentStream)
	mux.HandleFunc("POST /api/deployments/{id}/cancel", s.handleCancelDeployment)

	mux.HandleFunc("GET /api/self", s.handleGetSelf)
	mux.HandleFunc("POST /api/self/update", s.handleSelfUpdate)

	mux.HandleFunc("GET /api/overview", s.handleOverview)

	return mux
}

// handleHealth is what the self-update health check polls, so it stays cheap and
// dependency-free. The version tells an upgrade whether the binary that came
// back up is the new one.
func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok", "version": s.Version})
}

// --- helpers ---

func writeJSON(w http.ResponseWriter, code int, body any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	if body == nil {
		return
	}
	if err := json.NewEncoder(w).Encode(body); err != nil {
		// The status line is already sent; nothing useful left to do.
		return
	}
}

// mustJSON encodes a value that is known to be encodable, for embedding in a
// server-sent event.
func mustJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return `{}`
	}
	return string(b)
}

type apiError struct {
	Error string `json:"error"`
}

func writeError(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, apiError{Error: msg})
}

// writeStoreError maps a store error onto a status code.
func (s *Server) writeStoreError(w http.ResponseWriter, err error, action string) {
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	if isUniqueViolation(err) {
		writeError(w, http.StatusConflict, "that name is already taken")
		return
	}
	s.Log.Error("api: "+action, "err", err)
	writeError(w, http.StatusInternalServerError, action+" failed")
}

func isUniqueViolation(err error) bool {
	return err != nil && strings.Contains(err.Error(), "UNIQUE constraint failed")
}

func decodeJSON(r *http.Request, dst any) error {
	return decodeJSONLimit(r, dst, 1<<20)
}

// decodeJSONLimit is decodeJSON with room for a request that legitimately
// carries bulk — the contents of a file being saved on a host.
func decodeJSONLimit(r *http.Request, dst any, limit int64) error {
	dec := json.NewDecoder(http.MaxBytesReader(nil, r.Body, limit))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return errors.New("invalid request body: " + err.Error())
	}
	return nil
}

func pathID(r *http.Request) (int64, error) {
	return strconv.ParseInt(r.PathValue("id"), 10, 64)
}
