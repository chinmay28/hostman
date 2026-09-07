package deploy

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/chinmay28/deployer/server/internal/store"
)

// Updating HostMan restarts deployer.service, which kills the process running
// the deployment and, with it, the SSH session carrying the install script —
// sshd hangs up the remote command when its client disappears. So the script is
// started detached, writing to a file on the host, and HostMan follows that
// file. After the restart the new process picks the same file back up and
// finishes the record.

// detachedLogPath is where a detached deployment writes on the host.
func detachedLogPath(deploymentID int64) string {
	return "/tmp/deployer-selfupdate-" + strconv.FormatInt(deploymentID, 10) + ".log"
}

// exitMarker is how the detached script reports its exit status through a file.
const exitMarker = "__deployer_exit__:"

// pollInterval is how often a resumed follower re-reads the log if `tail -f`
// is unavailable.
const pollInterval = 2 * time.Second

// startDetached launches the command in its own session so it survives both the
// SSH connection closing and HostMan being restarted by the command itself.
func startDetachedScript(command, logPath string) string {
	// setsid detaches from the session sshd will tear down; nohup covers the
	// SIGHUP; the marker line records the exit status for whoever reads the
	// file later.
	//
	// The marker is written from an EXIT trap rather than a trailing command:
	// an install script ending in `exit 1` — or aborting under `set -e` —
	// would otherwise never reach it, leaving the follower waiting for an
	// answer that never comes.
	inner := fmt.Sprintf("trap 'printf \"\\n%s%%s\\n\" \"$?\"' EXIT\n%s", exitMarker, WrapCommand(command))
	return fmt.Sprintf(
		": > %s\nnohup setsid bash -c %s >> %s 2>&1 < /dev/null &\necho detached",
		ShellQuote(logPath), ShellQuote(inner), ShellQuote(logPath))
}

// readScript returns the whole log at once, for a follower that just restarted.
func readScript(logPath string) string {
	return "cat " + ShellQuote(logPath) + " 2>/dev/null || true"
}

// parseExitMarker looks for the recorded exit status in a detached log. The
// marker only counts as the last line of the file: the trap writes it as the
// very last thing, so anything printed after it means the command is still
// going and the "marker" was just output that happened to look like one.
func parseExitMarker(log string) (code int, done bool) {
	last := lastNonEmptyLine(log)
	rest, ok := strings.CutPrefix(last, exitMarker)
	if !ok {
		return 0, false
	}
	value, err := strconv.Atoi(strings.TrimSpace(rest))
	if err != nil {
		return 0, false
	}
	return value, true
}

func lastNonEmptyLine(s string) string {
	lines := strings.Split(s, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if line := strings.TrimRight(lines[i], "\r"); strings.TrimSpace(line) != "" {
			return line
		}
	}
	return ""
}

// stripExitMarker removes the bookkeeping line before the log is shown.
//
// It hides the marker as soon as its prefix appears at the start of the last
// line, without waiting for the exit status to follow it. A read that lands
// mid-printf sees `__deployer_exit__:` with no digits yet, and that must not
// reach the log — once written it would never be taken back, because the
// follower only ever appends what is new.
//
// Requiring it to be the *last* line is what keeps this from swallowing real
// output: a command that happens to print the marker mid-stream keeps
// everything after it, and parseExitMarker still refuses to call it finished.
func stripExitMarker(log string) string {
	if !strings.HasPrefix(lastNonEmptyLine(log), exitMarker) {
		return log
	}
	idx := strings.LastIndex(log, exitMarker)
	return strings.TrimRight(log[:idx], "\n")
}

// ResumeDetached picks up detached deployments that were still running when
// HostMan stopped — most often because they were what restarted it.
func (rn *Runner) ResumeDetached(ctx context.Context) {
	pending, err := rn.db.ResumableDeployments(ctx)
	if err != nil {
		rn.log.Error("deploy: list resumable deployments", "err", err)
		return
	}
	for _, dep := range pending {
		host, err := rn.db.GetHost(ctx, dep.HostID)
		if err != nil {
			rn.log.Error("deploy: resume needs its host", "deployment", dep.ID, "err", err)
			continue
		}
		rn.log.Info("resuming deployment that outlived a restart", "deployment", dep.ID, "app", dep.AppName)

		runCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), Timeout)
		run := newRun(dep.ID, cancel)
		run.detached = true
		rn.mu.Lock()
		rn.active[dep.ID] = run
		rn.busy[[2]int64{dep.AppID, dep.HostID}] = dep.ID
		rn.mu.Unlock()

		go rn.runDetached(runCtx, cancel, run, dep, host, true)
	}
}

