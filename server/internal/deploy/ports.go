package deploy

import (
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/chinmay28/deployer/server/internal/store"
)

// Nothing ever tells HostMan which ports an app serves on, but an app that
// has been set up properly says so twice over: an HTTP health check names the
// port that answers, and install commands take the port as a parameter. Those
// two are enough to show "on nakedpi · port 8899" beside an app, which is the
// difference between knowing it is up and knowing where to open it. Anything
// less certain than those two is left out rather than guessed at.

// portParam matches parameter names that hold a port: port, PORT, ports,
// http_port, web-port, webPort.
var portParam = regexp.MustCompile(`(?i:(?:^|[_.-])ports?$)|[a-z0-9]Ports?$`)

// maxPort is the highest number a TCP port can be. A parameter matching the
// name but not the range is some other kind of number.
const maxPort = 65535

// InstallationPorts returns the ports an installation answers on, lowest
// first, and nothing at all when neither the health check nor the parameters
// say. It is display information: no connection is made to find out.
func InstallationPorts(in *store.Installation) []int {
	if in == nil {
		return nil
	}

	var ports []int
	seen := map[int]bool{}
	add := func(p int) {
		if p <= 0 || p > maxPort || seen[p] {
			return
		}
		seen[p] = true
		ports = append(ports, p)
	}

	if in.HealthType == store.HealthHTTP {
		// A target that still has placeholders in it after this — {{user}},
		// say — renders to nothing usable, and no port is claimed for it.
		if target, err := RenderTarget(in.HealthTarget, healthValues(in), false); err == nil {
			if p, ok := urlPort(target); ok {
				add(p)
			}
		}
	}

	for _, p := range paramPorts(in) {
		add(p)
	}

	sort.Ints(ports)
	return ports
}

// paramPorts is the ports the installation's own parameters name, lowest
// first — the half of the answer that owes nothing to the health check.
func paramPorts(in *store.Installation) []int {
	var ports []int
	seen := map[int]bool{}
	for name, value := range in.Params {
		if !portParam.MatchString(name) {
			continue
		}
		p, err := strconv.Atoi(strings.TrimSpace(value))
		if err != nil || p <= 0 || p > maxPort || seen[p] {
			continue
		}
		seen[p] = true
		ports = append(ports, p)
	}
	sort.Ints(ports)
	return ports
}

// healthValues fills a health target's placeholders from what the installation
// itself carries. The checker has the host record too and can also substitute
// {{user}}; there is no host to ask here, so a target that uses it renders to
// nothing and claims neither a port nor a link, which is the right way to be
// wrong about it.
func healthValues(in *store.Installation) map[string]string {
	values := map[string]string{}
	for k, v := range in.Params {
		values[k] = v
	}
	values[VarHost] = in.HostAddress
	values[VarHostName] = in.HostName
	return values
}

// urlPort is the port a health check URL dials: the one written into it, or
// the scheme's default when it is left out, because that is the port the app
// is answering on either way.
func urlPort(target string) (int, bool) {
	u, err := url.Parse(strings.TrimSpace(target))
	if err != nil || u.Host == "" {
		return 0, false
	}
	if p := u.Port(); p != "" {
		n, err := strconv.Atoi(p)
		if err != nil {
			return 0, false
		}
		return n, true
	}
	switch strings.ToLower(u.Scheme) {
	case "http":
		return 80, true
	case "https":
		return 443, true
	}
	return 0, false
}
