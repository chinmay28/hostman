package hostenv

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Servers returns the DNS servers to ask, each as "ip:port". It is called on
// every query, so it must be cheap; an empty answer fails the query.
type Servers func() []string

// MulticastDNS is where a query for a name under .local goes: the mDNS group
// and port (RFC 6762). Asked from an ordinary port, it is a "one-shot" query,
// which a responder such as avahi answers by unicast straight back to the
// asker, with the query's own id and question — exactly what a DNS client
// expects of a server.
const MulticastDNS = "224.0.0.251:5353"

// NewResolver is a pure-Go resolver that sends every query to the servers
// returned by servers, whatever the host's own configuration says, except a
// query for a name under .local, which is asked of multicast DNS.
//
// Go's resolver still decides how to ask — retries, timeouts, falling back
// to TCP for a truncated answer — and hands each attempt to Dial with the
// server it would have used. Dial ignores that address and takes the next of
// ours in turn, so a retry after a timeout goes to a different server.
func NewResolver(servers Servers) *net.Resolver {
	return newResolver(servers, MulticastDNS)
}

func newResolver(servers Servers, mdns string) *net.Resolver {
	var next atomic.Uint64
	pick := func() (string, error) {
		list := servers()
		if len(list) == 0 {
			return "", &net.DNSError{Err: "no DNS servers configured", IsTemporary: true}
		}
		return list[int((next.Add(1)-1)%uint64(len(list)))], nil
	}
	return &net.Resolver{
		PreferGo: true,
		Dial: func(ctx context.Context, network, _ string) (net.Conn, error) {
			if strings.HasPrefix(network, "udp") {
				// Where a UDP query goes depends on the name in it, which
				// only the first Write carries.
				pc, err := net.ListenPacket("udp", ":0")
				if err != nil {
					return nil, err
				}
				return &queryConn{pc: pc, pick: pick, mdns: mdns}, nil
			}
			// TCP is only ever a retry of a truncated unicast answer: mDNS
			// has no TCP, so it goes to a server whatever the name.
			addr, err := pick()
			if err != nil {
				return nil, err
			}
			var d net.Dialer
			return d.DialContext(ctx, network, addr)
		},
	}
}

// queryConn is one UDP query. It is a PacketConn as well as a Conn, which is
// what tells Go's resolver to send unframed packets rather than TCP's
// length-prefixed ones.
type queryConn struct {
	pc   net.PacketConn
	pick func() (string, error)
	mdns string

	mu        sync.Mutex
	dest      *net.UDPAddr
	multicast bool
}

func (c *queryConn) Write(b []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.dest == nil {
		addr := c.mdns
		c.multicast = IsLocalName(questionName(b))
		if !c.multicast {
			var err error
			if addr, err = c.pick(); err != nil {
				return 0, err
			}
		}
		// Always a literal address: nothing here may itself need DNS.
		ap, err := netip.ParseAddrPort(addr)
		if err != nil {
			return 0, err
		}
		c.dest = net.UDPAddrFromAddrPort(ap)
	}
	return c.pc.WriteTo(b, c.dest)
}

// Read takes the next answer. From a server, only that server's: the socket
// is not connected, so this is what keeps an answer from anywhere else out,
// as a connected socket would. A multicast query is answered from whichever
// machine has the name, which cannot be known in advance.
func (c *queryConn) Read(b []byte) (int, error) {
	n, _, err := c.ReadFrom(b)
	return n, err
}

func (c *queryConn) ReadFrom(b []byte) (int, net.Addr, error) {
	c.mu.Lock()
	dest, multicast := c.dest, c.multicast
	c.mu.Unlock()
	if dest == nil {
		return 0, nil, errors.New("hostenv: read before the query was written")
	}
	for {
		n, from, err := c.pc.ReadFrom(b)
		if err != nil {
			return n, from, err
		}
		if multicast || sameAddr(from, dest) {
			return n, from, nil
		}
	}
}

func (c *queryConn) WriteTo(b []byte, _ net.Addr) (int, error) { return c.Write(b) }
func (c *queryConn) Close() error                              { return c.pc.Close() }
func (c *queryConn) LocalAddr() net.Addr                       { return c.pc.LocalAddr() }
func (c *queryConn) SetDeadline(t time.Time) error             { return c.pc.SetDeadline(t) }
func (c *queryConn) SetReadDeadline(t time.Time) error         { return c.pc.SetReadDeadline(t) }
func (c *queryConn) SetWriteDeadline(t time.Time) error        { return c.pc.SetWriteDeadline(t) }

func (c *queryConn) RemoteAddr() net.Addr {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.dest == nil {
		return &net.UDPAddr{}
	}
	return c.dest
}

