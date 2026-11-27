package wol

import (
	"context"
	"net"
	"sync"
	"time"
)

// Repeats is when a wake packet is sent again, counted from the first one.
//
// One packet is enough for a machine that is already down. It is not enough for
// one that is still on its way there: a host told to sleep takes a few seconds
// to go and one told to shut down takes longer, HostMan calls both offline from
// the moment they accept, and a card whose machine is still up lets the packet
// pass. The later packets are for that machine. The spacing widens because
// suspending is quick and shutting down is not.
var Repeats = []time.Duration{5 * time.Second, 15 * time.Second, 30 * time.Second, 60 * time.Second}

// repeatTimeout bounds one repeated send, which has no request to bound it.
const repeatTimeout = 5 * time.Second

// Repeater sends a magic packet and then sends it again on a schedule, one
// schedule per hardware address. The zero value is ready to use, and its
// methods are safe to call from several goroutines.
//
// A repeated packet is as unanswered as the first: nothing here knows whether
// the machine woke, so the schedule runs to its end unless Cancel or Close
// stops it. Whoever sends a machine down on purpose must Cancel its address,
// or a packet still queued from an earlier wake brings it straight back.
type Repeater struct {
	// Sender sends one packet; nil means Sender{}, the real thing.
	Sender interface {
		Wake(ctx context.Context, mac net.HardwareAddr) error
	}
	// After overrides Repeats: offsets from the first packet, in ascending
	// order. Empty but not nil means the packet is sent once.
	After []time.Duration
	// OnRepeat, when set, is told how each repeated send went.
	OnRepeat func(mac net.HardwareAddr, err error)

	mu      sync.Mutex
	pending map[string]*schedule
	closed  bool
	wg      sync.WaitGroup
}

// schedule is the repeats still owed to one address.
type schedule struct{ cancel context.CancelFunc }

// Wake sends the packet for mac now and returns how that went. If it left, the
// repeats are scheduled, replacing any still owed to the same address — a
// second press starts the minute again instead of doubling the packets. If it
// did not, nothing is scheduled: the caller has been told, and will ask again.
// The repeats outlive ctx, which bounds only the first send.
func (r *Repeater) Wake(ctx context.Context, mac net.HardwareAddr) error {
	if err := r.send(ctx, mac); err != nil {
		return err
	}
	after := r.After
	if after == nil {
		after = Repeats
	}
	// The caller may reuse its slice once this returns.
	mac = append(net.HardwareAddr(nil), mac...)

	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed || len(after) == 0 {
		return nil
	}
	key := mac.String()
	if old := r.pending[key]; old != nil {
		old.cancel()
	}
	rctx, cancel := context.WithCancel(context.Background())
	s := &schedule{cancel: cancel}
	if r.pending == nil {
		r.pending = map[string]*schedule{}
	}
	r.pending[key] = s
	r.wg.Add(1)
	go r.run(rctx, s, mac, after)
	return nil
}

// Cancel drops the repeats still owed to mac. It does nothing where there are
// none, and cannot recall a packet already sent.
func (r *Repeater) Cancel(mac net.HardwareAddr) {
	r.mu.Lock()
	defer r.mu.Unlock()
	key := mac.String()
	if s := r.pending[key]; s != nil {
		s.cancel()
		delete(r.pending, key)
	}
}

// Close drops every repeat still owed and waits for the sends under way.
// A Wake after Close still sends its first packet, and schedules nothing.
func (r *Repeater) Close() {
	r.mu.Lock()
	r.closed = true
	for key, s := range r.pending {
		s.cancel()
		delete(r.pending, key)
	}
	r.mu.Unlock()
	r.wg.Wait()
}

func (r *Repeater) run(ctx context.Context, s *schedule, mac net.HardwareAddr, after []time.Duration) {
	defer r.wg.Done()
	defer func() {
		s.cancel()
		r.mu.Lock()
		// Only its own entry: a later Wake may have put another in its place.
		if r.pending[mac.String()] == s {
			delete(r.pending, mac.String())
		}
		r.mu.Unlock()
	}()

	start := time.Now()
	for _, at := range after {
		timer := time.NewTimer(time.Until(start.Add(at)))
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
		sendCtx, cancel := context.WithTimeout(ctx, repeatTimeout)
		err := r.send(sendCtx, mac)
		cancel()
		if ctx.Err() != nil {
			return
		}
		if r.OnRepeat != nil {
			r.OnRepeat(mac, err)
		}
	}
}

func (r *Repeater) send(ctx context.Context, mac net.HardwareAddr) error {
	if r.Sender != nil {
		return r.Sender.Wake(ctx, mac)
	}
	return Sender{}.Wake(ctx, mac)
}
