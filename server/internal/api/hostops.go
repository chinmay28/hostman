package api

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/chinmay28/deployer/server/internal/hostops"
	"github.com/chinmay28/deployer/server/internal/store"
)

// Managing a host — its files, its services, its crontab, restarting it — is
// work HostMan asks the host to do. So the failures worth telling apart are
// "you asked for something impossible" (400) and "the host refused" (502): a
// missing file or a read-only filesystem is not a bug in this API, and saying
// so as a 500 would send the user looking in the wrong place.

// opTimeout bounds one operation on a host. Generous enough for a slow
// handshake to a sleeping Pi, short enough that a hung host frees the request.
const opTimeout = 30 * time.Second

// unitTimeout bounds a systemctl verb. Starting or stopping a service is the
// one thing here that is allowed to take its time: systemd's own default is to
// wait 90 seconds for a unit to come up or go down, and giving up before it
// does would report a failure that has not happened yet.
const unitTimeout = 110 * time.Second

// opContext gives a host operation its own deadline.
func opContext(r *http.Request) (context.Context, context.CancelFunc) {
	return context.WithTimeout(r.Context(), opTimeout)
}

// writeOpError maps a failure on the host onto a status code.
func (s *Server) writeOpError(w http.ResponseWriter, err error, action string) {
	switch {
	case errors.Is(err, hostops.ErrInvalid):
		// HostMan refused it, not the host: the request is what needs fixing.
		writeError(w, http.StatusBadRequest, err.Error())
	case errors.Is(err, hostops.ErrNotEmpty):
		// The caller can retry asking for the contents to go too.
		writeError(w, http.StatusConflict, err.Error())
	case errors.Is(err, hostops.ErrUnitRunning):
		// Stopping it first is a step the caller can take, not a mistake.
		writeError(w, http.StatusConflict, err.Error())
	case errors.Is(err, hostops.ErrNeedsRoot):
		writeError(w, http.StatusForbidden, err.Error())
	default:
		s.Log.Debug("api: "+action, "err", err)
		writeError(w, http.StatusBadGateway, err.Error())
	}
}

// --- reboot ---

// handleHostReboot restarts a host. The host is marked offline straight away:
// it is about to go, and a UI that keeps calling it online until the next poll
// fails would be lying for half a minute.
//
// Restarting is the only power state offered. HostMan can watch a machine come
// back from a reboot; it cannot bring one back from off.
func (s *Server) handleHostReboot(w http.ResponseWriter, r *http.Request) {
	h, err := s.hostFromPath(r)
	if err != nil {
		s.writeStoreError(w, err, "reboot")
		return
	}
	ctx, cancel := opContext(r)
	defer cancel()

	if err := s.Ops.Reboot(ctx, h); err != nil {
		s.writeOpError(w, err, "reboot host")
		return
	}
	s.Log.Info("api: rebooting host", "host", h.Name)

	const reason = "rebooting — waiting for it to come back"
	if err := s.DB.MarkHostFailed(r.Context(), h.ID, store.StatusOffline, reason); err != nil {
		s.Log.Error("api: mark host going down", "host", h.Name, "err", err)
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": reason})
}

// --- why it restarted ---

// bootTimeout bounds the read of a host's boot record. It is longer than an
// ordinary operation because reading the previous boot out of the journal means
// systemd walking its archives, which on a Pi's SD card is not instant.
const bootTimeout = 60 * time.Second

// handleHostBoot answers "why did it restart?" with HostMan's best guess and
// everything it looked at to make it. The guess is made on the host's evidence
// in one place — hostops.diagnose — so what this returns is what the screen
// shows, rather than a second opinion assembled here.
func (s *Server) handleHostBoot(w http.ResponseWriter, r *http.Request) {
	h, err := s.hostFromPath(r)
	if err != nil {
		s.writeStoreError(w, err, "read boot record")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), bootTimeout)
	defer cancel()

	report, err := s.Ops.LastBoot(ctx, h)
	if err != nil {
		s.writeOpError(w, err, "read boot record")
		return
	}
	writeJSON(w, http.StatusOK, report)
}

