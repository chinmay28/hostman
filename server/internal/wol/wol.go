// Package wol wakes a machine over the network. A network card that is left
// powered while its machine sleeps — or, on most PCs, while it is off — listens
// for one thing: a "magic packet" carrying its own hardware address sixteen
// times over. Anything on the same network segment can send one.
//
// That last clause is the limit of it. The packet is a broadcast, which a
// router does not pass on, so it wakes only what shares a LAN with the machine
// HostMan runs on. And nothing answers: a packet that was sent is not a machine
// that woke, which only its coming back online can say.
package wol

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
)

// Port is where magic packets are conventionally sent: discard. The card does
// not care — it matches the payload, not the port — but firewalls and packet
// captures expect it.
const Port = 9

// ParseMAC reads a 48-bit Ethernet address in any of the forms net.ParseMAC
// takes. Longer hardware addresses (InfiniBand's) and the all-zero and
// broadcast addresses are refused: none of them names a card that could wake.
func ParseMAC(s string) (net.HardwareAddr, error) {
	mac, err := net.ParseMAC(strings.TrimSpace(s))
	if err != nil {
		return nil, fmt.Errorf("%q is not a hardware address", s)
	}
	if len(mac) != 6 {
		return nil, fmt.Errorf("%q is not a 48-bit Ethernet address", s)
	}
	if bytes.Equal(mac, make([]byte, 6)) || bytes.Equal(mac, bytes.Repeat([]byte{0xff}, 6)) {
		return nil, fmt.Errorf("%q does not name one network card", s)
	}
	return mac, nil
}

// MagicPacket is the payload that wakes the card with the given address: six
// bytes of 0xff, then the address sixteen times. mac must be six bytes long,
// which ParseMAC guarantees.
func MagicPacket(mac net.HardwareAddr) []byte {
	p := make([]byte, 0, 6+16*len(mac))
	p = append(p, bytes.Repeat([]byte{0xff}, 6)...)
	for i := 0; i < 16; i++ {
		p = append(p, mac...)
	}
	return p
}

// Broadcast is the directed broadcast address of an IPv4 network — the last
// address in it — or nil for anything that has none: IPv6, a point-to-point
// /31 or a single-address /32.
func Broadcast(n *net.IPNet) net.IP {
	if n == nil {
		return nil
	}
	ip, mask := n.IP.To4(), n.Mask
	if ip == nil {
		return nil
	}
	if len(mask) == net.IPv6len {
		mask = mask[12:]
	}
	if ones, bits := net.IPMask(mask).Size(); bits != 32 || ones > 30 {
		return nil
	}
	b := make(net.IP, net.IPv4len)
	for i := range b {
		b[i] = ip[i] | ^mask[i]
	}
	return b
}

// Targets is where a magic packet should be sent from a machine with the given
// interface addresses: the limited broadcast address first, which needs no
// knowledge of the network at all, then each attached network's own broadcast
// address, which is what reaches a second network card when the first holds
// the default route. Loopback and duplicates are left out.
func Targets(addrs []net.Addr) []net.IP {
	out := []net.IP{net.IPv4bcast}
	for _, a := range addrs {
		n, ok := a.(*net.IPNet)
		if !ok || n.IP.IsLoopback() {
			continue
		}
		b := Broadcast(n)
		if b == nil {
			continue
		}
		dup := false
		for _, have := range out {
			dup = dup || have.Equal(b)
		}
		if !dup {
			out = append(out, b)
		}
	}
	return out
}

// Sender sends magic packets. The zero value is ready to use.
type Sender struct {
	// Addrs lists this machine's interface addresses; nil means
	// net.InterfaceAddrs. Where that fails — Android does not let an app
	// list its interfaces — the limited broadcast address is still tried.
	Addrs func() ([]net.Addr, error)
	// Port overrides the destination port; zero means Port.
	Port int

	// targets stands in for Targets in tests, which have no LAN to broadcast on.
	targets func([]net.Addr) []net.IP
}

// Wake sends the magic packet for mac to every target, and succeeds if at
// least one send did: a machine with a down interface among several should
// still be able to wake its neighbours. Success means the packet left, not
// that anything woke.
func (s Sender) Wake(ctx context.Context, mac net.HardwareAddr) error {
	if len(mac) != 6 {
		return fmt.Errorf("wake: %q is not a 48-bit Ethernet address", mac.String())
	}
	list := s.Addrs
	if list == nil {
		list = net.InterfaceAddrs
	}
	addrs, _ := list() // an empty list still leaves the limited broadcast
	port := s.Port
	if port == 0 {
		port = Port
	}

	var lc net.ListenConfig
	conn, err := lc.ListenPacket(ctx, "udp4", ":0")
	if err != nil {
		return fmt.Errorf("wake: %w", err)
	}
	defer conn.Close()
	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetWriteDeadline(deadline)
	}

	targets := s.targets
	if targets == nil {
		targets = Targets
	}
	packet := MagicPacket(mac)
	var errs []error
	sent := 0
	for _, ip := range targets(addrs) {
		if _, err := conn.WriteTo(packet, &net.UDPAddr{IP: ip, Port: port}); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", ip, err))
			continue
		}
		sent++
	}
	if sent == 0 {
		return fmt.Errorf("wake: no packet could be sent: %w", errors.Join(errs...))
	}
	return nil
}
