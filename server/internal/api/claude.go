package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/chinmay28/hostman/server/internal/claude"
	"github.com/chinmay28/hostman/server/internal/hostops"
)

// Claude on a host is two things with two shapes of endpoint.
//
// Getting it there — installed, signed in — is host administration like the
// remote session's: a status that is safe to poll, and a few verbs that start
// something the host gets on with by itself.
//
// Talking to it is a session, shaped like a shell's: opened and listed under
// its host, then addressed by its own id, because the session is what
// persists and the screen may be a different phone. The stream is Server-Sent
// Events from an offset, for the same reason a shell's is — a phone spends its
// life being interrupted, and asking again from where it got to is the whole
// of the recovery. Messages and answers go the other way as ordinary POSTs.

// claudeOpenTimeout bounds opening a session: one SSH connection and one
// process start. Everything after that is unbounded on purpose.
const claudeOpenTimeout = 30 * time.Second

// claudeControlTimeout bounds a model or mode change, which the CLI answers
// at once or not at all.
const claudeControlTimeout = 25 * time.Second

// claudeHeartbeat is how often an idle stream says something, so a proxy or a
// phone radio does not decide the connection is dead.
const claudeHeartbeat = 15 * time.Second

// --- getting it onto the host ---

func (s *Server) handleClaudeStatus(w http.ResponseWriter, r *http.Request) {
	h, err := s.hostFromPath(r)
	if err != nil {
		s.writeStoreError(w, err, "read Claude")
		return
	}
	ctx, cancel := opContext(r)
	defer cancel()
	c, err := s.Ops.ClaudeStatus(ctx, h)
	if err != nil {
		s.writeOpError(w, err, "read Claude")
		return
	}
	writeJSON(w, http.StatusOK, c)
}

func (s *Server) handleClaudeInstall(w http.ResponseWriter, r *http.Request) {
	h, err := s.hostFromPath(r)
	if err != nil {
		s.writeStoreError(w, err, "install Claude")
		return
	}
	ctx, cancel := opContext(r)
	defer cancel()
	c, err := s.Ops.InstallClaude(ctx, h)
	if err != nil {
		s.writeOpError(w, err, "install Claude")
		return
	}
	s.Log.Info("api: installing Claude", "host", h.Name, "user", h.Username)
	writeJSON(w, http.StatusOK, c)
}

type claudeLoginInput struct {
	// Console asks for Anthropic Console (API billing) rather than a Claude
	// subscription.
	Console bool `json:"console"`
}

func (s *Server) handleClaudeLogin(w http.ResponseWriter, r *http.Request) {
	h, err := s.hostFromPath(r)
	if err != nil {
		s.writeStoreError(w, err, "sign in to Claude")
		return
	}
	var in claudeLoginInput
	if r.ContentLength > 0 {
		if err := decodeJSON(r, &in); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
	}
	ctx, cancel := opContext(r)
	defer cancel()
	c, err := s.Ops.LoginClaude(ctx, h, in.Console)
	if err != nil {
		s.writeOpError(w, err, "sign in to Claude")
		return
	}
	s.Log.Info("api: started a Claude sign-in", "host", h.Name, "user", h.Username)
	writeJSON(w, http.StatusOK, c)
}

type claudeLoginCodeInput struct {
	Code string `json:"code"`
}

func (s *Server) handleClaudeLoginCode(w http.ResponseWriter, r *http.Request) {
	h, err := s.hostFromPath(r)
	if err != nil {
		s.writeStoreError(w, err, "sign in to Claude")
		return
	}
	var in claudeLoginCodeInput
	if err := decodeJSON(r, &in); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	ctx, cancel := opContext(r)
	defer cancel()
	c, err := s.Ops.LoginCode(ctx, h, in.Code)
	if err != nil {
		s.writeOpError(w, err, "sign in to Claude")
		return
	}
	writeJSON(w, http.StatusOK, c)
}

func (s *Server) handleClaudeCancelLogin(w http.ResponseWriter, r *http.Request) {
	h, err := s.hostFromPath(r)
	if err != nil {
		s.writeStoreError(w, err, "cancel the Claude sign-in")
		return
	}
	ctx, cancel := opContext(r)
	defer cancel()
	c, err := s.Ops.CancelLogin(ctx, h)
	if err != nil {
		s.writeOpError(w, err, "cancel the Claude sign-in")
		return
	}
	writeJSON(w, http.StatusOK, c)
}

