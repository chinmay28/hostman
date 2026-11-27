package wol

import (
	"bytes"
	"context"
	"errors"
	"net"
	"testing"
	"time"
)

func TestParseMAC(t *testing.T) {
	for _, s := range []string{"a8:a1:59:0c:1f:2e", "A8-A1-59-0C-1F-2E", " a8:a1:59:0c:1f:2e\n"} {
		mac, err := ParseMAC(s)
		if err != nil || mac.String() != "a8:a1:59:0c:1f:2e" {
			t.Errorf("ParseMAC(%q) = %v, %v; want a8:a1:59:0c:1f:2e", s, mac, err)
		}
	}
	for _, s := range []string{
		"", "nakedpi", "a8:a1:59:0c:1f", "00:00:00:00:00:00", "ff:ff:ff:ff:ff:ff",
		"00:00:00:00:fe:80:00:00:00:00:00:00:02:00:5e:10:00:00:00:01", // InfiniBand
	} {
		if mac, err := ParseMAC(s); err == nil {
			t.Errorf("ParseMAC(%q) = %v, accepted; want it refused", s, mac)
		}
	}
}

func TestMagicPacket(t *testing.T) {
	mac, _ := ParseMAC("a8:a1:59:0c:1f:2e")
	p := MagicPacket(mac)
	if len(p) != 102 {
		t.Fatalf("len = %d, want 102", len(p))
	}
	if !bytes.Equal(p[:6], []byte{0xff, 0xff, 0xff, 0xff, 0xff, 0xff}) {
		t.Errorf("header = % x, want six 0xff", p[:6])
	}
	for i := 0; i < 16; i++ {
		if got := p[6+i*6 : 12+i*6]; !bytes.Equal(got, mac) {
			t.Errorf("repetition %d = % x, want the address", i, got)
		}
	}
}

func cidr(t *testing.T, s string) *net.IPNet {
	t.Helper()
	ip, n, err := net.ParseCIDR(s)
	if err != nil {
		t.Fatal(err)
	}
	n.IP = ip // an interface address keeps its host part
	return n
}

func TestBroadcast(t *testing.T) {
	cases := map[string]string{
		"192.168.1.20/24": "192.168.1.255",
		"10.1.2.3/8":      "10.255.255.255",
		"172.16.5.9/20":   "172.16.15.255",
		"192.168.1.6/30":  "192.168.1.7",
		"192.168.1.6/31":  "", // point-to-point: no broadcast
		"192.168.1.6/32":  "",
		"fe80::1/64":      "",
	}
	for in, want := range cases {
		got := Broadcast(cidr(t, in))
		if (got == nil) != (want == "") || (got != nil && got.String() != want) {
			t.Errorf("Broadcast(%s) = %v, want %q", in, got, want)
		}
	}
	if Broadcast(nil) != nil {
		t.Error("Broadcast(nil) should be nil")
	}
}

func TestTargets(t *testing.T) {
	addrs := []net.Addr{
		cidr(t, "127.0.0.1/8"),
		cidr(t, "192.168.1.20/24"),
		cidr(t, "192.168.1.21/24"), // a second address on the same network
		cidr(t, "fe80::1/64"),
		cidr(t, "10.8.0.2/32"),
		&net.IPAddr{IP: net.ParseIP("10.0.0.1")}, // not a network at all
	}
	got := Targets(addrs)
	want := []string{"255.255.255.255", "192.168.1.255"}
	if len(got) != len(want) {
		t.Fatalf("Targets = %v, want %v", got, want)
	}
	for i := range want {
		if got[i].String() != want[i] {
			t.Errorf("Targets[%d] = %v, want %s", i, got[i], want[i])
		}
	}

	// Knowing nothing about the network still leaves somewhere to send.
	if got := Targets(nil); len(got) != 1 || !got[0].Equal(net.IPv4bcast) {
		t.Errorf("Targets(nil) = %v, want only the limited broadcast", got)
	}
}

// What leaves the socket is the magic packet, on the port asked for, and not
// being allowed to list interfaces — Android's rule — does not stop it. A
// loopback listener stands in for the LAN, which a test has none of.
func TestWakeSendsTheMagicPacket(t *testing.T) {
	listener, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Skipf("no loopback UDP here: %v", err)
	}
	defer listener.Close()

	mac, _ := ParseMAC("a8:a1:59:0c:1f:2e")
	s := Sender{
		Port:    listener.LocalAddr().(*net.UDPAddr).Port,
		Addrs:   func() ([]net.Addr, error) { return nil, errors.New("not allowed") },
		targets: func([]net.Addr) []net.IP { return []net.IP{net.IPv4(127, 0, 0, 1)} },
	}
	if err := s.Wake(context.Background(), mac); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 200)
	_ = listener.SetReadDeadline(time.Now().Add(2 * time.Second))
	n, _, err := listener.ReadFrom(buf)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(buf[:n], MagicPacket(mac)) {
		t.Errorf("received % x, want the magic packet", buf[:n])
	}
}

// One target that cannot be sent to does not cost the others; none at all is
// an error, since then nothing left the machine.
func TestWakeNeedsOnlyOneSendToSucceed(t *testing.T) {
	listener, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Skipf("no loopback UDP here: %v", err)
	}
	defer listener.Close()
	mac, _ := ParseMAC("a8:a1:59:0c:1f:2e")
	bad := net.ParseIP("fe80::1") // a udp4 socket cannot send here
	s := Sender{Port: listener.LocalAddr().(*net.UDPAddr).Port}

	s.targets = func([]net.Addr) []net.IP { return []net.IP{bad, net.IPv4(127, 0, 0, 1)} }
	if err := s.Wake(context.Background(), mac); err != nil {
		t.Errorf("one good target of two: %v", err)
	}
	s.targets = func([]net.Addr) []net.IP { return []net.IP{bad} }
	if err := s.Wake(context.Background(), mac); err == nil {
		t.Error("no target could be sent to, and Wake said nothing")
	}
}

func TestWakeRefusesAnAddressThatIsNotEthernet(t *testing.T) {
	if err := (Sender{}).Wake(context.Background(), net.HardwareAddr{1, 2, 3}); err == nil {
		t.Fatal("a three-byte address was accepted")
	}
}