func sameAddr(a net.Addr, b *net.UDPAddr) bool {
	u, ok := a.(*net.UDPAddr)
	if !ok {
		return false
	}
	return u.Port == b.Port && u.IP.Equal(b.IP)
}

// IsLocalName reports whether a fully qualified name belongs to multicast
// DNS: "nakedpi.local." and anything under it, in any case.
func IsLocalName(name string) bool {
	name = strings.ToLower(strings.TrimSuffix(name, "."))
	return name != "local" && strings.HasSuffix(name, ".local")
}

// questionName is the name in a DNS query's first question, "nakedpi.local."
// with the trailing dot, or "" when b is not a query it can read. A query
// carries its name uncompressed, so a pointer means it is not one.
func questionName(b []byte) string {
	if len(b) < 12 || (int(b[4])<<8|int(b[5])) == 0 {
		return ""
	}
	var name strings.Builder
	for i := 12; i < len(b); {
		n := int(b[i])
		i++
		switch {
		case n == 0:
			if name.Len() == 0 {
				return "."
			}
			return name.String()
		case n > 63 || i+n > len(b):
			return ""
		}
		name.Write(b[i : i+n])
		name.WriteByte('.')
		i += n
	}
	return ""
}

// ParseServers reads a server list: IP addresses, with or without a port,
// separated by commas, spaces or newlines. A '#' starts a comment that runs
// to the end of its line. Anything that is not an address is skipped, and so
// is a repeat. Each address comes back as "ip:port", port 53 when none was
// given; an IPv6 address with a port is written "[2001:db8::1]:53".
func ParseServers(text string) []string {
	var out []string
	seen := map[string]bool{}
	for _, line := range strings.Split(text, "\n") {
		if i := strings.IndexByte(line, '#'); i >= 0 {
			line = line[:i]
		}
		for _, field := range strings.FieldsFunc(line, func(r rune) bool {
			return r == ',' || r == ' ' || r == '\t' || r == '\r'
		}) {
			addr, ok := parseServer(field)
			if ok && !seen[addr] {
				seen[addr] = true
				out = append(out, addr)
			}
		}
	}
	return out
}

func parseServer(s string) (string, bool) {
	if ap, err := netip.ParseAddrPort(s); err == nil {
		if ap.Port() == 0 || ap.Addr().Zone() != "" {
			return "", false
		}
		return netip.AddrPortFrom(ap.Addr().Unmap(), ap.Port()).String(), true
	}
	ip, err := netip.ParseAddr(strings.Trim(s, "[]"))
	if err != nil || ip.Zone() != "" {
		// A zoned address (fe80::1%wlan0) names an interface the server
		// process may not see the same way; Android hands those out only
		// alongside routable ones, so leaving it out loses nothing.
		return "", false
	}
	return netip.AddrPortFrom(ip.Unmap(), 53).String(), true
}

// StaticServers is a fixed list, parsed once.
func StaticServers(list string) Servers {
	parsed := ParseServers(list)
	return func() []string { return parsed }
}

// FileServers reads the list from path, again whenever the file's size or
// modification time changes, and at most once every recheck interval
// otherwise checks nothing. While the file is missing, unreadable or names
// no server, the answer is fallback's.
func FileServers(path string, fallback Servers) Servers {
	return newFileSource(path, fallback, time.Second, time.Now).servers
}

type fileSource struct {
	path     string
	fallback Servers
	recheck  time.Duration
	now      func() time.Time

	mu      sync.Mutex
	checked time.Time
	modTime time.Time
	size    int64
	list    []string
}

func newFileSource(path string, fallback Servers, recheck time.Duration, now func() time.Time) *fileSource {
	if fallback == nil {
		fallback = func() []string { return nil }
	}
	return &fileSource{path: path, fallback: fallback, recheck: recheck, now: now}
}

func (f *fileSource) servers() []string {
	f.mu.Lock()
	defer f.mu.Unlock()

	if now := f.now(); f.checked.IsZero() || now.Sub(f.checked) >= f.recheck {
		f.checked = now
		f.refresh()
	}
	if len(f.list) > 0 {
		return f.list
	}
	return f.fallback()
}

func (f *fileSource) refresh() {
	info, err := os.Stat(f.path)
	if err != nil {
		f.list, f.modTime, f.size = nil, time.Time{}, 0
		return
	}
	if info.ModTime().Equal(f.modTime) && info.Size() == f.size && f.list != nil {
		return
	}
	data, err := os.ReadFile(f.path)
	if err != nil {
		f.list = nil
		return
	}
	f.modTime, f.size = info.ModTime(), info.Size()
	f.list = ParseServers(string(data))
}
