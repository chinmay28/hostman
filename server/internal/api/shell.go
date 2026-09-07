package api

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/chinmay28/hostman/server/internal/shell"
)

// A shell is a login shell on a host with a pty behind it, and it outlives the
// screen looking at it. That shapes the endpoints: opening one and watching one
// are separate calls, the handle is an opaque id rather than the connection,
// and the stream can be asked to start from an offset. A phone that locked its
// screen mid-command reconnects, asks for the bytes it missed, and is back in
// the same shell in the same directory.
//
// Keystrokes and screen bytes both cross the wire base64-encoded. A pty deals
// in bytes, not text: half a UTF-8 character can legitimately end a chunk, a
// control byte is the point rather than a hazard, and SSE is a line protocol
// that would otherwise have to escape every newline a terminal draws.

// shellOpenTimeout bounds opening a shell — one SSH connection and one pty
// request. Everything after that is unbounded on purpose: a shell is meant to
// sit there.
const shellOpenTimeout = 30 * time.Second

// shellHeartbeat is how often an idle stream says something, so a proxy or a
// phone radio does not decide the connection is dead. It is shorter than the
// deployment stream's because a shell is idle far more of the time.
const shellHeartbeat = 15 * time.Second

// shellOpenRequest is the geometry the screen worked out for itself. Both are
// clamped server-side, so an absent or absurd size is a small terminal rather
// than a rejected request.
type shellOpenRequest struct {
	Cols int `json:"cols"`
	Rows int `json:"rows"`
}

type shellInputRequest struct {
	// Data is base64. A keystroke is one byte, and the ones that matter most —
	// Ctrl-C, Tab, an arrow key's escape sequence — are not text.
	Data string `json:"data"`
}

func (s *Server) handleListShells(w http.ResponseWriter, r *http.Request) {
	h, err := s.hostFromPath(r)
	if err != nil {
		s.writeStoreError(w, err, "list shells")
		return
	}
	sessions := s.Shells.ForHost(h.ID)
	// A JSON array rather than null, so the screen can map over it unguarded.
	out := make([]shell.Info, 0, len(sessions))
	for _, sess := range sessions {
		out = append(out, sess.Info())
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleOpenShell(w http.ResponseWriter, r *http.Request) {
	h, err := s.hostFromPath(r)
	if err != nil {
		s.writeStoreError(w, err, "open shell")
		return
	}
	var req shellOpenRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), shellOpenTimeout)
	defer cancel()

	sess, err := s.Shells.Open(ctx, h, req.Cols, req.Rows)
	if err != nil {
		s.writeShellError(w, err, "open shell")
		return
	}
	s.Log.Info("api: opened a shell", "host", h.Name, "session", sess.ID())
	writeJSON(w, http.StatusCreated, sess.Info())
}

func (s *Server) handleGetShell(w http.ResponseWriter, r *http.Request) {
	sess, err := s.Shells.Get(r.PathValue("sid"))
	if err != nil {
		s.writeShellError(w, err, "read shell")
		return
	}
	writeJSON(w, http.StatusOK, sess.Info())
}

func (s *Server) handleCloseShell(w http.ResponseWriter, r *http.Request) {
	if err := s.Shells.Close(r.PathValue("sid")); err != nil {
		s.writeShellError(w, err, "close shell")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleShellInput(w http.ResponseWriter, r *http.Request) {
	sess, err := s.Shells.Get(r.PathValue("sid"))
	if err != nil {
		s.writeShellError(w, err, "send to shell")
		return
	}
	var req shellInputRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	data, err := base64.StdEncoding.DecodeString(req.Data)
	if err != nil {
		writeError(w, http.StatusBadRequest, "input must be base64")
		return
	}
	if err := sess.Write(data); err != nil {
		s.writeShellError(w, err, "send to shell")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleShellResize(w http.ResponseWriter, r *http.Request) {
	sess, err := s.Shells.Get(r.PathValue("sid"))
	if err != nil {
		s.writeShellError(w, err, "resize shell")
		return
	}
	var req shellOpenRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := sess.Resize(req.Cols, req.Rows); err != nil {
		s.writeShellError(w, err, "resize shell")
		return
	}
	writeJSON(w, http.StatusOK, sess.Info())
}

// handleShellStream streams the screen as Server-Sent Events, starting from the
// offset the client asks for. Zero means the beginning of what is still kept.
//
// SSE rather than a socket because a shell on a phone spends its life being
// interrupted: the browser reconnects an EventSource by itself, the client
// knows the offset it reached, and asking again from there is the whole of the
// recovery. Keystrokes go the other way as ordinary POSTs, which need no
// connection to have stayed up.
func (s *Server) handleShellStream(w http.ResponseWriter, r *http.Request) {
	sess, err := s.Shells.Get(r.PathValue("sid"))
	if err != nil {
		s.writeShellError(w, err, "watch shell")
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "streaming unsupported")
		return
	}
	from := int64(0)
	if v := r.URL.Query().Get("from"); v != "" {
		if n, err := parseOffset(v); err == nil {
			from = n
		}
	}

	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	h.Set("Connection", "keep-alive")
	h.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)

	// Watching is what keeps the session out of the reaper's way; detaching is
	// what starts its idle clock again.
	detach := sess.Attach()
	defer detach()

	// The screen learns the session's own view of itself first, so a client
	// that reconnected into a shell somebody else has since resized can correct
	// its geometry before drawing anything.
	fmt.Fprintf(w, "event: session\ndata: %s\n\n", mustJSON(sess.Info()))
	flusher.Flush()

	for {
		// A read bounded by the heartbeat rather than by nothing: the offset
		// lives out here, so timing one out costs nothing and gives the idle
		// connection something to say.
		ctx, cancel := context.WithTimeout(r.Context(), shellHeartbeat)
		chunk, err := sess.Read(ctx, from)
		cancel()
		if err != nil {
			if r.Context().Err() != nil {
				return
			}
			fmt.Fprint(w, ": keepalive\n\n")
			flusher.Flush()
			continue
		}
		if len(chunk.Data) > 0 {
			payload := map[string]any{
				"from": chunk.From,
				"next": chunk.Next,
				"data": base64.StdEncoding.EncodeToString(chunk.Data),
			}
			fmt.Fprintf(w, "event: out\ndata: %s\n\n", mustJSON(payload))
			flusher.Flush()
			from = chunk.Next
		}
		if chunk.Done {
			fmt.Fprintf(w, "event: exit\ndata: %s\n\n", mustJSON(map[string]any{"exit": chunk.Exit}))
			flusher.Flush()
			return
		}
	}
}

// writeShellError maps a shell failure onto a status code. A session that has
// been reaped is a 404 and not an error worth logging: it is what happens to
// every shell eventually.
func (s *Server) writeShellError(w http.ResponseWriter, err error, action string) {
	switch {
	case errors.Is(err, shell.ErrNotFound):
		writeError(w, http.StatusNotFound, err.Error())
	case errors.Is(err, shell.ErrClosed):
		writeError(w, http.StatusConflict, err.Error())
	case errors.Is(err, shell.ErrTooMany):
		writeError(w, http.StatusConflict, err.Error())
	default:
		s.Log.Debug("api: "+action, "err", err)
		writeError(w, http.StatusBadGateway, err.Error())
	}
}

// parseOffset reads a stream cursor, refusing anything that is not a
// non-negative number rather than silently treating it as zero and replaying
// the whole screen over a shell somebody is using.
func parseOffset(v string) (int64, error) {
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		return 0, err
	}
	if n < 0 {
		return 0, errors.New("negative offset")
	}
	return n, nil
}