// handleKeepJournal makes the host's journal survive a restart. It is the
// answer to the commonest reason the handler above has nothing to say: on
// Debian, and so on Raspberry Pi OS, systemd keeps its log in memory unless
// /var/log/journal exists, which means the log of the boot that crashed dies
// with it.
//
// A host that already keeps its journal is not an error and says so.
func (s *Server) handleKeepJournal(w http.ResponseWriter, r *http.Request) {
	h, err := s.hostFromPath(r)
	if err != nil {
		s.writeStoreError(w, err, "keep the journal")
		return
	}
	ctx, cancel := opContext(r)
	defer cancel()

	storage, err := s.Ops.KeepJournal(ctx, h)
	if err != nil {
		s.writeOpError(w, err, "keep the journal")
		return
	}
	if !storage.Already {
		s.Log.Info("api: turned on persistent logging", "host", h.Name)
	}
	writeJSON(w, http.StatusOK, storage)
}

// --- crontab ---

func (s *Server) handleGetCrontab(w http.ResponseWriter, r *http.Request) {
	h, err := s.hostFromPath(r)
	if err != nil {
		s.writeStoreError(w, err, "read crontab")
		return
	}
	ctx, cancel := opContext(r)
	defer cancel()

	cron, err := s.Ops.ReadCrontab(ctx, h, r.URL.Query().Get("user"))
	if err != nil {
		s.writeOpError(w, err, "read crontab")
		return
	}
	writeJSON(w, http.StatusOK, cron)
}

type crontabInput struct {
	User    string `json:"user"`
	Content string `json:"content"`
}

// handlePutCrontab installs a crontab. cron parses it on the way in and keeps
// the old one if it cannot, so a syntax error comes back as the host's own
// complaint rather than as a broken schedule.
func (s *Server) handlePutCrontab(w http.ResponseWriter, r *http.Request) {
	h, err := s.hostFromPath(r)
	if err != nil {
		s.writeStoreError(w, err, "write crontab")
		return
	}
	var in crontabInput
	if err := decodeJSONLimit(r, &in, 2*hostops.MaxCrontabBytes); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	ctx, cancel := opContext(r)
	defer cancel()

	if err := s.Ops.WriteCrontab(ctx, h, in.User, in.Content); err != nil {
		s.writeOpError(w, err, "write crontab")
		return
	}
	s.Log.Info("api: wrote crontab", "host", h.Name, "user", in.User)

	cron, err := s.Ops.ReadCrontab(ctx, h, in.User)
	if err != nil {
		// It was installed; failing to read it back is not worth an error.
		writeJSON(w, http.StatusOK, &hostops.Crontab{User: in.User, Content: in.Content, Exists: true})
		return
	}
	writeJSON(w, http.StatusOK, cron)
}

// --- services ---

// handleListServices lists the services someone installed on the host by hand.
// Distribution units are left out on purpose: the point of the screen is the
// handful of things this machine was set up to run.
func (s *Server) handleListServices(w http.ResponseWriter, r *http.Request) {
	h, err := s.hostFromPath(r)
	if err != nil {
		s.writeStoreError(w, err, "list services")
		return
	}
	ctx, cancel := opContext(r)
	defer cancel()

	units, err := s.Ops.Units(ctx, h)
	if err != nil {
		s.writeOpError(w, err, "list services")
		return
	}
	writeJSON(w, http.StatusOK, units)
}

func (s *Server) handleGetService(w http.ResponseWriter, r *http.Request) {
	h, err := s.hostFromPath(r)
	if err != nil {
		s.writeStoreError(w, err, "read service")
		return
	}
	name, ok := queryUnit(w, r)
	if !ok {
		return
	}
	ctx, cancel := opContext(r)
	defer cancel()

	unit, err := s.Ops.Unit(ctx, h, name)
	if err != nil {
		s.writeOpError(w, err, "read service")
		return
	}
	writeJSON(w, http.StatusOK, unit)
}

