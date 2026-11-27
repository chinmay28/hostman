// Command hostman runs the HostMan server: REST API plus the PWA, from a
// single binary.
package main

import (
	"context"
	"errors"
	"flag"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"runtime/debug"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/chinmay28/hostman/server/internal/api"
	"github.com/chinmay28/hostman/server/internal/claude"
	"github.com/chinmay28/hostman/server/internal/deploy"
	"github.com/chinmay28/hostman/server/internal/discovery"
	"github.com/chinmay28/hostman/server/internal/forward"
	"github.com/chinmay28/hostman/server/internal/hostenv"
	"github.com/chinmay28/hostman/server/internal/hostops"
	"github.com/chinmay28/hostman/server/internal/hosts"
	"github.com/chinmay28/hostman/server/internal/powersched"
	"github.com/chinmay28/hostman/server/internal/selfhost"
	"github.com/chinmay28/hostman/server/internal/shell"
	"github.com/chinmay28/hostman/server/internal/sshx"
	"github.com/chinmay28/hostman/server/internal/store"
	"github.com/chinmay28/hostman/server/internal/version"
	"github.com/chinmay28/hostman/server/internal/web"
)

func main() {
	if err := run(); err != nil {
		slog.Error("fatal", "err", err)
		os.Exit(1)
	}
}

func run() error {
	var (
		addr    = flag.String("addr", envOr("HOSTMAN_ADDR", ":8899"), "listen address (host:port)")
		dbPath  = flag.String("db", envOr("HOSTMAN_DB", "data/hostman.db"), "path to the SQLite database")
		pin     = flag.String("pin", os.Getenv("HOSTMAN_PIN"), "optional PIN required to use the UI; empty disables authentication")
		sshUser = flag.String("self-user", os.Getenv("HOSTMAN_SELF_USER"), "SSH user HostMan connects as on its own machine")
		repo    = flag.String("self-repo", envOr("HOSTMAN_REPO", "chinmay28/hostman"), "repository a self-update builds from")
		ref     = flag.String("self-ref", envOr("HOSTMAN_REF", "main"), "git ref a self-update builds from by default")
		verbose = flag.Bool("v", false, "verbose logging")

		// For a HostMan that something else installs and runs — the Android
		// app's, on a phone. Each is inert at its default.
		managedBy   = flag.String("managed-by", os.Getenv("HOSTMAN_MANAGED_BY"), "what installs and updates HostMan here instead of HostMan itself; when set, this machine is not registered as a host and self-update is off")
		forwardBind = flag.String("forward-bind", os.Getenv("HOSTMAN_FORWARD_BIND"), "address apps' port forwards listen on; empty is every interface")
		discoveryAt = flag.Int("discovery-port", envInt("HOSTMAN_DISCOVERY_PORT", discovery.DefaultPort), "UDP port that answers the Android app's search of the network; 0 turns it off")
		name        = flag.String("name", os.Getenv("HOSTMAN_NAME"), "what the Android app lists this server as when it searches the network; empty is the hostname")
	)
	flag.Parse()

	level := slog.LevelInfo
	if *verbose {
		level = slog.LevelDebug
	}
	log := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: level}))
	slog.SetDefault(log)

	// DNS and the local zone on a host without /etc/resolv.conf or a zone of
	// its own (Android); a no-op wherever their variables are unset.
	if applied, err := hostenv.Apply(os.Getenv); err != nil {
		log.Warn("host environment", "err", err, "applied", applied)
	} else if len(applied) > 0 {
		log.Info("host environment", "applied", applied)
	}

	db, err := store.Open(*dbPath)
	if err != nil {
		return err
	}
	defer db.Close()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	identity, err := sshx.EnsureIdentity(ctx, db)
	if err != nil {
		return err
	}
	log.Info("ssh identity ready", "fingerprint", identity.Fingerprint())

	self := selfhost.New(db, selfhost.Config{
		SSHUser:   *sshUser,
		Port:      portOf(*addr),
		Repo:      *repo,
		Ref:       *ref,
		ManagedBy: *managedBy,
	}, log)
	self.Ensure(ctx)

	hostSvc := hosts.NewService(db, identity, self)
	poller := hosts.NewPoller(hostSvc, db, log)
	go poller.Run(ctx)

	// An ordinary deployment dies with the process that was watching it, so
	// none should be left claiming to be running. Detached ones are exempt:
	// they keep going on the host and are resumed below.
	if n, err := db.InterruptRunningDeployments(ctx); err != nil {
		return err
	} else if n > 0 {
		log.Warn("marked deployments as interrupted after restart", "count", n)
	}

	// Shells are held open between visits, so they outlive the requests that
	// made them and are reaped on their own clock.
	shells := shell.NewManager(hostSvc, log)
	go shells.Run(ctx)
	// Conversations with Claude Code are held open the same way, on a longer
	// clock.
	claudeSessions := claude.NewManager(hostSvc, log)
	go claudeSessions.Run(ctx)

	health := deploy.NewChecker(db, hostSvc, log)
	go health.Run(ctx)

	// Apps that set a forward are relayed from a port on this machine to the
	// host they are installed on, over SSH, for networks where a phone can
	// reach HostMan but not the app's host.
	forwards := forward.NewManager(forward.DBSource(db), func(ctx context.Context, h *store.Host) (forward.Tunnel, error) {
		return hostSvc.Connect(ctx, h)
	}, log)
	forwards.BindTo(*forwardBind)
	go forwards.Run(ctx)
	runner := deploy.NewRunner(db, hostSvc, health, log)

	auth := api.NewPinAuth(*pin)
	// Deployments that were left running are either resumable (they outlive a
	// restart on purpose) or genuinely interrupted.
	runner.ResumeDetached(ctx)

	apiSrv := &api.Server{
		DB: db, Hosts: hostSvc, Poller: poller,
		Runner: runner, Health: health, Ops: hostops.NewService(hostSvc), Transfers: hostops.NewTransfers(hostSvc, log), Shells: shells, Claude: claudeSessions,
		Log: log, Auth: auth,
		Self: self, Version: appVersion(), SelfRef: *ref, ManagedBy: *managedBy,
		Forwards: forwards, Port: portNumber(*addr),
	}

	// Hosts with a sleep and wake schedule are sent down and brought back as
	// their times come round, by the same means as the buttons on their page.
	schedules := &powersched.Runner{DB: db, Actor: apiSrv.ScheduledPower(), Log: log}
	go schedules.Run(ctx)

	mux := http.NewServeMux()
	mux.Handle("/api/", apiSrv.Routes())
	mux.Handle("/", web.Handler())

	srv := &http.Server{
		Addr:              *addr,
		Handler:           auth.Middleware(mux),
		ReadHeaderTimeout: 10 * time.Second,
		// No WriteTimeout: deployment log streaming holds responses open.
		IdleTimeout: 2 * time.Minute,
	}

	// Answers the Android app's "Find on this network". Losing it costs only
	// that, so a port that is taken is a warning, not a reason to stop.
	if *discoveryAt > 0 {
		announce := discovery.Announcement{Name: serverName(*name), Port: portNumber(*addr), Version: apiSrv.Version}
		go func() {
			err := discovery.Listen(ctx, ":"+strconv.Itoa(*discoveryAt), func() discovery.Announcement { return announce })
			if err != nil {
				log.Warn("network discovery is off", "port", *discoveryAt, "err", err)
			}
		}()
	}

	errCh := make(chan error, 1)
	go func() {
		log.Info("hostman listening", "version", apiSrv.Version, "addr", *addr, "db", *dbPath, "auth", authMode(auth))
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
		log.Info("shutting down")
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	// Stop in-flight deployments first so each one records an outcome instead
	// of being left as "running" in the database.
	runner.Shutdown(shutdownCtx)
	return srv.Shutdown(shutdownCtx)
}

