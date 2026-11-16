package hostenv

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"sync/atomic"
	"testing"
	"time"
)

func TestParseServers(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{"", nil},
		{"1.1.1.1", []string{"1.1.1.1:53"}},
		{"1.1.1.1, 8.8.8.8:5353", []string{"1.1.1.1:53", "8.8.8.8:5353"}},
		{"2606:4700::1111", []string{"[2606:4700::1111]:53"}},
		{"[2606:4700::1111]:853", []string{"[2606:4700::1111]:853"}},
		{"[2606:4700::1111]", []string{"[2606:4700::1111]:53"}},
		{"::ffff:10.0.0.1", []string{"10.0.0.1:53"}},
		{"# the phone's servers\n192.168.1.1  # router\n\n10.0.0.1\r\n", []string{"192.168.1.1:53", "10.0.0.1:53"}},
		{"1.1.1.1 1.1.1.1:53", []string{"1.1.1.1:53"}},
		{"dns.google, 1.1.1.1:0, fe80::1%wlan0, 9.9.9.9", []string{"9.9.9.9:53"}},
	}
	for _, c := range cases {
		if got := ParseServers(c.in); !reflect.DeepEqual(got, c.want) {
			t.Errorf("ParseServers(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestFileServersFollowsTheFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "dns")
	now := time.Unix(1000, 0)
	src := newFileSource(path, StaticServers("9.9.9.9"), time.Second, func() time.Time { return now })

	if got := src.servers(); !reflect.DeepEqual(got, []string{"9.9.9.9:53"}) {
		t.Fatalf("missing file: got %q, want the fallback", got)
	}

	write := func(text string, mtime time.Time) {
		t.Helper()
		if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(path, mtime, mtime); err != nil {
			t.Fatal(err)
		}
	}

	write("192.168.1.1\n", time.Unix(2000, 0))
	if got := src.servers(); !reflect.DeepEqual(got, []string{"9.9.9.9:53"}) {
		t.Fatalf("within the recheck interval: got %q, want the old answer", got)
	}
	now = now.Add(time.Second)
	if got := src.servers(); !reflect.DeepEqual(got, []string{"192.168.1.1:53"}) {
		t.Fatalf("after the interval: got %q", got)
	}

	// A new network: the app rewrites the file.
	write("10.0.0.1, 10.0.0.2\n", time.Unix(3000, 0))
	now = now.Add(time.Second)
	if got := src.servers(); !reflect.DeepEqual(got, []string{"10.0.0.1:53", "10.0.0.2:53"}) {
		t.Fatalf("after a rewrite: got %q", got)
	}

	// No network: the file names nothing, so the fallback answers.
	write("", time.Unix(4000, 0))
	now = now.Add(time.Second)
	if got := src.servers(); !reflect.DeepEqual(got, []string{"9.9.9.9:53"}) {
		t.Fatalf("empty file: got %q, want the fallback", got)
	}

	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	now = now.Add(time.Second)
	if got := src.servers(); !reflect.DeepEqual(got, []string{"9.9.9.9:53"}) {
		t.Fatalf("removed file: got %q, want the fallback", got)
	}
}

func TestFileServersWithNoFallback(t *testing.T) {
	src := FileServers(filepath.Join(t.TempDir(), "absent"), nil)
	if got := src(); len(got) != 0 {
		t.Fatalf("got %q, want nothing", got)
	}
}

// fakeDNS answers every A question with ip, and counts what it was asked.
// fakeDNS answers every A question with ip, and counts what it was asked. It
// is a plain DNS server and a stand-in for an mDNS responder alike: a one-shot
// mDNS query is answered the same way, by unicast to the asker.
func fakeDNS(t *testing.T, ip [4]byte) (addr string, asked *atomic.Int32) {
	t.Helper()
	conn, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	asked = new(atomic.Int32)

	go func() {
		buf := make([]byte, 1500)
		for {
			n, from, err := conn.ReadFrom(buf)
			if err != nil {
				return
			}
			if reply := answer(buf[:n], ip); reply != nil {
				asked.Add(1)
				conn.WriteTo(reply, from)
			}
		}
	}()
	return conn.LocalAddr().String(), asked
}

// answer builds the reply to a one-question query: the question echoed, and
// for an A question one A record pointing at ip.
func answer(query []byte, ip [4]byte) []byte {
	name := questionName(query)
	if name == "" {
		return nil
	}
	end := 12 + len(name) + 1 // the labels and the root byte
	if name == "." {
		end = 13
	}
	if len(query) < end+4 {
		return nil
	}
	qtype := int(query[end])<<8 | int(query[end+1])
	out := append([]byte{}, query[:2]...)     // id
	out = append(out, 0x84, 0x00, 0x00, 0x01) // response, authoritative; one question
	if qtype == 1 {
		out = append(out, 0x00, 0x01) // one answer
	} else {
		out = append(out, 0x00, 0x00)
	}
	out = append(out, 0, 0, 0, 0)         // no authority, no additional
	out = append(out, query[12:end+4]...) // the question
	if qtype == 1 {
		out = append(out, 0xc0, 0x0c, 0, 1, 0, 1, 0, 0, 0, 60, 0, 4)
		out = append(out, ip[:]...)
	}
	return out
}

func lookup(t *testing.T, r *net.Resolver, name string) []string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ips, err := r.LookupHost(ctx, name)
	if err != nil {
		t.Fatalf("LookupHost(%q): %v", name, err)
	}
	return ips
}

func TestResolverAsksTheConfiguredServer(t *testing.T) {
	addr, asked := fakeDNS(t, [4]byte{192, 0, 2, 7})
	r := NewResolver(StaticServers(addr))

	if ips := lookup(t, r, "hosts.example.test."); !reflect.DeepEqual(ips, []string{"192.0.2.7"}) {
		t.Fatalf("got %q", ips)
	}
	if asked.Load() == 0 {
		t.Fatal("the fake server was never asked")
	}
}

func TestResolverTakesTheServersInTurn(t *testing.T) {
	a, askedA := fakeDNS(t, [4]byte{192, 0, 2, 1})
	b, askedB := fakeDNS(t, [4]byte{192, 0, 2, 2})
	r := NewResolver(StaticServers(a + "," + b))

	for i := 0; i < 4; i++ {
		lookup(t, r, "hosts.example.test.")
	}
	if askedA.Load() == 0 || askedB.Load() == 0 {
		t.Fatalf("queries went to one server only: a=%d b=%d", askedA.Load(), askedB.Load())
	}
}

func TestResolverWithNoServersFails(t *testing.T) {
	r := NewResolver(func() []string { return nil })
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := r.LookupHost(ctx, "hosts.example.test."); err == nil {
		t.Fatal("a lookup with no servers succeeded")
	}
}

// The name a home network is reached by — nakedpi.local — is asked of
// multicast DNS, and never of the network's server, which has never heard of
// it. Every other name still goes to the server.
func TestResolverAsksMulticastDNSForLocalNames(t *testing.T) {
	server, askedServer := fakeDNS(t, [4]byte{192, 0, 2, 1})
	mdns, askedMDNS := fakeDNS(t, [4]byte{192, 168, 2, 123})
	r := newResolver(StaticServers(server), mdns)

	if ips := lookup(t, r, "nakedpi.local."); !reflect.DeepEqual(ips, []string{"192.168.2.123"}) {
		t.Fatalf("nakedpi.local = %q, want the mDNS answer", ips)
	}
	if askedServer.Load() != 0 {
		t.Fatalf("the DNS server was asked %d times about a .local name", askedServer.Load())
	}
	if ips := lookup(t, r, "NakedPi.Local"); !reflect.DeepEqual(ips, []string{"192.168.2.123"}) {
		t.Fatalf("NakedPi.Local = %q, want the mDNS answer", ips)
	}

	before := askedMDNS.Load()
	if ips := lookup(t, r, "hosts.example.test."); !reflect.DeepEqual(ips, []string{"192.0.2.1"}) {
		t.Fatalf("hosts.example.test = %q, want the server's answer", ips)
	}
	if askedMDNS.Load() != before {
		t.Fatal("an ordinary name was asked of mDNS")
	}
}

// The socket is not connected, so it is up to the conn to ignore answers from
// anywhere but the server it asked.
func TestAServerQueryIgnoresAnswersFromElsewhere(t *testing.T) {
	server, _ := fakeDNS(t, [4]byte{192, 0, 2, 1})
	pc, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	c := &queryConn{pc: pc, pick: func() (string, error) { return server, nil }, mdns: MulticastDNS}
	defer c.Close()
	query := []byte{0x12, 0x34, 1, 0, 0, 1, 0, 0, 0, 0, 0, 0, 1, 'x', 4, 't', 'e', 's', 't', 0, 0, 1, 0, 1}
	if _, err := c.Write(query); err != nil {
		t.Fatal(err)
	}

	stranger, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer stranger.Close()
	stranger.WriteTo([]byte("forged"), pc.LocalAddr())

	c.SetReadDeadline(time.Now().Add(5 * time.Second))
	buf := make([]byte, 512)
	n, err := c.Read(buf)
	if err != nil {
		t.Fatal(err)
	}
	if string(buf[:n]) == "forged" || n < 12 || buf[0] != 0x12 || buf[1] != 0x34 {
		t.Fatalf("read %q, want the server's answer", buf[:n])
	}
}

func TestIsLocalName(t *testing.T) {
	for name, want := range map[string]bool{
		"nakedpi.local.":      true,
		"nakedpi.local":       true,
		"a.b.LOCAL.":          true,
		"local.":              false,
		"nakedpi.localdomain": false,
		"example.com.":        false,
		"":                    false,
	} {
		if got := IsLocalName(name); got != want {
			t.Errorf("IsLocalName(%q) = %v, want %v", name, got, want)
		}
	}
}

func TestQuestionName(t *testing.T) {
	header := []byte{0, 1, 1, 0, 0, 1, 0, 0, 0, 0, 0, 0}
	q := func(rest ...byte) []byte { return append(append([]byte{}, header...), rest...) }
	cases := []struct {
		in   []byte
		want string
	}{
		{q(7, 'n', 'a', 'k', 'e', 'd', 'p', 'i', 5, 'l', 'o', 'c', 'a', 'l', 0, 0, 1, 0, 1), "nakedpi.local."},
		{q(0, 0, 1, 0, 1), "."},
		{q(0xc0, 0x0c), ""}, // a pointer: not a query
		{q(9, 'x'), ""},     // runs off the end
		{[]byte{0, 1}, ""},  // no header
		{append([]byte{0, 1, 1, 0, 0, 0}, make([]byte, 6)...), ""}, // no question
	}
	for _, c := range cases {
		if got := questionName(c.in); got != c.want {
			t.Errorf("questionName(% x) = %q, want %q", c.in, got, c.want)
		}
	}
}
