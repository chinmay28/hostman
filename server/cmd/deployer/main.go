// Command deployer runs the HostMan server: REST API plus the PWA, from a
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
	"syscall"
	"time"

	"github.com/chinmay28/deployer/server/internal/api"
	"github.com/chinmay28/deployer/server/internal/claude"
	"github.com/chinmay28/deployer/server/internal/deploy"
	"github.com/chinmay28/deployer/server/internal/hostops"
	"github.com/chinmay28/deployer/server/internal/hosts"
	"github.com/chinmay28/deployer/server/internal/selfhost"
	"github.com/chinmay28/deployer/server/internal/shell"
	"github.com/chinmay28/deployer/server/internal/sshx"
	"github.com/chinmay28/deployer/server/internal/store"
	"github.com/chinmay28/deployer/server/internal/version"
	"github.com/chinmay28/deployer/server/internal/web"
)

func main() {
	if err := run(); err != nil {
		slog.Error("fatal", "err", err)
		os.Exit(1)
	}
}

func run() error {
	var (
		addr    = flag.String("addr", envOr("DEPLOYER_ADDR", ":8899"), "listen address (host:port)")
		dbPath  = flag.String("db", envOr("DEPLOYER_DB", "data/deployer.db"), "path to the SQLite database")
		pin     = flag.String("pin", os.Getenv("DEPLOYER_PIN"), "optional PIN required to use the UI; empty disables authentication")
		sshUser = flag.String("self-user", os.Getenv("DEPLOYER_SELF_USER"), "SSH user HostMan connects as on its own machine")
		repo    = flag.String("self-repo", envOr("DEPLOYER_REPO", "chinmay28/hostman"), "repository a self-update builds from")
		ref     = flag.String("self-ref", envOr("DEPLOYER_REF", "main"), "git ref a self-update builds from by default")
		verbose = flag.Bool("v", false, "verbose logging")
	)
	flag.Parse()

	level := slog.LevelInfo
	if *verbose {
		level = slog.LevelDebug
	}
	log := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: level}))
	slog.SetDefault(log)

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
		SSHUser: *sshUser,
		Port:    portOf(*addr),
		Repo:    *repo,
		Ref:     *ref,
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
	runner := deploy.NewRunner(db, hostSvc, health, log)

	auth := api.NewPinAuth(*pin)
	// Deployments that were left running are either resumable (they outlive a
	// restart on purpose) or genuinely interrupted.
	runner.ResumeDetached(ctx)

	apiSrv := &api.Server{
		DB: db, Hosts: hostSvc, Poller: poller,
		Runner: runner, Health: health, Ops: hostops.NewService(hostSvc), Shells: shells, Claude: claudeSessions,
		Log: log, Auth: auth,
		Self: self, Version: appVersion(), SelfRef: *ref,
	}

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

	errCh := make(chan error, 1)
	go func() {
		log.Info("deployer listening", "version", apiSrv.Version, "addr", *addr, "db", *dbPath, "auth", authMode(auth))
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

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