// portNumber is the listen address's port as a number, or 0 if it has none.
func portNumber(addr string) int {
	n, _ := strconv.Atoi(portOf(addr))
	return n
}

func authMode(a *api.PinAuth) string {
	if a == nil {
		return "none"
	}
	return "pin"
}

// portOf pulls the port out of a listen address for the self health check.
func portOf(addr string) string {
	_, port, err := net.SplitHostPort(addr)
	if err != nil || port == "" {
		return "8899"
	}
	return port
}

// appVersion reports the build this binary came from: vYEAR.MONTH.PATCH, where
// the patch number is the repository's commit count that `make build` and the
// installer stamp in (see internal/version).
//
// A build made without that stamp has no commit count to report, so it says so
// — patch 0 — and pins down what it actually is with the revision Go records,
// which is the more useful half of the answer for a build off someone's branch.
func appVersion() string {
	if version.Stamped() {
		return version.String()
	}
	if rev := revision(); rev != "" {
		return version.String() + "+" + rev
	}
	return version.String()
}

// revision is the short commit Go stamped into the binary, empty if it didn't
// (no VCS at build time, or -buildvcs=false).
func revision() string {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return ""
	}
	for _, setting := range info.Settings {
		if setting.Key == "vcs.revision" {
			if len(setting.Value) > 12 {
				return setting.Value[:12]
			}
			return setting.Value
		}
	}
	return ""
}

// serverName is what the network search lists this server as: the name it was
// given, else the machine's short hostname.
func serverName(given string) string {
	if given = strings.TrimSpace(given); given != "" {
		return given
	}
	host, err := os.Hostname()
	if err != nil || host == "" {
		return "HostMan"
	}
	short, _, _ := strings.Cut(host, ".")
	return short
}

// envInt is envOr for a number; a value that is not one is the fallback.
func envInt(key string, fallback int) int {
	if n, err := strconv.Atoi(os.Getenv(key)); err == nil {
		return n
	}
	return fallback
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
