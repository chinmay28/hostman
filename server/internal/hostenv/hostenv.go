// Package hostenv fits the process to a host whose userland is not the one
// the standard library expects of Linux.
//
// Android is the host it is for. The HostMan phone app can run the hostman
// server on the phone itself (apps/android, "full stack"), as a static Go
// binary in the app's sandbox, and two things a Linux distribution provides
// are missing there:
//
//   - /etc/resolv.conf. Without it Go's resolver asks 127.0.0.1:53, where
//     nothing answers, and every host added by name is "unreachable". The
//     phone knows its DNS servers — they change with every network it joins —
//     so the app writes them to a file and names it in HOSTMAN_DNS_FILE; the
//     resolver re-reads it whenever it changes. HOSTMAN_DNS names servers
//     directly, for a host whose servers never change.
//   - The local time zone. Go on Android starts in UTC, so every time HostMan
//     shows or logs would be off by the phone's offset. HOSTMAN_TZ names the
//     zone (the app passes the phone's, e.g. "Europe/Berlin").
//
// The resolver it installs also answers the names a home network uses most:
// `nakedpi.local` and the like. On a Linux machine those come from avahi
// through the C library, which a static Go binary never asks; here a name
// under .local is asked of multicast DNS (RFC 6762) as a one-shot query, and
// everything else of the configured servers.
//
// Nothing here does anything unless its variable is set, so on an ordinary
// machine Apply is a no-op.
package hostenv

import (
	"fmt"
	"net"
	"strings"
	"time"
)

// The variables Apply reads.
const (
	// EnvDNS is a list of DNS servers: IP addresses, optionally with a port,
	// separated by commas or spaces. Port 53 when none is given.
	EnvDNS = "HOSTMAN_DNS"

	// EnvDNSFile is a file holding the same list, one or more per line, which
	// is re-read whenever it changes. When both are set, the file wins and
	// HOSTMAN_DNS is what is used while the file is missing or empty.
	EnvDNSFile = "HOSTMAN_DNS_FILE"

	// EnvTZ is an IANA time zone name.
	EnvTZ = "HOSTMAN_TZ"
)

// Apply reads the variables above through getenv (os.Getenv, outside tests)
// and installs what they name: a resolver as net.DefaultResolver, and a
// zone as time.Local. It returns what it changed, for a log line, and an
// error only for a variable it could not use — in which case everything it
// could use is still applied.
func Apply(getenv func(string) string) (applied []string, err error) {
	var problems []string

	if servers := dnsSource(getenv); servers != nil {
		net.DefaultResolver = NewResolver(servers)
		applied = append(applied, "dns")
	}

	if name := strings.TrimSpace(getenv(EnvTZ)); name != "" {
		if loc, lerr := time.LoadLocation(name); lerr != nil {
			problems = append(problems, fmt.Sprintf("%s=%q: %v", EnvTZ, name, lerr))
		} else {
			time.Local = loc
			applied = append(applied, "tz")
		}
	}

	if len(problems) > 0 {
		err = fmt.Errorf("hostenv: %s", strings.Join(problems, "; "))
	}
	return applied, err
}

// dnsSource is the server list the environment names, or nil for none.
func dnsSource(getenv func(string) string) Servers {
	static := strings.TrimSpace(getenv(EnvDNS))
	file := strings.TrimSpace(getenv(EnvDNSFile))
	switch {
	case file != "":
		return FileServers(file, StaticServers(static))
	case static != "":
		return StaticServers(static)
	default:
		return nil
	}
}
