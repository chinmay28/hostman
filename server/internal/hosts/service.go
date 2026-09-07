// Package hosts manages target machines: connecting to them, verifying they
// are usable, and recording what they report.
package hosts

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/chinmay28/deployer/server/internal/metrics"
	"github.com/chinmay28/deployer/server/internal/sshx"
	"github.com/chinmay28/deployer/server/internal/store"
)

// SelfIdentifier recognises the machine HostMan itself is running on.
type SelfIdentifier interface {
	IsSelf(machineID string) bool
}

// Service connects to hosts using HostMan's SSH identity.
type Service struct {
	db       *store.DB
	self     SelfIdentifier
	identity atomic.Pointer[sshx.Identity]

	// procs holds the newest process snapshot per host. Unlike a sample it is
	// not written to the database: "what is using this machine" is a question
	// about now, and keeping a day of answers would cost more rows than the
	// telemetry they sit beside. The price is that a restart forgets them until
	// the next poll, which is seconds away.
	mu    sync.Mutex
	procs map[int64]*metrics.Processes
}

// NewService builds a Service around the given identity. self may be nil, in
// which case no host is ever tagged as the home host.
func NewService(db *store.DB, id *sshx.Identity, self SelfIdentifier) *Service {
	s := &Service{db: db, self: self, procs: map[int64]*metrics.Processes{}}
	s.identity.Store(id)
	return s
}

// Identity returns the SSH identity currently in use.
func (s *Service) Identity() *sshx.Identity { return s.identity.Load() }

// SetIdentity swaps in a rotated keypair for subsequent connections.
func (s *Service) SetIdentity(id *sshx.Identity) { s.identity.Store(id) }

// Processes returns the newest snapshot of what a host is busy with, or nil
// where none has been taken since HostMan started.
func (s *Service) Processes(hostID int64) *metrics.Processes {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.procs[hostID]
}

// Forget drops what is remembered about a host in memory. Called when a host is
// removed, so a later host cannot inherit its snapshot through a reused id.
func (s *Service) Forget(hostID int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.procs, hostID)
}

func (s *Service) rememberProcesses(hostID int64, p *metrics.Processes) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.procs[hostID] = p
}

// Connect dials a host, pinning its key on the first successful connection.
// The caller must Close the returned client.
func (s *Service) Connect(ctx context.Context, h *store.Host) (*sshx.Client, error) {
	client, err := sshx.Dial(ctx, sshx.Target{
		Address: h.Address,
		Port:    h.Port,
		User:    h.Username,
		HostKey: h.HostKey,
	}, s.identity.Load())
	if err != nil {
		return nil, err
	}
	if h.HostKey == "" && client.HostKey != "" {
		if err := s.db.SetHostKey(ctx, h.ID, client.HostKey); err != nil {
			client.Close()
			return nil, fmt.Errorf("pin host key: %w", err)
		}
		h.HostKey = client.HostKey
	}
	return client, nil
}

// Probe connects to a host, collects telemetry, and records the result. On
// failure the host is marked offline (or errored) with the reason.
func (s *Service) Probe(ctx context.Context, h *store.Host) (*metrics.Probe, error) {
	client, err := s.Connect(ctx, h)
	if err != nil {
		s.recordFailure(ctx, h, err)
		return nil, err
	}
	defer client.Close()

	probe, err := metrics.Collect(ctx, client)
	if err != nil {
		s.recordFailure(ctx, h, err)
		return nil, err
	}
	// The machine id is what identifies the home host: the same machine can be
	// reached as 127.0.0.1, as a LAN address or as nakedpi.local, and all three
	// should be recognised.
	if s.self != nil {
		probe.Facts.IsSelf = s.self.IsSelf(probe.Facts.MachineID)
	}
	if err := s.db.MarkHostOnline(ctx, h.ID, probe.Facts); err != nil {
		return nil, err
	}
	probe.Sample.HostID = h.ID
	if err := s.db.InsertSample(ctx, &probe.Sample); err != nil {
		return nil, err
	}
	if probe.Processes != nil {
		s.rememberProcesses(h.ID, probe.Processes)
	}
	return probe, nil
}

func (s *Service) recordFailure(ctx context.Context, h *store.Host, cause error) {
	status := store.StatusOffline
	if errors.Is(cause, sshx.ErrHostKeyChanged) {
		status = store.StatusError
	}
	// Use a detached context: ctx may already be cancelled by whatever failed.
	recCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	_ = s.db.MarkHostFailed(recCtx, h.ID, status, cause.Error())
}

// TestResult reports what a connection check found.
type TestResult struct {
	OK       bool     `json:"ok"`
	Error    string   `json:"error,omitempty"`
	SudoOK   bool     `json:"sudoOk"`
	IsSelf   bool     `json:"isSelf"`
	Hostname string   `json:"hostname,omitempty"`
	OS       string   `json:"os,omitempty"`
	Kernel   string   `json:"kernel,omitempty"`
	Arch     string   `json:"arch,omitempty"`
	Hints    []string `json:"hints,omitempty"`
}

// Test checks that a host is reachable, that HostMan's key is authorized, and
// that passwordless sudo works. Hints tell the user how to fix what is missing.
func (s *Service) Test(ctx context.Context, h *store.Host) *TestResult {
	probe, err := s.Probe(ctx, h)
	if err != nil {
		res := &TestResult{Error: err.Error()}
		switch {
		case errors.Is(err, sshx.ErrHostKeyChanged):
			res.Hints = append(res.Hints,
				"The host's SSH key changed. If you reinstalled it, remove and re-add the host to trust the new key.")
		default:
			res.Hints = append(res.Hints,
				"Set up access with the host's password and HostMan will authorize its own key.",
				fmt.Sprintf("Or add HostMan's public key to ~%s/.ssh/authorized_keys on the host by hand (see Settings).", h.Username),
				"Check the address, port and username, and that the host is powered on and reachable.")
		}
		return res
	}
	res := &TestResult{
		OK:       true,
		SudoOK:   probe.Facts.SudoOK,
		IsSelf:   probe.Facts.IsSelf,
		Hostname: probe.Facts.Hostname,
		OS:       probe.Facts.OS,
		Kernel:   probe.Facts.Kernel,
		Arch:     probe.Facts.Arch,
	}
	if !res.SudoOK {
		res.Hints = append(res.Hints,
			fmt.Sprintf("Passwordless sudo is not enabled for %s. Deploys that need root will fail until it is — set up access with the host's password, or run the command in Settings.", h.Username))
	}
	return res
}
