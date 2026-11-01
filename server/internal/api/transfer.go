package api

import (
	"errors"
	"net/http"

	"github.com/chinmay28/hostman/server/internal/hostops"
	"github.com/chinmay28/hostman/server/internal/store"
)

// Copying, moving and sending files are jobs: the request starts one and
// answers 202 with it, and the screen follows the job by its id. The checks
// that need no host — the paths, the destination host existing and being a
// different one — are made here, before anything is started, so a request
// that could never work is refused as a 400 rather than as a job that failed.

type transferInput struct {
	Path string `json:"path"`
	// To is the destination directory. The item keeps its name under it.
	To string `json:"to"`
	// HostID is the destination host, for a send.
	HostID int64 `json:"hostId,omitempty"`
}

func (s *Server) handleCopyFile(w http.ResponseWriter, r *http.Request) {
	h, in, ok := s.transferRequest(w, r, "copy")
	if !ok {
		return
	}
	job, err := s.Transfers.Copy(r.Context(), h, in.Path, in.To)
	if err != nil {
		s.writeOpError(w, err, "copy")
		return
	}
	writeJSON(w, http.StatusAccepted, job)
}

func (s *Server) handleMoveFile(w http.ResponseWriter, r *http.Request) {
	h, in, ok := s.transferRequest(w, r, "move")
	if !ok {
		return
	}
	job, err := s.Transfers.Move(r.Context(), h, in.Path, in.To)
	if err != nil {
		s.writeOpError(w, err, "move")
		return
	}
	writeJSON(w, http.StatusAccepted, job)
}

// handleSendFile starts a copy to another host. The destination must be a
// host HostMan knows: a 404 for one it does not, the same as any other
// request about a missing host.
func (s *Server) handleSendFile(w http.ResponseWriter, r *http.Request) {
	h, in, ok := s.transferRequest(w, r, "send")
	if !ok {
		return
	}
	if in.HostID == 0 {
		writeError(w, http.StatusBadRequest, "a destination host is required")
		return
	}
	dst, err := s.DB.GetHost(r.Context(), in.HostID)
	if err != nil {
		s.writeStoreError(w, err, "send")
		return
	}
	job, err := s.Transfers.Send(r.Context(), h, in.Path, dst, in.To)
	if err != nil {
		s.writeOpError(w, err, "send")
		return
	}
	writeJSON(w, http.StatusAccepted, job)
}

// transferRequest reads the host and the body shared by the three verbs, and
// answers the request itself when either is no good.
func (s *Server) transferRequest(w http.ResponseWriter, r *http.Request, action string) (h *store.Host, in transferInput, ok bool) {
	host, err := s.hostFromPath(r)
	if err != nil {
		s.writeStoreError(w, err, action)
		return nil, in, false
	}
	if err := decodeJSON(r, &in); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return nil, in, false
	}
	if in.Path, err = hostops.CleanPath(in.Path); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return nil, in, false
	}
	if in.To, err = hostops.CleanPath(in.To); err != nil {
		writeError(w, http.StatusBadRequest, "destination: "+err.Error())
		return nil, in, false
	}
	return host, in, true
}

// handleListFileJobs lists the jobs a host is part of, as source or as
// destination, newest first.
func (s *Server) handleListFileJobs(w http.ResponseWriter, r *http.Request) {
	h, err := s.hostFromPath(r)
	if err != nil {
		s.writeStoreError(w, err, "list jobs")
		return
	}
	writeJSON(w, http.StatusOK, s.Transfers.ForHost(h.ID))
}

func (s *Server) handleGetFileJob(w http.ResponseWriter, r *http.Request) {
	job, err := s.Transfers.Get(r.PathValue("jid"))
	if err != nil {
		s.writeJobError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, job)
}

func (s *Server) handleCancelFileJob(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("jid")
	if err := s.Transfers.Cancel(id); err != nil {
		s.writeJobError(w, err)
		return
	}
	s.Log.Info("api: cancelled a transfer", "job", id)
	job, err := s.Transfers.Get(id)
	if err != nil {
		s.writeJobError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, job)
}

// handleForgetFileJob drops a finished job from the list. A running one is a
// 409: cancelling it is the step before this.
func (s *Server) handleForgetFileJob(w http.ResponseWriter, r *http.Request) {
	if err := s.Transfers.Forget(r.PathValue("jid")); err != nil {
		s.writeJobError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) writeJobError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, hostops.ErrJobNotFound):
		writeError(w, http.StatusNotFound, err.Error())
	case errors.Is(err, hostops.ErrJobRunning):
		writeError(w, http.StatusConflict, err.Error())
	default:
		writeError(w, http.StatusInternalServerError, err.Error())
	}
}
