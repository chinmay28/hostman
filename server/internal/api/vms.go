package api

import (
	"context"
	"net/http"
)

// Virtual machines are VirtualBox's, on a host that has it. The endpoints are
// the two things anybody does to a VM from a phone: ask what is there and what
// state it is in, and change that state. Making or deleting a machine is a
// laptop job and is not offered.
//
// The host's page shows the screen only where the probe found VBoxManage, so
// the fact lives on the host record; these endpoints answer either way, and on
// a host without VirtualBox the list says so rather than failing.

// handleVMs lists the SSH user's virtual machines.
func (s *Server) handleVMs(w http.ResponseWriter, r *http.Request) {
	h, err := s.hostFromPath(r)
	if err != nil {
		s.writeStoreError(w, err, "list virtual machines")
		return
	}
	ctx, cancel := opContext(r)
	defer cancel()

	list, err := s.Ops.VMStatus(ctx, h)
	if err != nil {
		s.writeOpError(w, err, "list virtual machines")
		return
	}
	writeJSON(w, http.StatusOK, list)
}

type vmActionInput struct {
	// Action is one of hostops.VMActions: start, stop, poweroff, reset, pause,
	// resume or save.
	Action string `json:"action"`
	// ID is the machine's uuid, as the list gave it.
	ID string `json:"id"`
}

// handleVMAction runs one verb on one machine and answers with the list as it
// is afterwards, which is what the screen shows next.
func (s *Server) handleVMAction(w http.ResponseWriter, r *http.Request) {
	h, err := s.hostFromPath(r)
	if err != nil {
		s.writeStoreError(w, err, "the virtual machine")
		return
	}
	var in vmActionInput
	if err := decodeJSON(r, &in); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	// Saving a machine's state writes its memory to disk, and an ACPI shutdown
	// waits on the guest; neither is quick on a small host, so the actions
	// get the same patience a systemd unit does.
	ctx, cancel := context.WithTimeout(r.Context(), unitTimeout)
	defer cancel()

	list, err := s.Ops.VMAction(ctx, h, in.ID, in.Action)
	if err != nil {
		s.writeOpError(w, err, "the virtual machine: "+in.Action)
		return
	}
	s.Log.Info("api: virtual machine", "host", h.Name, "action", in.Action, "vm", in.ID)
	writeJSON(w, http.StatusOK, list)
}
