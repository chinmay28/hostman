package api

import (
	"encoding/base64"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/chinmay28/hostman/server/internal/hostops"
)

// What a torrent request carries reaches a daemon on somebody's machine and a
// folder on its disk, so the values that are wrong are refused here — before
// anything connects, and long before anything reaches a command line.
func TestTorrentRequestsAreValidatedBeforeConnecting(t *testing.T) {
	_, h := testServer(t, "")
	id := newHost(t, h)
	torrent := base64.StdEncoding.EncodeToString([]byte("d8:announce20:http://t.example/a4:infod4:name5:thingee"))

	cases := []struct{ name, method, path, body string }{
		{"nothing to download", "POST", "/api/hosts/%d/torrents", `{}`},
		{"a link and a file at once", "POST", "/api/hosts/%d/torrents",
			`{"source":"magnet:?xt=urn:btih:` + strings.Repeat("a", 40) + `","file":"` + torrent + `"}`},
		{"a magnet with no torrent in it", "POST", "/api/hosts/%d/torrents", `{"source":"magnet:?dn=thing"}`},
		{"a link that is a local file", "POST", "/api/hosts/%d/torrents", `{"source":"file:///etc/shadow"}`},
		{"a link that is an option", "POST", "/api/hosts/%d/torrents", `{"source":"--config=/etc/passwd"}`},
		{"a link carrying a command", "POST", "/api/hosts/%d/torrents", `{"source":"https://example.com/a\";reboot"}`},
		{"a file that is not a torrent", "POST", "/api/hosts/%d/torrents",
			`{"file":"` + base64.StdEncoding.EncodeToString([]byte("just a photo")) + `"}`},
		{"a file that did not arrive intact", "POST", "/api/hosts/%d/torrents", `{"file":"!!!not base64!!!"}`},
		{"a folder that is not a path", "POST", "/api/hosts/%d/torrents",
			`{"source":"magnet:?xt=urn:btih:` + strings.Repeat("a", 40) + `","path":"srv/media"}`},
		{"an unknown field", "POST", "/api/hosts/%d/torrents", `{"source":"magnet:?xt=urn:btih:` + strings.Repeat("a", 40) + `","seed":true}`},

		{"a folder that is not absolute", "POST", "/api/hosts/%d/torrents/setup", `{"downloads":"downloads"}`},
		{"downloading into the root", "POST", "/api/hosts/%d/torrents/setup", `{"downloads":"/"}`},
		{"a folder carrying a quote", "POST", "/api/hosts/%d/torrents/setup", `{"downloads":"/srv/\"media\""}`},
		{"an unknown setting", "POST", "/api/hosts/%d/torrents/setup", `{"downloads":"/srv/media","port":58846}`},

		{"an action nothing offers", "POST", "/api/hosts/%d/torrents/action", `{"action":"seed"}`},
		{"no action at all", "POST", "/api/hosts/%d/torrents/action", `{"id":"` + strings.Repeat("a", 40) + `"}`},
		{"pausing nothing in particular", "POST", "/api/hosts/%d/torrents/action", `{"action":"pause"}`},
		{"an id that is not a hash", "POST", "/api/hosts/%d/torrents/action", `{"action":"pause","id":"all"}`},
		{"an id carrying a command", "POST", "/api/hosts/%d/torrents/action",
			`{"action":"remove","id":"` + strings.Repeat("a", 40) + ` ; reboot"}`},

		{"a seeding ratio below anything useful", "POST", "/api/hosts/%d/torrents/action", `{"action":"seeding","ratio":0.01}`},
		{"a seeding ratio nobody meant", "POST", "/api/hosts/%d/torrents/action", `{"action":"seeding","ratio":1000}`},
		{"a negative seeding ratio", "POST", "/api/hosts/%d/torrents/action", `{"action":"seeding","ratio":-2}`},
		{"removing entries with no ratio to reach", "POST", "/api/hosts/%d/torrents/action", `{"action":"seeding","remove":true}`},

		{"a torrent limit of nothing", "POST", "/api/hosts/%d/torrents/action", `{"action":"limit"}`},
		{"a torrent limit below any meaning", "POST", "/api/hosts/%d/torrents/action", `{"action":"limit","limit":-2}`},
		{"a torrent limit nobody meant", "POST", "/api/hosts/%d/torrents/action", `{"action":"limit","limit":100000}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := do(t, h, tc.method, fmt.Sprintf(tc.path, id), tc.body)
			if w.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400 (body %s)", w.Code, w.Body)
			}
			if decode[apiError](t, w).Error == "" {
				t.Error("a 400 should say what was wrong with the request")
			}
		})
	}
}

// A host that does not exist is a 404 whatever is asked about its downloader,
// rather than an attempt to connect to nothing.
func TestTorrentRequestsNeedAHost(t *testing.T) {
	_, h := testServer(t, "")
	for _, tc := range []struct{ method, path, body string }{
		{"GET", "/api/hosts/999/torrents", ""},
		{"POST", "/api/hosts/999/torrents", `{"source":"magnet:?xt=urn:btih:` + strings.Repeat("a", 40) + `"}`},
		{"POST", "/api/hosts/999/torrents/action", `{"action":"stop"}`},
		{"POST", "/api/hosts/999/torrents/setup", `{}`},
		{"DELETE", "/api/hosts/999/torrents/setup", ""},
	} {
		w := do(t, h, tc.method, tc.path, tc.body)
		if w.Code != http.StatusNotFound {
			t.Errorf("%s %s = %d, want 404 (body %s)", tc.method, tc.path, w.Code, w.Body)
		}
	}
}

// A .torrent file is the one request here that carries bulk, so the body limit
// has to be the file's size rather than the ordinary one — and still a limit,
// because a phone can put anything in a field.
func TestTorrentAddCarriesAFile(t *testing.T) {
	if maxTorrentBody <= int64(hostops.MaxTorrentFileBytes) {
		t.Fatalf("the body limit (%d) is smaller than the file it must carry", maxTorrentBody)
	}
	_, h := testServer(t, "")
	id := newHost(t, h)

	// Well past the limit: refused for its size rather than read into memory.
	oversized := strings.Repeat("A", int(maxTorrentBody)+1024)
	w := do(t, h, "POST", fmt.Sprintf("/api/hosts/%d/torrents", id), `{"file":"`+oversized+`"}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (body %s)", w.Code, w.Body)
	}
}
