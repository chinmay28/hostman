package api

import (
	"context"
	"net/http"
	"time"
)

// Ollama is the model server on a host that has it. The endpoints are the
// three things anybody does with one from a phone: ask what is there, pull
// another model, and act on one — drop it, unload it — or on the server.
// Talking to a model is the terminal with `ollama run` typed into it, which
// the shell endpoints already do.
//
// The host's page shows the screen only where the probe found ollama, so the
// fact lives on the host record; these endpoints answer either way, and on a
// host without it the status says so rather than failing.

// ollamaActionTimeout bounds an action. Removing a model is unlinking
// gigabytes, unloading one is freeing them, and starting the server waits for
// it to answer; none is quick on a small host.
const ollamaActionTimeout = 60 * time.Second

// handleOllama reports on ollama: whether it is installed, whether the server
// answers, what is on the disk and in memory, and where a pull got to.
//
// Nothing here writes, which matters because a screen following a pull polls
// the host on a timer rather than on a tap.
func (s *Server) handleOllama(w http.ResponseWriter, r *http.Request) {
	h, err := s.hostFromPath(r)
	if err != nil {
		s.writeStoreError(w, err, "read ollama")
		return
	}
	ctx, cancel := opContext(r)
	defer cancel()

	status, err := s.Ops.OllamaStatus(ctx, h)
	if err != nil {
		s.writeOpError(w, err, "read ollama")
		return
	}
	writeJSON(w, http.StatusOK, status)
}

type ollamaPullInput struct {
	// Model is what to pull, as ollama spells it: llama3.2, llama3.2:3b,
	// hf.co/org/model:Q4_K_M.
	Model string `json:"model"`
}

// handleOllamaPull starts a pull and answers with the status as it is
// afterwards, pull under way. The download itself runs on for minutes after
// this answers: the status's `pull` field is where it is followed.
func (s *Server) handleOllamaPull(w http.ResponseWriter, r *http.Request) {
	h, err := s.hostFromPath(r)
	if err != nil {
		s.writeStoreError(w, err, "pull a model")
		return
	}
	var in ollamaPullInput
	if err := decodeJSON(r, &in); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	ctx, cancel := opContext(r)
	defer cancel()

	status, err := s.Ops.PullOllamaModel(ctx, h, in.Model)
	if err != nil {
		s.writeOpError(w, err, "pull a model")
		return
	}
	s.Log.Info("api: pulling a model", "host", h.Name, "model", in.Model)
	writeJSON(w, http.StatusOK, status)
}

type ollamaActionInput struct {
	// Action is one of hostops.OllamaActions: remove or stop a model, start
	// the server, cancel the pull that is running, or clear the record of
	// the last one.
	Action string `json:"action"`
	// Model names the model for remove and stop; the rest ignore it.
	Model string `json:"model"`
}

// handleOllamaAction runs one verb and answers with the status as it is
// afterwards, which is what the screen shows next.
func (s *Server) handleOllamaAction(w http.ResponseWriter, r *http.Request) {
	h, err := s.hostFromPath(r)
	if err != nil {
		s.writeStoreError(w, err, "ollama")
		return
	}
	var in ollamaActionInput
	if err := decodeJSON(r, &in); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), ollamaActionTimeout)
	defer cancel()

	status, err := s.Ops.OllamaAction(ctx, h, in.Action, in.Model)
	if err != nil {
		s.writeOpError(w, err, "ollama: "+in.Action)
		return
	}
	s.Log.Info("api: ollama", "host", h.Name, "action", in.Action, "model", in.Model)
	writeJSON(w, http.StatusOK, status)
}