func (s *Server) handleServiceLogs(w http.ResponseWriter, r *http.Request) {
	h, err := s.hostFromPath(r)
	if err != nil {
		s.writeStoreError(w, err, "read service log")
		return
	}
	name, ok := queryUnit(w, r)
	if !ok {
		return
	}
	// An unreadable or absent line count means the default, not a 400: the
	// number is the UI's business and any of them gives a usable answer.
	lines, _ := strconv.Atoi(r.URL.Query().Get("lines"))
	ctx, cancel := opContext(r)
	defer cancel()

	log, err := s.Ops.Log(ctx, h, name, lines)
	if err != nil {
		s.writeOpError(w, err, "read service log")
		return
	}
	writeJSON(w, http.StatusOK, log)
}

type createServiceInput struct {
	Name    string `json:"name"`
	Content string `json:"content"`
}

// handleCreateService writes a new unit file and hands it to systemd. It is
// created stopped: starting it and enabling it are separate calls, so a unit
// that will not start comes back as a service that exists and did not start
// rather than as an install that half happened.
func (s *Server) handleCreateService(w http.ResponseWriter, r *http.Request) {
	h, err := s.hostFromPath(r)
	if err != nil {
		s.writeStoreError(w, err, "create service")
		return
	}
	var in createServiceInput
	if err := decodeJSONLimit(r, &in, 4*hostops.MaxUnitBytes); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	ctx, cancel := opContext(r)
	defer cancel()

	unit, err := s.Ops.CreateUnit(ctx, h, in.Name, in.Content)
	if err != nil {
		s.writeOpError(w, err, "create service")
		return
	}
	s.Log.Info("api: created service", "host", h.Name, "unit", unit.Name)
	writeJSON(w, http.StatusCreated, unit)
}

// handleDeleteService removes a service that is not running. A running one
// comes back as a 409: stopping it is the caller's next step, not an error in
// what they asked for.
func (s *Server) handleDeleteService(w http.ResponseWriter, r *http.Request) {
	h, err := s.hostFromPath(r)
	if err != nil {
		s.writeStoreError(w, err, "delete service")
		return
	}
	name, ok := queryUnit(w, r)
	if !ok {
		return
	}
	ctx, cancel := opContext(r)
	defer cancel()

	if err := s.Ops.RemoveUnit(ctx, h, name); err != nil {
		s.writeOpError(w, err, "delete service")
		return
	}
	s.Log.Info("api: deleted service", "host", h.Name, "unit", name)
	w.WriteHeader(http.StatusNoContent)
}

type serviceActionInput struct {
	Name   string `json:"name"`
	Action string `json:"action"`
}

// handleServiceAction runs one systemctl verb and answers with the state the
// service ended up in, so the screen that asked does not have to guess whether
// "start" left it running or failed a second later.
func (s *Server) handleServiceAction(w http.ResponseWriter, r *http.Request) {
	h, err := s.hostFromPath(r)
	if err != nil {
		s.writeStoreError(w, err, "manage service")
		return
	}
	var in serviceActionInput
	if err := decodeJSON(r, &in); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), unitTimeout)
	defer cancel()

	if err := s.Ops.Act(ctx, h, in.Name, in.Action); err != nil {
		s.writeOpError(w, err, "manage service")
		return
	}
	s.Log.Info("api: systemctl", "host", h.Name, "unit", in.Name, "action", in.Action)

	unit, err := s.Ops.Unit(ctx, h, in.Name)
	if err != nil {
		// The action worked; failing to read the state back afterwards is the
		// next request's problem, not this one's.
		writeJSON(w, http.StatusOK, map[string]string{"name": in.Name, "action": in.Action})
		return
	}
	writeJSON(w, http.StatusOK, unit)
}

