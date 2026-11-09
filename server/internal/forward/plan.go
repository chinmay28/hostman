// Package forward relays a port on HostMan's own machine to an app on one of
// its hosts.
//
// HostMan can always reach the hosts it manages (it deploys to them over SSH),
// but a phone often can't: a VPN, client isolation on the Wi-Fi, a mesh node
// or a separate subnet gets in the way. An app that sets a forward is reachable
// at http://<HostMan's address>:<forward port>/ from anywhere HostMan itself
// is. Each connection is carried over HostMan's SSH connection to the host and
// opened there against 127.0.0.1:<app port>, the way `ssh -L` does, so it
// needs nothing from the host beyond the SSH access HostMan already has.
package forward

import (
	"fmt"
	"sort"
	"strings"

	"github.com/chinmay28/hostman/server/internal/store"
)

// States a forward can be in.
const (
	// StateListening: HostMan accepts connections on the forward port.
	StateListening = "listening"
	// StateWaiting: set up correctly, but there is nothing to forward to yet
	// (the app is not installed anywhere).
	StateWaiting = "waiting"
	// StateError: the forward can't run; Detail says why.
	StateError = "error"
)

// Rule is one forward that should be running.
type Rule struct {
	AppID      int64
	AppName    string
	ListenPort int
	Host       store.Host
	TargetPort int
}

// key identifies everything a running forward depends on: when any of it
// changes, the forward is restarted rather than left relaying to the old place.
func (r Rule) key() string {
	h := r.Host
	return fmt.Sprintf("%d|%d|%s|%d|%s|%s|%d",
		r.ListenPort, h.ID, h.Address, h.Port, h.Username, h.HostKey, r.TargetPort)
}

// Status is what the app's page shows about its forward.
type Status struct {
	AppID      int64  `json:"appId"`
	ListenPort int    `json:"listenPort"`
	TargetPort int    `json:"targetPort"`
	HostID     int64  `json:"hostId,omitempty"`
	HostName   string `json:"hostName,omitempty"`
	State      string `json:"state"`
	Detail     string `json:"detail,omitempty"`
	// Active is the number of connections being relayed right now.
	Active int `json:"active"`
}

// Plan works out which forwards should run. Every app that asks for a forward
// gets either a Rule or a Status explaining why it can't have one.
//
// A forward needs exactly one installation to point at: an app on two hosts
// would make one port mean two places, so that is reported rather than
// guessed at. Two apps can't share a forward port either; the app with the
// lower ID keeps it.
func Plan(apps []*store.App, installs []*store.Installation, hosts []*store.Host) ([]Rule, []Status) {
	byHost := map[int64]*store.Host{}
	for _, h := range hosts {
		byHost[h.ID] = h
	}
	byApp := map[int64][]*store.Installation{}
	for _, in := range installs {
		byApp[in.AppID] = append(byApp[in.AppID], in)
	}

	sorted := append([]*store.App(nil), apps...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].ID < sorted[j].ID })

	var rules []Rule
	var statuses []Status
	portOwner := map[int]string{}
	for _, a := range sorted {
		if a.ForwardPort <= 0 {
			continue
		}
		st := Status{AppID: a.ID, ListenPort: a.ForwardPort, TargetPort: a.ForwardTargetPort, State: StateError}
		if owner, taken := portOwner[a.ForwardPort]; taken {
			st.Detail = fmt.Sprintf("port %d is already forwarded for %s", a.ForwardPort, owner)
			statuses = append(statuses, st)
			continue
		}
		portOwner[a.ForwardPort] = a.Name

		ins := byApp[a.ID]
		switch len(ins) {
		case 0:
			st.State = StateWaiting
			st.Detail = "not installed on any host yet"
			statuses = append(statuses, st)
			continue
		case 1:
		default:
			names := make([]string, 0, len(ins))
			for _, in := range ins {
				names = append(names, in.HostName)
			}
			sort.Strings(names)
			st.Detail = fmt.Sprintf("installed on %d hosts (%s); a forward needs exactly one", len(ins), strings.Join(names, ", "))
			statuses = append(statuses, st)
			continue
		}

		h, ok := byHost[ins[0].HostID]
		if !ok {
			st.Detail = "its host is no longer known to HostMan"
			statuses = append(statuses, st)
			continue
		}
		rules = append(rules, Rule{
			AppID: a.ID, AppName: a.Name, ListenPort: a.ForwardPort,
			Host: *h, TargetPort: a.ForwardTargetPort,
		})
	}
	return rules, statuses
}
