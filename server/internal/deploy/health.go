package deploy

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/chinmay28/deployer/server/internal/hosts"
	"github.com/chinmay28/deployer/server/internal/store"
)

// Health check cadence and bounds.
const (
	healthInterval = 60 * time.Second
	httpTimeout    = 8 * time.Second
	sshTimeout     = 15 * time.Second
	healthWorkers  = 4
)

// Checker answers "is this thing actually running?" after a deployment, and
// keeps answering it on a timer.
type Checker struct {
	db    *store.DB
	hosts *hosts.Service
	log   *slog.Logger
	http  *http.Client
}

// NewChecker builds a Checker.
func NewChecker(db *store.DB, h *hosts.Service, log *slog.Logger) *Checker {
	return &Checker{
		db:    db,
		hosts: h,
		log:   log,
		http:  &http.Client{Timeout: httpTimeout},
	}
}

// Run checks every installation on a timer until ctx is cancelled.
func (c *Checker) Run(ctx context.Context) {
	ticker := time.NewTicker(healthInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			c.CheckAll(ctx)
		}
	}
}

// CheckAll checks every installation, skipping those on unreachable hosts.
func (c *Checker) CheckAll(ctx context.Context) {
	installs, err := c.db.ListInstallations(ctx)
	if err != nil {
		c.log.Error("health: list installations", "err", err)
		return
	}
	hostList, err := c.db.ListHosts(ctx)
	if err != nil {
		c.log.Error("health: list hosts", "err", err)
		return
	}
	online := map[int64]*store.Host{}
	for _, h := range hostList {
		online[h.ID] = h
	}

	work := make(chan *store.Installation)
	var wg sync.WaitGroup
	for i := 0; i < healthWorkers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for in := range work {
				c.checkAndRecord(ctx, in, online[in.HostID])
			}
		}()
	}
	for _, in := range installs {
		select {
		case <-ctx.Done():
			close(work)
			wg.Wait()
			return
		case work <- in:
		}
	}
	close(work)
	wg.Wait()
}

// settleAttempts is how many times a just-deployed app is given to come up
// before a failure is recorded. A service that restarts — HostMan included —
// is briefly unreachable, and calling that unhealthy would be wrong.
const settleAttempts = 12

// CheckSoon checks a freshly deployed app, retrying until it answers or the
// attempts run out, so a slow restart is not reported as a failure.
func (c *Checker) CheckSoon(appID, hostID int64, delay time.Duration) {
	go func() {
		timer := time.NewTimer(delay)
		defer timer.Stop()
		<-timer.C

		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()

		for attempt := 0; attempt < settleAttempts; attempt++ {
			in, err := c.db.FindInstallation(ctx, appID, hostID)
			if err != nil {
				return
			}
			host, err := c.db.GetHost(ctx, hostID)
			if err != nil {
				return
			}
			status, _ := c.checkAndRecord(ctx, in, host)
			if status != store.HealthFailing {
				return
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(healthSettleInterval):
			}
		}
	}()
}

// healthSettleInterval spaces out the attempts above.
const healthSettleInterval = 5 * time.Second

// CheckOne checks a single installation now and records the result.
func (c *Checker) CheckOne(ctx context.Context, in *store.Installation) (string, string) {
	host, err := c.db.GetHost(ctx, in.HostID)
	if err != nil {
		return store.HealthUnknown, "host not found"
	}
	return c.checkAndRecord(ctx, in, host)
}

func (c *Checker) checkAndRecord(ctx context.Context, in *store.Installation, host *store.Host) (string, string) {
	// An app that is being deployed right now is expected to be down for a
	// moment — HostMan updating itself is exactly this case. Recording a
	// failure would only mean showing "Not responding" for something that is
	// mid-restart, so the last known result stands.
	if running, err := c.db.HasRunningDeployment(ctx, in.AppID, in.HostID); err == nil && running {
		return store.HealthUnknown, "a deployment is running"
	}
	status, detail := c.check(ctx, in, host)
	if err := c.db.SetInstallationHealth(ctx, in.ID, status, detail); err != nil && ctx.Err() == nil {
		c.log.Error("health: record result", "installation", in.ID, "err", err)
	}
	return status, detail
}

func (c *Checker) check(ctx context.Context, in *store.Installation, host *store.Host) (status, detail string) {
	if in.HealthType == "" || in.HealthType == store.HealthNone {
		return store.HealthUnchecked, "no health check configured"
	}
	if host == nil {
		return store.HealthUnknown, "host not found"
	}
	if host.Status != store.StatusOnline {
		return store.HealthUnknown, "host is " + host.Status
	}

	values := map[string]string{}
	for k, v := range in.Params {
		values[k] = v
	}
	values[VarHost] = host.Address
	values[VarHostName] = host.Name
	values[VarUser] = host.Username

	switch in.HealthType {
	case store.HealthHTTP:
		return c.checkHTTP(ctx, in.HealthTarget, values)
	case store.HealthSystemd:
		return c.checkSystemd(ctx, host, in.HealthTarget, values)
	default:
		return store.HealthUnknown, "unknown health check type " + in.HealthType
	}
}

func (c *Checker) checkHTTP(ctx context.Context, target string, values map[string]string) (string, string) {
	url, err := RenderTarget(target, values, false)
	if err != nil {
		return store.HealthUnknown, err.Error()
	}
	reqCtx, cancel := context.WithTimeout(ctx, httpTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, url, nil)
	if err != nil {
		return store.HealthUnknown, err.Error()
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return store.HealthFailing, err.Error()
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 200 && resp.StatusCode < 400 {
		return store.HealthPassing, fmt.Sprintf("HTTP %d from %s", resp.StatusCode, url)
	}
	return store.HealthFailing, fmt.Sprintf("HTTP %d from %s", resp.StatusCode, url)
}

func (c *Checker) checkSystemd(ctx context.Context, host *store.Host, target string, values map[string]string) (string, string) {
	unit, err := RenderTarget(target, values, true)
	if err != nil {
		return store.HealthUnknown, err.Error()
	}
	runCtx, cancel := context.WithTimeout(ctx, sshTimeout)
	defer cancel()
	client, err := c.hosts.Connect(runCtx, host)
	if err != nil {
		return store.HealthUnknown, err.Error()
	}
	defer client.Close()

	res, err := client.Run(runCtx, "systemctl is-active -- "+unit)
	if err != nil {
		return store.HealthUnknown, err.Error()
	}
	state := strings.TrimSpace(res.Stdout)
	if state == "" {
		state = strings.TrimSpace(res.Stderr)
	}
	if res.ExitCode == 0 && state == "active" {
		return store.HealthPassing, unit + " is active"
	}
	if state == "" {
		state = fmt.Sprintf("exit status %d", res.ExitCode)
	}
	return store.HealthFailing, unit + " is " + state
}