// handleReloadServices makes systemd re-read the unit files on disk, which is
// what turns an edited unit file into an edited service.
func (s *Server) handleReloadServices(w http.ResponseWriter, r *http.Request) {
	h, err := s.hostFromPath(r)
	if err != nil {
		s.writeStoreError(w, err, "reload services")
		return
	}
	ctx, cancel := opContext(r)
	defer cancel()

	if err := s.Ops.Reload(ctx, h); err != nil {
		s.writeOpError(w, err, "reload services")
		return
	}
	s.Log.Info("api: daemon-reload", "host", h.Name)
	writeJSON(w, http.StatusOK, map[string]string{"status": "reloaded"})
}

// queryUnit reads and validates the ?name= parameter, answering the request
// itself when it is missing or is not a service name.
func queryUnit(w http.ResponseWriter, r *http.Request) (string, bool) {
	name, err := hostops.CleanUnit(r.URL.Query().Get("name"))
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return "", false
	}
	return name, true
}

// --- files ---

func (s *Server) handleListFiles(w http.ResponseWriter, r *http.Request) {
	h, err := s.hostFromPath(r)
	if err != nil {
		s.writeStoreError(w, err, "list files")
		return
	}
	ctx, cancel := opContext(r)
	defer cancel()

	// An absent path means the SSH user's home, which only the host knows.
	listing, err := s.Ops.List(ctx, h, r.URL.Query().Get("path"))
	if err != nil {
		s.writeOpError(w, err, "list files")
		return
	}
	writeJSON(w, http.StatusOK, listing)
}

func (s *Server) handleReadFile(w http.ResponseWriter, r *http.Request) {
	h, err := s.hostFromPath(r)
	if err != nil {
		s.writeStoreError(w, err, "read file")
		return
	}
	path, ok := queryPath(w, r)
	if !ok {
		return
	}
	ctx, cancel := opContext(r)
	defer cancel()

	file, err := s.Ops.Read(ctx, h, path)
	if err != nil {
		s.writeOpError(w, err, "read file")
		return
	}
	writeJSON(w, http.StatusOK, file)
}

// usageTimeout bounds measuring a directory. Counting a tree is a walk over
// every entry in it, and on a Pi's SD card a home directory full of photos
// takes longer than the ordinary limit allows.
const usageTimeout = 3 * time.Minute

// handleFileUsage counts what is under a directory and how much disk it takes.
func (s *Server) handleFileUsage(w http.ResponseWriter, r *http.Request) {
	h, err := s.hostFromPath(r)
	if err != nil {
		s.writeStoreError(w, err, "measure directory")
		return
	}
	path, ok := queryPath(w, r)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), usageTimeout)
	defer cancel()

	usage, err := s.Ops.Usage(ctx, h, path)
	if err != nil {
		s.writeOpError(w, err, "measure directory")
		return
	}
	writeJSON(w, http.StatusOK, usage)
}

type writeFileInput struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

func (s *Server) handleWriteFile(w http.ResponseWriter, r *http.Request) {
	h, err := s.hostFromPath(r)
	if err != nil {
		s.writeStoreError(w, err, "write file")
		return
	}
	var in writeFileInput
	// Room for the file itself, plus what JSON escaping may add to it.
	if err := decodeJSONLimit(r, &in, 4*hostops.MaxWriteBytes); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if _, err := hostops.CleanPath(in.Path); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	ctx, cancel := opContext(r)
	defer cancel()

	if err := s.Ops.Write(ctx, h, in.Path, in.Content); err != nil {
		s.writeOpError(w, err, "write file")
		return
	}
	s.Log.Info("api: wrote file", "host", h.Name, "path", in.Path, "bytes", len(in.Content))

	// Answer with the file as it now is, so the editor picks up the size and
	// modification time it was just given rather than guessing at them.
	file, err := s.Ops.Read(ctx, h, in.Path)
	if err != nil {
		writeJSON(w, http.StatusOK, &hostops.File{Path: in.Path, Content: in.Content})
		return
	}
	writeJSON(w, http.StatusOK, file)
}