// runDetached launches a detached deployment (unless it is being resumed after
// a restart) and follows its log until it records an exit status.
func (rn *Runner) runDetached(ctx context.Context, cancel context.CancelFunc, run *Run, dep *store.Deployment, host *store.Host, resumed bool) {
	defer cancel()
	defer func() {
		rn.mu.Lock()
		delete(rn.active, dep.ID)
		delete(rn.busy, [2]int64{dep.AppID, dep.HostID})
		rn.mu.Unlock()
		run.finish()
	}()

	if resumed {
		fmt.Fprintf(run, "==> Reconnected after restart, following %s on %s\n\n",
			dep.DetachedLog, host.Name)
	} else {
		fmt.Fprintf(run, "==> Updating %s on %s (%s@%s)\n$ %s\n\n",
			dep.AppName, host.Name, host.Username, host.Address, dep.Command)
		fmt.Fprint(run, "This runs detached, so it survives HostMan restarting itself.\n\n")
		if err := rn.launchDetached(ctx, host, dep); err != nil {
			// A cancel while connecting is a cancel, not a failure.
			if status, failure, ok := interrupted(ctx); ok {
				rn.finishDetached(run, dep, host, status, nil, failure)
				return
			}
			fmt.Fprintf(run, "!! Could not start: %v\n", err)
			rn.finishDetached(run, dep, host, store.DeployFailed, nil, err.Error())
			return
		}
	}

	status, exitCode, failure := rn.tailUntilDone(ctx, run, dep, host)
	if status == statusAbandoned {
		// HostMan is shutting down, most likely because this very update
		// restarted it. The row stays running and the next process resumes it.
		return
	}
	rn.finishDetached(run, dep, host, status, exitCode, failure)
}

// statusAbandoned is internal: it never reaches the database.
const statusAbandoned = "abandoned"

// launchDetached starts the command on the host in its own session.
func (rn *Runner) launchDetached(ctx context.Context, host *store.Host, dep *store.Deployment) error {
	startCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	client, err := rn.hosts.Connect(startCtx, host)
	if err != nil {
		return err
	}
	defer client.Close()

	res, err := client.Run(startCtx, startDetachedScript(dep.Command, dep.DetachedLog))
	if err != nil {
		return err
	}
	if res.ExitCode != 0 {
		return fmt.Errorf("could not start the update (exit %d): %s",
			res.ExitCode, strings.TrimSpace(res.Stderr))
	}
	return nil
}

// finishDetached records the outcome of a detached deployment.
func (rn *Runner) finishDetached(run *Run, dep *store.Deployment, host *store.Host, status string, exitCode *int, failure string) {
	saveCtx, saveCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer saveCancel()

	switch status {
	case store.DeploySucceeded:
		fmt.Fprintf(run, "\n==> Succeeded in %s\n", time.Since(dep.StartedAt).Round(time.Second))
	case store.DeployCanceled:
		fmt.Fprint(run, "\n==> Stopped watching. The command keeps running on the host.\n")
	default:
		fmt.Fprintf(run, "\n==> Failed: %s\n", failure)
	}

	if err := rn.db.FinishDeployment(saveCtx, dep.ID, status, exitCode, failure, run.log()); err != nil {
		rn.log.Error("deploy: record detached outcome", "deployment", dep.ID, "err", err)
	}
	if status != store.DeploySucceeded {
		return
	}
	if err := rn.db.UpsertInstallation(saveCtx, dep.AppID, dep.HostID, dep.Params, dep.ID); err != nil {
		rn.log.Error("deploy: record installation", "deployment", dep.ID, "err", err)
		return
	}
	rn.log.Info("self-update finished", "app", dep.AppName, "host", host.Name)
	if rn.health != nil {
		rn.health.CheckSoon(dep.AppID, dep.HostID, 5*time.Second)
	}
}

// tailUntilDone follows the log file, reconnecting while the host is restarting
// services underneath it.
func (rn *Runner) tailUntilDone(ctx context.Context, run *Run, dep *store.Deployment, host *store.Host) (status string, exitCode *int, failure string) {
	// What has already been written to `run`, so reconnects don't duplicate it.
	seen := 0
	attempt := 0

	for {
		select {
		case <-run.abandon:
			return statusAbandoned, nil, ""
		default:
		}
		if err := ctx.Err(); err != nil {
			if s, f, ok := interrupted(ctx); ok {
				return s, nil, f
			}
			return store.DeployFailed, nil, err.Error()
		}

		full, err := rn.readDetachedLog(ctx, host, dep.DetachedLog)
		if err == nil {
			// The marker is bookkeeping, not output: never show it.
			visible := stripExitMarker(full)
			if len(visible) > seen {
				run.Write([]byte(visible[seen:]))
				seen = len(visible)
			}
			if code, done := parseExitMarker(full); done {
				if code == 0 {
					return store.DeploySucceeded, &code, ""
				}
				return store.DeployFailed, &code, fmt.Sprintf("install command exited with status %d", code)
			}
			attempt = 0
		} else {
			attempt++
			// The service being updated is HostMan itself, so losing the
			// connection for a while is expected, not a failure.
			if attempt == 1 {
				fmt.Fprintf(run, "\n... host unreachable, waiting (%v)\n", err)
			}
			if attempt > 60 {
				return store.DeployFailed, nil, "lost contact with the host while the update was running"
			}
		}

		select {
		case <-ctx.Done():
		case <-run.abandon:
			return statusAbandoned, nil, ""
		case <-time.After(pollInterval):
		}
	}
}

// readDetachedLog fetches the whole log in one short-lived connection, so a
// restart of the SSH server (or of HostMan) cannot strand a long-lived one.
func (rn *Runner) readDetachedLog(ctx context.Context, host *store.Host, logPath string) (string, error) {
	readCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()

	client, err := rn.hosts.Connect(readCtx, host)
	if err != nil {
		return "", err
	}
	defer client.Close()

	res, err := client.Run(readCtx, readScript(logPath))
	if err != nil {
		return "", err
	}
	return res.Stdout, nil
}