type claudeKeyInput struct {
	Key string `json:"key"`
}

func (s *Server) handleClaudeKey(w http.ResponseWriter, r *http.Request) {
	h, err := s.hostFromPath(r)
	if err != nil {
		s.writeStoreError(w, err, "store the API key")
		return
	}
	var in claudeKeyInput
	if err := decodeJSON(r, &in); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	ctx, cancel := opContext(r)
	defer cancel()
	c, err := s.Ops.SetAPIKey(ctx, h, in.Key)
	if err != nil {
		s.writeOpError(w, err, "store the API key")
		return
	}
	// The key itself is deliberately not logged.
	s.Log.Info("api: stored an API key for Claude", "host", h.Name, "user", h.Username)
	writeJSON(w, http.StatusOK, c)
}

// --- sessions ---

type claudeOpenRequest struct {
	Dir   string `json:"dir"`
	Model string `json:"model"`
	Mode  string `json:"mode"`
	Name  string `json:"name"`
}

func (s *Server) handleListClaudeSessions(w http.ResponseWriter, r *http.Request) {
	h, err := s.hostFromPath(r)
	if err != nil {
		s.writeStoreError(w, err, "list Claude sessions")
		return
	}
	sessions := s.Claude.ForHost(h.ID)
	out := make([]claude.Info, 0, len(sessions))
	for _, sess := range sessions {
		out = append(out, sess.Info())
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleOpenClaudeSession(w http.ResponseWriter, r *http.Request) {
	h, err := s.hostFromPath(r)
	if err != nil {
		s.writeStoreError(w, err, "open a Claude session")
		return
	}
	var req claudeOpenRequest
	if r.ContentLength > 0 {
		if err := decodeJSON(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
	}
	if len(req.Name) > 80 {
		writeError(w, http.StatusBadRequest, "the name is too long")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), claudeOpenTimeout)
	defer cancel()

	sess, err := s.Claude.Open(ctx, h, claude.Options{Dir: req.Dir, Model: req.Model, Mode: req.Mode, Name: req.Name})
	if err != nil {
		s.writeClaudeError(w, err, "open a Claude session")
		return
	}
	s.Log.Info("api: opened a Claude session", "host", h.Name, "session", sess.ID(), "mode", req.Mode)
	writeJSON(w, http.StatusCreated, sess.Info())
}

func (s *Server) handleGetClaudeSession(w http.ResponseWriter, r *http.Request) {
	sess, err := s.Claude.Get(r.PathValue("sid"))
	if err != nil {
		s.writeClaudeError(w, err, "read the Claude session")
		return
	}
	writeJSON(w, http.StatusOK, sess.Info())
}

func (s *Server) handleCloseClaudeSession(w http.ResponseWriter, r *http.Request) {
	if err := s.Claude.Close(r.PathValue("sid")); err != nil {
		s.writeClaudeError(w, err, "end the Claude session")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

type claudeMessageInput struct {
	Text string `json:"text"`
}

func (s *Server) handleClaudeMessage(w http.ResponseWriter, r *http.Request) {
	sess, err := s.Claude.Get(r.PathValue("sid"))
	if err != nil {
		s.writeClaudeError(w, err, "send to Claude")
		return
	}
	var in claudeMessageInput
	if err := decodeJSON(r, &in); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := sess.Send(in.Text); err != nil {
		s.writeClaudeError(w, err, "send to Claude")
		return
	}
	writeJSON(w, http.StatusOK, sess.Info())
}

type claudeAnswerInput struct {
	RequestID string `json:"requestId"`
	Allow     bool   `json:"allow"`
	// Always asks the CLI to remember the answer for the rest of the session.
	Always bool `json:"always"`
	// Reason is passed to Claude with a no, so it can do something else.
	Reason string `json:"reason"`
}

func (s *Server) handleClaudeAnswer(w http.ResponseWriter, r *http.Request) {
	sess, err := s.Claude.Get(r.PathValue("sid"))
	if err != nil {
		s.writeClaudeError(w, err, "answer Claude")
		return
	}
	var in claudeAnswerInput
	if err := decodeJSON(r, &in); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if in.RequestID == "" {
		writeError(w, http.StatusBadRequest, "requestId is required")
		return
	}
	if err := sess.Answer(in.RequestID, in.Allow, in.Always, in.Reason); err != nil {
		s.writeClaudeError(w, err, "answer Claude")
		return
	}
	writeJSON(w, http.StatusOK, sess.Info())
}

type claudeModelInput struct {
	Model string `json:"model"`
}

func (s *Server) handleClaudeModel(w http.ResponseWriter, r *http.Request) {
	sess, err := s.Claude.Get(r.PathValue("sid"))
	if err != nil {
		s.writeClaudeError(w, err, "change the model")
		return
	}
	var in claudeModelInput
	if err := decodeJSON(r, &in); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), claudeControlTimeout)
	defer cancel()
	if err := sess.SetModel(ctx, in.Model); err != nil {
		s.writeClaudeError(w, err, "change the model")
		return
	}
	writeJSON(w, http.StatusOK, sess.Info())
}

type claudeModeInput struct {
	Mode string `json:"mode"`
}

func (s *Server) handleClaudeMode(w http.ResponseWriter, r *http.Request) {
	sess, err := s.Claude.Get(r.PathValue("sid"))
	if err != nil {
		s.writeClaudeError(w, err, "change the permission mode")
		return
	}
	var in claudeModeInput
	if err := decodeJSON(r, &in); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), claudeControlTimeout)
	defer cancel()
	if err := sess.SetMode(ctx, in.Mode); err != nil {
		s.writeClaudeError(w, err, "change the permission mode")
		return
	}
	writeJSON(w, http.StatusOK, sess.Info())
}