type pathInput struct {
	Path string `json:"path"`
}

func (s *Server) handleMkdir(w http.ResponseWriter, r *http.Request) {
	h, err := s.hostFromPath(r)
	if err != nil {
		s.writeStoreError(w, err, "create directory")
		return
	}
	var in pathInput
	if err := decodeJSON(r, &in); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	clean, err := hostops.CleanPath(in.Path)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	ctx, cancel := opContext(r)
	defer cancel()

	if err := s.Ops.Mkdir(ctx, h, clean); err != nil {
		s.writeOpError(w, err, "create directory")
		return
	}
	writeJSON(w, http.StatusCreated, map[string]string{"path": clean})
}

type renameInput struct {
	Path string `json:"path"`
	To   string `json:"to"`
}

func (s *Server) handleRenameFile(w http.ResponseWriter, r *http.Request) {
	h, err := s.hostFromPath(r)
	if err != nil {
		s.writeStoreError(w, err, "rename")
		return
	}
	var in renameInput
	if err := decodeJSON(r, &in); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	from, err := hostops.CleanPath(in.Path)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	to, err := hostops.CleanPath(in.To)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	ctx, cancel := opContext(r)
	defer cancel()

	if err := s.Ops.Rename(ctx, h, from, to); err != nil {
		s.writeOpError(w, err, "rename")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"path": to})
}

// recursiveChmodTimeout bounds a `chmod -R`. Walking a directory tree is work
// the host does entry by entry, and on a Pi's SD card a deep one takes longer
// than the ordinary limit allows — a request that timed out halfway would leave
// half a tree changed and say nothing useful about which half.
const recursiveChmodTimeout = 3 * time.Minute

type chmodInput struct {
	Path string `json:"path"`
	// Mode is octal, as the listing shows it: "755", or "0644".
	Mode string `json:"mode"`
	// Recursive applies the mode to everything inside a directory too.
	Recursive bool `json:"recursive"`
}

func (s *Server) handleChmodFile(w http.ResponseWriter, r *http.Request) {
	h, err := s.hostFromPath(r)
	if err != nil {
		s.writeStoreError(w, err, "change permissions")
		return
	}
	var in chmodInput
	if err := decodeJSON(r, &in); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	clean, err := hostops.CleanPath(in.Path)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	mode, err := hostops.CleanMode(in.Mode)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	timeout := opTimeout
	if in.Recursive {
		timeout = recursiveChmodTimeout
	}
	ctx, cancel := context.WithTimeout(r.Context(), timeout)
	defer cancel()

	got, err := s.Ops.Chmod(ctx, h, clean, mode, in.Recursive)
	if err != nil {
		s.writeOpError(w, err, "change permissions")
		return
	}
	s.Log.Info("api: changed permissions", "host", h.Name, "path", clean, "mode", mode, "recursive", in.Recursive)
	writeJSON(w, http.StatusOK, map[string]string{"path": clean, "mode": got})
}

func (s *Server) handleRemoveFile(w http.ResponseWriter, r *http.Request) {
	h, err := s.hostFromPath(r)
	if err != nil {
		s.writeStoreError(w, err, "delete file")
		return
	}
	path, ok := queryPath(w, r)
	if !ok {
		return
	}
	ctx, cancel := opContext(r)
	defer cancel()

	recursive := r.URL.Query().Get("recursive") == "true"
	if err := s.Ops.Remove(ctx, h, path, recursive); err != nil {
		s.writeOpError(w, err, "delete file")
		return
	}
	s.Log.Info("api: deleted", "host", h.Name, "path", path, "recursive", recursive)
	w.WriteHeader(http.StatusNoContent)
}

// queryPath reads and validates the ?path= parameter, answering the request
// itself when it is missing or malformed.
func queryPath(w http.ResponseWriter, r *http.Request) (string, bool) {
	clean, err := hostops.CleanPath(r.URL.Query().Get("path"))
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return "", false
	}
	return clean, true
}
