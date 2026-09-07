package deploy

import (
	"net"
	"net/url"
	"strconv"
	"strings"

	"github.com/chinmay28/deployer/server/internal/store"
)

// Knowing an app is healthy is only half of knowing where to open it, and the
// other half is already written down: the health check names the address that
// answers, and a port parameter names the port. InstallationURL turns those
// into the one thing a person actually wants from a phone — a link that opens
// the app. As with ports, nothing here is scanned or guessed: an app that
// declares neither gets no link rather than a broken one.

// InstallationURL returns the address to open an installation at, or "" where
// the app has not said enough for HostMan to know one.
func InstallationURL(in *store.Installation) string {
	if in == nil {
		return ""
	}

	// An HTTP health check is the better source: it is a URL somebody wrote
	// down, scheme and all, and it is known to answer.
	if in.HealthType == store.HealthHTTP {
		if target, err := RenderTarget(in.HealthTarget, healthValues(in), false); err == nil {
			if origin := browsableOrigin(target); origin != "" {
				return origin
			}
		}
	}

	// Otherwise the parameters are all there is, and the lowest port is the one
	// to offer: where an app declares several, the plain HTTP port comes before
	// the alternates far more often than not. A port the health check named is
	// deliberately not reused here — a check that did not give a browsable URL
	// was not describing a web page.
	ports := paramPorts(in)
	if len(ports) == 0 || in.HostAddress == "" {
		return ""
	}
	return origin(schemeForPort(ports[0]), in.HostAddress, ports[0])
}

// browsableOrigin reduces a health check URL to something worth opening:
// scheme, host and port, without the path. /healthz is where an app reports on
// itself, not where it greets a person, and the app's own root is the closest
// thing to a front door HostMan can name without guessing.
func browsableOrigin(target string) string {
	u, err := url.Parse(strings.TrimSpace(target))
	if err != nil {
		return ""
	}
	scheme := strings.ToLower(u.Scheme)
	if scheme != "http" && scheme != "https" {
		return ""
	}
	// A placeholder that rendered to an empty string leaves a URL that parses
	// but names no machine, like "http://:8080/".
	if u.Hostname() == "" {
		return ""
	}
	if port := u.Port(); port != "" {
		n, err := strconv.Atoi(port)
		if err != nil || n <= 0 || n > maxPort {
			return ""
		}
		return origin(scheme, u.Hostname(), n)
	}
	return scheme + "://" + hostForURL(u.Hostname()) + "/"
}

// schemeForPort is https only on the port that means https by definition.
// Anything else — 8443 included — is a convention HostMan would be guessing at.
func schemeForPort(port int) string {
	if port == 443 {
		return "https"
	}
	return "http"
}

// origin assembles scheme, host and port, leaving out the port a browser would
// have used anyway so the link reads like one a person would type.
func origin(scheme, host string, port int) string {
	if (scheme == "http" && port == 80) || (scheme == "https" && port == 443) {
		return scheme + "://" + hostForURL(host) + "/"
	}
	return scheme + "://" + net.JoinHostPort(host, strconv.Itoa(port)) + "/"
}

// hostForURL brackets a bare IPv6 address, which is how one goes in a URL.
func hostForURL(host string) string {
	if strings.Contains(host, ":") && !strings.HasPrefix(host, "[") {
		return "[" + host + "]"
	}
	return host
}
