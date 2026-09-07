package hosts

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/chinmay28/hostman/server/internal/store"
)

// Poll cadence. Hosts are polled slowly in the background and quickly while
// someone is watching them in the UI.
const (
	tickInterval    = 5 * time.Second
	idleInterval    = 30 * time.Second
	watchedInterval = 5 * time.Second
	watchWindow     = 45 * time.Second
	probeTimeout    = 25 * time.Second
	pruneInterval   = time.Hour
	retention       = 24 * time.Hour
)

// Poller samples every host on a schedule and prunes old telemetry.
type Poller struct {
	svc *Service
	db  *store.DB
	log *slog.Logger

	mu       sync.Mutex
	lastPoll map[int64]time.Time
	watched  map[int64]time.Time
	inFlight map[int64]bool
}

// NewPoller creates a poller; call Run to start it.
func NewPoller(svc *Service, db *store.DB, log *slog.Logger) *Poller {
	return &Poller{
		svc:      svc,
		db:       db,
		log:      log,
		lastPoll: map[int64]time.Time{},
		watched:  map[int64]time.Time{},
		inFlight: map[int64]bool{},
	}
}

// Watch marks a host as actively viewed, raising its poll rate for a while.
func (p *Poller) Watch(hostID int64) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.watched[hostID] = time.Now()
}

// Run polls until ctx is cancelled.
func (p *Poller) Run(ctx context.Context) {
	ticker := time.NewTicker(tickInterval)
	defer ticker.Stop()
	pruneTicker := time.NewTicker(pruneInterval)
	defer pruneTicker.Stop()

	p.sweep(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			p.sweep(ctx)
		case <-pruneTicker.C:
			p.prune(ctx)
		}
	}
}

func (p *Poller) sweep(ctx context.Context) {
	hosts, err := p.db.ListHosts(ctx)
	if err != nil {
		p.log.Error("poller: list hosts", "err", err)
		return
	}
	for _, h := range hosts {
		if !p.claim(h.ID) {
			continue
		}
		go func(h *store.Host) {
			defer p.release(h.ID)
			probeCtx, cancel := context.WithTimeout(ctx, probeTimeout)
			defer cancel()
			if _, err := p.svc.Probe(probeCtx, h); err != nil && ctx.Err() == nil {
				p.log.Debug("poller: probe failed", "host", h.Name, "err", err)
			}
		}(h)
	}
}

// claim reports whether the host is due for a poll and reserves it if so.
func (p *Poller) claim(hostID int64) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.inFlight[hostID] {
		return false
	}
	interval := idleInterval
	if seen, ok := p.watched[hostID]; ok {
		if time.Since(seen) <= watchWindow {
			interval = watchedInterval
		} else {
			delete(p.watched, hostID)
		}
	}
	if last, ok := p.lastPoll[hostID]; ok && time.Since(last) < interval {
		return false
	}
	p.lastPoll[hostID] = time.Now()
	p.inFlight[hostID] = true
	return true
}

func (p *Poller) release(hostID int64) {
	p.mu.Lock()
	defer p.mu.Unlock()
	delete(p.inFlight, hostID)
}

func (p *Poller) prune(ctx context.Context) {
	n, err := p.db.PruneSamples(ctx, time.Now().Add(-retention))
	if err != nil {
		p.log.Error("poller: prune samples", "err", err)
		return
	}
	if n > 0 {
		p.log.Debug("poller: pruned samples", "rows", n)
	}
}