func (s *Server) handleClaudeInterrupt(w http.ResponseWriter, r *http.Request) {
	sess, err := s.Claude.Get(r.PathValue("sid"))
	if err != nil {
		s.writeClaudeError(w, err, "interrupt Claude")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), claudeControlTimeout)
	defer cancel()
	if err := sess.Interrupt(ctx); err != nil {
		s.writeClaudeError(w, err, "interrupt Claude")
		return
	}
	writeJSON(w, http.StatusOK, sess.Info())
}

// handleClaudeStream streams a session's history as Server-Sent Events from
// the offset the client asks for. Every batch of entries is followed by the
// session as it now stands, so the screen's idea of busy, pending and cost
// never lags what it has drawn.
func (s *Server) handleClaudeStream(w http.ResponseWriter, r *http.Request) {
	sess, err := s.Claude.Get(r.PathValue("sid"))
	if err != nil {
		s.writeClaudeError(w, err, "watch the Claude session")
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

	detach := sess.Attach()
	defer detach()

	fmt.Fprintf(w, "event: session\ndata: %s\n\n", mustJSON(sess.Info()))
	flusher.Flush()

	for {
		ctx, cancel := context.WithTimeout(r.Context(), claudeHeartbeat)
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
		if len(chunk.Entries) > 0 {
			payload := map[string]any{
				"from":    chunk.From,
				"next":    chunk.Next,
				"entries": chunk.Entries,
			}
			fmt.Fprintf(w, "event: entries\ndata: %s\n\n", mustJSON(payload))
			fmt.Fprintf(w, "event: session\ndata: %s\n\n", mustJSON(sess.Info()))
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

// writeClaudeError maps a session failure onto a status code.
func (s *Server) writeClaudeError(w http.ResponseWriter, err error, action string) {
	switch {
	case errors.Is(err, claude.ErrNotFound):
		writeError(w, http.StatusNotFound, err.Error())
	case errors.Is(err, claude.ErrClosed), errors.Is(err, claude.ErrTooMany), errors.Is(err, claude.ErrNoRequest):
		writeError(w, http.StatusConflict, err.Error())
	case errors.Is(err, hostops.ErrInvalid):
		writeError(w, http.StatusBadRequest, err.Error())
	default:
		s.Log.Debug("api: "+action, "err", err)
		writeError(w, http.StatusBadGateway, err.Error())
	}
}
