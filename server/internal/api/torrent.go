package api

import (
	"context"
	"net/http"

	"github.com/chinmay28/deployer/server/internal/hostops"
)

// The downloader is deluge running on the host, driven from a phone. The
// endpoints are the four things anybody does with one: ask what it is doing,
// hand it a torrent, act on a torrent, and set the daemon up or take it away.
//
// Adding a torrent is the one request here that carries bulk — a .torrent file
// picked on a phone, base64 in the body — so it gets a body limit of its own.
// Everything else is a handful of fields.

// maxTorrentBody bounds an add. It is the largest .torrent file HostMan will
// carry, with room for base64's third and for the fields around it.
const maxTorrentBody = int64(hostops.MaxTorrentFileBytes)*2 + (1 << 16)

// handleTorrents reports on a host's downloader: whether deluge is installed,
// what HostMan has set up, whether the daemon is running, and what it is
// working on.
//
// Nothing here writes, which matters because a screen watching a download is
// the second place in HostMan that polls a host on a timer rather than on a
// tap — a progress bar that only moved when you asked it to would not be a
// progress bar.
func (s *Server) handleTorrents(w http.ResponseWriter, r *http.Request) {
	h, err := s.hostFromPath(r)
	if err != nil {
		s.writeStoreError(w, err, "read the downloader")
		return
	}
	ctx, cancel := opContext(r)
	defer cancel()

	daemon, err := s.Ops.TorrentStatus(ctx, h)
	if err != nil {
		s.writeOpError(w, err, "read the downloader")
		return
	}
	writeJSON(w, http.StatusOK, daemon)
}

// handleTorrentAdd starts one torrent downloading: a magnet link, an address of
// a .torrent file for the host to fetch itself, or the file's own bytes.
//
// It answers with the whole downloader rather than with the torrent that was
// added, because that is what the screen shows next and a magnet link has no
// name to report yet anyway — deluge only learns it once the metadata arrives.
func (s *Server) handleTorrentAdd(w http.ResponseWriter, r *http.Request) {
	h, err := s.hostFromPath(r)
	if err != nil {
		s.writeStoreError(w, err, "add a torrent")
		return
	}
	var in hostops.TorrentAdd
	if err := decodeJSONLimit(r, &in, maxTorrentBody); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	// A stopped daemon is started rather than refused, and systemd's own
	// patience is what that takes.
	ctx, cancel := context.WithTimeout(r.Context(), unitTimeout)
	defer cancel()

	daemon, err := s.Ops.AddTorrent(ctx, h, in)
	if err != nil {
		s.writeOpError(w, err, "add a torrent")
		return
	}
	s.Log.Info("api: added a torrent", "host", h.Name, "torrents", len(daemon.Torrents))
	writeJSON(w, http.StatusOK, daemon)
}

type torrentActionInput struct {
	// Action is "start" or "stop" for the daemon, "pause", "resume" or "remove"
	// for one torrent, and "seeding" or "limit" for the rules that apply to all
	// of them.
	Action string `json:"action"`
	// ID names the torrent, for the actions that act on one.
	ID string `json:"id"`
	// Data asks for the downloaded files to go too, on a remove. It is the one
	// thing on this screen that cannot be undone, so it is never assumed.
	Data bool `json:"data"`
	// Ratio and Remove are the seeding rule: how much a torrent uploads before
	// deluge stops seeding it, and whether its entry then goes from the list.
	Ratio  float64 `json:"ratio"`
	Remove bool    `json:"remove"`
	// Limit is how many torrents deluge works on at once, for the action that
	// sets it. -1 is no limit at all.
	Limit int `json:"limit"`
}

// handleTorrentAction runs the daemon, or acts on one torrent. Both live here
// because from a phone they are the same gesture — a button on a card — and
// splitting them would only mean two endpoints that answer with the same thing.
func (s *Server) handleTorrentAction(w http.ResponseWriter, r *http.Request) {
	h, err := s.hostFromPath(r)
	if err != nil {
		s.writeStoreError(w, err, "the downloader")
		return
	}
	var in torrentActionInput
	if err := decodeJSON(r, &in); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	// Starting and stopping wait for systemd, so they get systemd's patience.
	ctx, cancel := context.WithTimeout(r.Context(), unitTimeout)
	defer cancel()

	var daemon *hostops.TorrentDaemon
	switch in.Action {
	case "start":
		daemon, err = s.Ops.StartTorrent(ctx, h)
	case "stop":
		daemon, err = s.Ops.StopTorrent(ctx, h)
	case "pause", "resume", "remove":
		daemon, err = s.Ops.TorrentAction(ctx, h, in.ID, in.Action, in.Data)
	case "seeding":
		daemon, err = s.Ops.SetTorrentSeeding(ctx, h,
			hostops.TorrentSeeding{Ratio: in.Ratio, Remove: in.Remove})
	case "limit":
		daemon, err = s.Ops.SetTorrentActiveLimit(ctx, h, in.Limit)
	default:
		writeError(w, http.StatusBadRequest,
			"the downloader can be started or stopped, a torrent paused, resumed or removed, and the seeding rule or torrent limit set")
		return
	}
	if err != nil {
		s.writeOpError(w, err, "the downloader: "+in.Action)
		return
	}
	s.Log.Info("api: downloader", "host", h.Name, "action", in.Action, "withData", in.Data)
	writeJSON(w, http.StatusOK, daemon)
}

// handleTorrentSetup writes the downloader onto a host, or rewrites the one
// already there. It is idempotent, and it keeps the daemon's password and
// everything already downloading: changing the folder should not restart a
// download from the beginning.
func (s *Server) handleTorrentSetup(w http.ResponseWriter, r *http.Request) {
	h, err := s.hostFromPath(r)
	if err != nil {
		s.writeStoreError(w, err, "set up the downloader")
		return
	}
	var in hostops.TorrentSetup
	if r.ContentLength > 0 {
		if err := decodeJSON(r, &in); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
	}
	ctx, cancel := opContext(r)
	defer cancel()

	daemon, err := s.Ops.SetupTorrent(ctx, h, in)
	if err != nil {
		s.writeOpError(w, err, "set up the downloader")
		return
	}
	s.Log.Info("api: set up the downloader", "host", h.Name, "downloads", daemon.Downloads)
	writeJSON(w, http.StatusOK, daemon)
}

// handleTorrentRemove takes the downloader off the host: the service, and
// deluge's own state with it. Deluge stays installed, and every file already
// downloaded stays exactly where it is.
func (s *Server) handleTorrentRemove(w http.ResponseWriter, r *http.Request) {
	h, err := s.hostFromPath(r)
	if err != nil {
		s.writeStoreError(w, err, "remove the downloader")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), unitTimeout)
	defer cancel()

	if err := s.Ops.RemoveTorrent(ctx, h); err != nil {
		s.writeOpError(w, err, "remove the downloader")
		return
	}
	s.Log.Info("api: removed the downloader", "host", h.Name)
	w.WriteHeader(http.StatusNoContent)
}
