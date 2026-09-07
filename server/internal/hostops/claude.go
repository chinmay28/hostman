package hostops

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/chinmay28/deployer/server/internal/store"
)

// Claude Code on a host is three things HostMan looks after before a
// conversation can start: the CLI being installed, the SSH user being signed
// in, and — since neither is instant — the state of getting there.
//
// Three decisions shape this file.
//
// **It is installed for the user, not the machine.** Anthropic's installer
// puts the binary in ~/.local/bin and keeps it up to date from there; no
// sudo, nothing in /usr, nothing another user on the host inherits. HostMan
// runs it as the SSH user and nothing else, which is also who the sessions
// run as.
//
// **The sign-in belongs to the host.** `claude auth login` prints a link and
// waits for a code. HostMan lifts the link out of the log, the phone opens
// it, and the code the phone is given goes back through HostMan to the
// process that is waiting. The token the CLI ends up with is written by the
// CLI into the user's own home directory, and HostMan never reads it, never
// stores it and never sends it anywhere. The same goes for an API key: it is
// written into the CLI's own settings file on the host and forgotten here.
//
// **Both take longer than a request.** An install downloads tens of
// megabytes onto a Pi; a sign-in waits for a person with a phone. Each runs
// detached — nohup setsid, output to a file, the exit status recorded by the
// shell — and the status call reads the files, so a screen that polls sees
// where things got to, and a phone that locked half way loses nothing.
//
// Nothing on the host is sourced. Every value the caller supplies arrives as
// a quoted positional argument, and the login code — the one thing a person
// types — is checked against an alphabet before it is sent.

const (
	// claudeStateDir is where HostMan keeps the state of an install or a
	// sign-in on the host, relative to the user's home. It is HostMan's,
	// and holds nothing secret: logs, exit statuses, and the link a sign-in
	// is waiting on.
	claudeStateDir = ".local/state/deployer-claude"

	// MaxClaudeLogBytes is how much of an install or login log comes back.
	MaxClaudeLogBytes = 16 << 10

	// claudeInstallURL is Anthropic's installer.
	claudeInstallURL = "https://claude.ai/install.sh"
)

// ClaudeHost is everything the screen needs in one round trip: whether the
// CLI is there, whether the user is signed in, and how far an install or a
// sign-in has got.
type ClaudeHost struct {
	// User is the account the CLI is installed for and runs as.
	User string `json:"user"`
	Home string `json:"home"`
	// Installed reports the CLI being present for that user, and Version is
	// what it says it is.
	Installed bool   `json:"installed"`
	Version   string `json:"version,omitempty"`
	// Path is where the binary was found.
	Path string `json:"path,omitempty"`
	// Arch and OS are the host's, for the screen to say what it would fetch.
	Arch string `json:"arch,omitempty"`
	OS   string `json:"os,omitempty"`
	// Install is "absent", "running", "failed" or "done": the state of the
	// install HostMan started, if it started one.
	Install     string `json:"install"`
	InstallExit int    `json:"installExit,omitempty"`
	InstallLog  string `json:"installLog,omitempty"`
	// SignedIn reports the CLI having credentials for this user, and Auth is
	// how: "oauth" for a Claude account, "api_key" for a key, or empty.
	SignedIn bool   `json:"signedIn"`
	Auth     string `json:"auth,omitempty"`
	// Account is the signed-in account's email, when the CLI recorded one.
	Account string `json:"account,omitempty"`
	// Plan is the subscription type the CLI recorded, when it did.
	Plan string `json:"plan,omitempty"`
	// Login is "absent", "waiting", "running", "failed" or "done": the state
	// of the sign-in HostMan started. "waiting" means the link is ready and
	// a code is what it needs.
	Login string `json:"login"`
	// LoginURL is the link to open, while a sign-in is waiting.
	LoginURL  string `json:"loginUrl,omitempty"`
	LoginExit int    `json:"loginExit,omitempty"`
	LoginLog  string `json:"loginLog,omitempty"`
	// Ready means a session could be started right now.
	Ready bool `json:"ready"`
}

// claudeStatusScript reads everything without writing anything. Every part is
// optional and a part that is not there is an answer, so it never fails.
//
// The one thing it runs is `claude auth status --json`, which is the CLI's
// own word on whether it is signed in. Where the CLI is not there the section
// is simply empty.
const claudeStatusScript = `set -u
max=%d
state="$HOME/%s"
PATH="$HOME/.local/bin:$PATH"; export PATH

printf '@@home\n%%s\n' "$HOME"
printf '@@arch\n%%s\n' "$(uname -m 2>/dev/null)"
printf '@@os\n%%s\n' "$(uname -s 2>/dev/null)"

printf '@@path\n'
command -v claude 2>/dev/null || true

printf '@@version\n'
if command -v claude >/dev/null 2>&1; then
  claude --version 2>/dev/null | head -1
fi

printf '@@auth\n'
if command -v claude >/dev/null 2>&1; then
  claude auth status --json 2>/dev/null | head -c 4096
fi

printf '\n@@config\n'
# The CLI's own record of who is signed in. The file can run to megabytes of
# project history, so only the two account fields are lifted out of it.
if [ -f "$HOME/.claude.json" ]; then
  grep -o -E '"(emailAddress|subscriptionType)"[[:space:]]*:[[:space:]]*"[^"]*"' "$HOME/.claude.json" 2>/dev/null | head -4
fi

printf '\n@@install\n'
if [ -f "$state/install.status" ]; then
  cat "$state/install.status"
elif [ -f "$state/install.pid" ] && kill -0 "$(cat "$state/install.pid" 2>/dev/null)" 2>/dev/null; then
  printf 'running\n'
elif [ -f "$state/install.log" ]; then
  # A log without a status and without a process: the install died without
  # writing one, which a reboot in the middle would do.
  printf 'lost\n'
fi

printf '@@install_log\n'
if [ -f "$state/install.log" ]; then
  tail -c "$max" "$state/install.log"
fi

printf '\n@@login\n'
if [ -f "$state/login.status" ]; then
  cat "$state/login.status"
elif [ -f "$state/login.pid" ] && kill -0 "$(cat "$state/login.pid" 2>/dev/null)" 2>/dev/null; then
  printf 'running\n'
elif [ -f "$state/login.log" ]; then
  printf 'lost\n'
fi

printf '@@login_log\n'
if [ -f "$state/login.log" ]; then
  tail -c "$max" "$state/login.log"
fi
printf '\n'
exit 0
`

// ClaudeStatus reports on Claude Code for the SSH user on a host.
func (s *Service) ClaudeStatus(ctx context.Context, h *store.Host) (*ClaudeHost, error) {
	script := fmt.Sprintf(claudeStatusScript, MaxClaudeLogBytes, claudeStateDir)
	res, err := s.run(ctx, h, asUser(script), "")
	if err != nil {
		return nil, err
	}
	if res.ExitCode != 0 {
		return nil, failure(res, "could not ask "+h.Name+" about Claude")
	}
	return parseClaudeStatus(res.Stdout, h.Username), nil
}

// loginURLPattern is the link the CLI prints for a browser that did not open.
var loginURLPattern = regexp.MustCompile(`https://[a-zA-Z0-9./_-]+/oauth/authorize\?[^\s'"]+`)

// parseClaudeStatus turns the script's output into the one answer the screen
// works from.
func parseClaudeStatus(out, user string) *ClaudeHost {
	found := sections(out)
	c := &ClaudeHost{
		User:    user,
		Home:    first(found["home"]),
		Arch:    first(found["arch"]),
		OS:      first(found["os"]),
		Path:    first(found["path"]),
		Install: "absent",
		Login:   "absent",
	}
	c.Installed = c.Path != ""
	if v := first(found["version"]); v != "" {
		// "2.1.211 (Claude Code)" is the CLI's phrasing; the number is enough.
		c.Version = strings.TrimSpace(strings.SplitN(v, " ", 2)[0])
	}

	if raw := strings.TrimSpace(strings.Join(found["auth"], "\n")); raw != "" {
		var auth struct {
			LoggedIn   bool   `json:"loggedIn"`
			AuthMethod string `json:"authMethod"`
		}
		if json.Unmarshal([]byte(raw), &auth) == nil {
			c.SignedIn = auth.LoggedIn
			c.Auth = authKind(auth.AuthMethod)
		}
	}
	for _, line := range found["config"] {
		key, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		value = strings.Trim(strings.TrimSpace(value), `"`)
		switch strings.Trim(strings.TrimSpace(key), `"`) {
		case "emailAddress":
			if c.Account == "" {
				c.Account = value
			}
		case "subscriptionType":
			if c.Plan == "" {
				c.Plan = value
			}
		}
	}

	c.Install, c.InstallExit = jobState(first(found["install"]))
	c.InstallLog = strings.TrimSpace(strings.Join(found["install_log"], "\n"))
	c.Login, c.LoginExit = jobState(first(found["login"]))
	c.LoginLog = strings.TrimSpace(strings.Join(found["login_log"], "\n"))
	if c.Login == "running" {
		if m := loginURLPattern.FindString(c.LoginLog); m != "" {
			c.LoginURL = m
			c.Login = "waiting"
		}
	}
	// A finished sign-in has done its job; the CLI's own word is what counts
	// from here, and a stale "done" would only confuse a later sign-out.
	if c.Login == "done" && !c.SignedIn {
		c.Login = "absent"
	}
	c.Ready = c.Installed && c.SignedIn
	return c
}

// authKind names the CLI's authentication method in HostMan's two words.
func authKind(method string) string {
	switch {
	case method == "":
		return ""
	case strings.Contains(method, "api_key"), strings.Contains(method, "apiKey"):
		return "api_key"
	default:
		return "oauth"
	}
}

// jobState reads a detached job's status file: absent, running, an exit
// status, or lost.
func jobState(status string) (string, int) {
	switch status {
	case "":
		return "absent", 0
	case "running":
		return "running", 0
	case "lost":
		return "failed", -1
	}
	code, err := strconv.Atoi(status)
	if err != nil {
		return "failed", -1
	}
	if code == 0 {
		return "done", 0
	}
	return "failed", code
}

// claudeInstallScript starts the installer detached and returns at once. The
// job itself is a few lines: fetch the installer, run it, write the status.
// Everything the user sees comes from the log the status call tails.
const claudeInstallScript = `set -u
state="$HOME/%s"
mkdir -p "$state" || exit 2
if [ -f "$state/install.pid" ] && kill -0 "$(cat "$state/install.pid" 2>/dev/null)" 2>/dev/null; then
  printf 'an install is already running\n' >&2
  exit 3
fi
command -v curl >/dev/null 2>&1 || { printf 'curl is not installed on this host\n' >&2; exit 4; }
rm -f "$state/install.status"
: > "$state/install.log"
cat > "$state/install.sh" <<'JOB'
#!/bin/sh
state=$1
url=$2
printf '== fetching the installer from %%s\n' "$url"
if curl -fsSL "$url" -o "$state/installer.sh"; then
  printf '== running the installer\n'
  bash "$state/installer.sh"
  code=$?
else
  printf 'could not download the installer\n' >&2
  code=5
fi
if [ "$code" -eq 0 ]; then
  PATH="$HOME/.local/bin:$PATH"; export PATH
  if command -v claude >/dev/null 2>&1; then
    printf '== installed %%s\n' "$(claude --version 2>/dev/null | head -1)"
  else
    printf 'the installer finished but claude is not on the PATH\n' >&2
    code=6
  fi
fi
rm -f "$state/installer.sh"
printf '%%s\n' "$code" > "$state/install.status"
rm -f "$state/install.pid"
exit "$code"
JOB
nohup setsid sh "$state/install.sh" "$state" %s >> "$state/install.log" 2>&1 < /dev/null &
printf '%%s\n' "$!" > "$state/install.pid"
exit 0
`

// InstallClaude starts installing the CLI for the SSH user, and returns as
// soon as the job is running. ClaudeStatus says how it is going.
func (s *Service) InstallClaude(ctx context.Context, h *store.Host) (*ClaudeHost, error) {
	script := fmt.Sprintf(claudeInstallScript, claudeStateDir, claudeInstallURL)
	res, err := s.run(ctx, h, asUser(script), "")
	if err != nil {
		return nil, err
	}
	if res.ExitCode != 0 {
		return nil, failure(res, "could not start installing Claude on "+h.Name)
	}
	return s.ClaudeStatus(ctx, h)
}

// claudeLoginScript starts `claude auth login` detached, with its stdin fed
// from a file HostMan appends the code to later. tail -f keeps the pipe open
// while the CLI waits, and the job kills it when the CLI is done.
//
// The CLI prints the link with "visit:" in front of it and then waits at
// "Paste code here"; the status call finds the link in the log.
const claudeLoginScript = `set -u
state="$HOME/%s"
PATH="$HOME/.local/bin:$PATH"; export PATH
command -v claude >/dev/null 2>&1 || { printf 'claude is not installed for this user\n' >&2; exit 4; }
mkdir -p "$state" || exit 2
if [ -f "$state/login.pid" ] && kill -0 "$(cat "$state/login.pid" 2>/dev/null)" 2>/dev/null; then
  kill "$(cat "$state/login.pid")" 2>/dev/null || true
  sleep 0.2
fi
rm -f "$state/login.status"
: > "$state/login.log"
: > "$state/login.code"
chmod 600 "$state/login.code" 2>/dev/null || true
cat > "$state/login.sh" <<'JOB'
#!/bin/sh
state=$1
shift
# The CLI reads the code from a pipe that tail keeps open: a pipeline would
# wait for tail too, and tail -f never ends, so the two are joined through a
# fifo instead and tail is ended by hand once the CLI is done.
rm -f "$state/login.fifo"
mkfifo "$state/login.fifo" || exit 2
tail -f "$state/login.code" > "$state/login.fifo" &
tailpid=$!
BROWSER=/bin/false claude auth login "$@" < "$state/login.fifo"
code=$?
kill "$tailpid" 2>/dev/null
printf '%%s\n' "$code" > "$state/login.status"
# The code file held a one-time secret; it is not needed once the CLI has it.
rm -f "$state/login.pid" "$state/login.code" "$state/login.fifo"
exit "$code"
JOB
nohup setsid sh "$state/login.sh" "$state" "$@" >> "$state/login.log" 2>&1 < /dev/null &
printf '%%s\n' "$!" > "$state/login.pid"
exit 0
`

// LoginClaude starts a sign-in for the SSH user. console asks for Console
// (API billing) rather than a Claude subscription.
func (s *Service) LoginClaude(ctx context.Context, h *store.Host, console bool) (*ClaudeHost, error) {
	script := fmt.Sprintf(claudeLoginScript, claudeStateDir)
	var args []string
	if console {
		args = append(args, "--console")
	}
	res, err := s.run(ctx, h, asUser(script, args...), "")
	if err != nil {
		return nil, err
	}
	if res.ExitCode != 0 {
		return nil, failure(res, "could not start signing in on "+h.Name)
	}
	return s.ClaudeStatus(ctx, h)
}

// loginCodePattern is the alphabet of a sign-in code: what an OAuth code and
// its state carry, joined by a hash. Anything else is not a code that was
// pasted, and does not reach the host.
var loginCodePattern = regexp.MustCompile(`^[A-Za-z0-9_#.=-]{8,512}$`)

// claudeLoginCodeScript hands the code to the waiting CLI by appending it to
// the file its stdin is following.
const claudeLoginCodeScript = `set -u
state="$HOME/%s"
if [ ! -f "$state/login.pid" ] || ! kill -0 "$(cat "$state/login.pid" 2>/dev/null)" 2>/dev/null; then
  printf 'no sign-in is waiting for a code\n' >&2
  exit 3
fi
printf '%%s\n' "$1" >> "$state/login.code" || exit 2
exit 0
`

// LoginCode gives a waiting sign-in the code the phone was shown.
func (s *Service) LoginCode(ctx context.Context, h *store.Host, code string) (*ClaudeHost, error) {
	code = strings.TrimSpace(code)
	if !loginCodePattern.MatchString(code) {
		return nil, invalid("that does not look like a sign-in code")
	}
	script := fmt.Sprintf(claudeLoginCodeScript, claudeStateDir)
	res, err := s.run(ctx, h, asUser(script, code), "")
	if err != nil {
		return nil, err
	}
	if res.ExitCode != 0 {
		return nil, failure(res, "could not hand the code to Claude on "+h.Name)
	}
	return s.ClaudeStatus(ctx, h)
}

// claudeCancelLoginScript ends a sign-in that is waiting.
const claudeCancelLoginScript = `set -u
state="$HOME/%s"
if [ -f "$state/login.pid" ]; then
  pid=$(cat "$state/login.pid" 2>/dev/null)
  [ -n "$pid" ] && kill -- -"$pid" 2>/dev/null || kill "$pid" 2>/dev/null || true
fi
rm -f "$state/login.pid" "$state/login.status" "$state/login.log" "$state/login.code"
exit 0
`

// CancelLogin ends a sign-in that is waiting and forgets it.
func (s *Service) CancelLogin(ctx context.Context, h *store.Host) (*ClaudeHost, error) {
	script := fmt.Sprintf(claudeCancelLoginScript, claudeStateDir)
	res, err := s.run(ctx, h, asUser(script), "")
	if err != nil {
		return nil, err
	}
	if res.ExitCode != 0 {
		return nil, failure(res, "could not cancel the sign-in on "+h.Name)
	}
	return s.ClaudeStatus(ctx, h)
}

// apiKeyPattern is the shape of an Anthropic API key.
var apiKeyPattern = regexp.MustCompile(`^sk-ant-[A-Za-z0-9_-]{20,300}$`)

// claudeAPIKeyScript writes the key into the CLI's own settings for the user,
// under env.ANTHROPIC_API_KEY, where the CLI reads it on every start. The key
// arrives on stdin, never on a command line the host's process list would
// show. The settings file is rewritten through a temporary file so a
// half-written one is never what the CLI reads.
const claudeAPIKeyScript = `set -u
dir="$HOME/.claude"
file="$dir/settings.json"
mkdir -p "$dir" || exit 2
chmod 700 "$dir" 2>/dev/null || true
key=$(head -c 512)
key=$(printf '%s' "$key" | tr -d '\r\n')
[ -n "$key" ] || { printf 'no key was given\n' >&2; exit 3; }
existing='{}'
[ -s "$file" ] && existing=$(cat "$file")
tmp=$(mktemp "$dir/.settings.XXXXXX") || exit 2
if command -v python3 >/dev/null 2>&1; then
  printf '%s' "$existing" | KEY="$key" python3 -c '
import json, os, sys
try:
    s = json.load(sys.stdin)
except Exception:
    s = {}
if not isinstance(s, dict):
    s = {}
env = s.get("env")
if not isinstance(env, dict):
    env = {}
env["ANTHROPIC_API_KEY"] = os.environ["KEY"]
s["env"] = env
print(json.dumps(s, indent=2))
' > "$tmp" || { rm -f "$tmp"; exit 4; }
elif command -v node >/dev/null 2>&1; then
  printf '%s' "$existing" | KEY="$key" node -e '
let s = {};
try { s = JSON.parse(require("fs").readFileSync(0, "utf8")); } catch {}
if (typeof s !== "object" || s === null || Array.isArray(s)) s = {};
if (typeof s.env !== "object" || s.env === null) s.env = {};
s.env.ANTHROPIC_API_KEY = process.env.KEY;
process.stdout.write(JSON.stringify(s, null, 2) + "\n");
' > "$tmp" || { rm -f "$tmp"; exit 4; }
else
  # Neither python nor node: only an empty or absent settings file can be
  # written safely, because nothing here can parse an existing one.
  if [ "$existing" != '{}' ] && [ -n "$(printf '%s' "$existing" | tr -d '[:space:]{}')" ]; then
    rm -f "$tmp"
    printf 'this host has neither python3 nor node to edit ~/.claude/settings.json; add the key by hand\n' >&2
    exit 5
  fi
  printf '{\n  "env": {\n    "ANTHROPIC_API_KEY": "%s"\n  }\n}\n' "$key" > "$tmp" || { rm -f "$tmp"; exit 4; }
fi
chmod 600 "$tmp" 2>/dev/null || true
mv -f "$tmp" "$file" || { rm -f "$tmp"; exit 4; }
exit 0
`

// SetAPIKey stores an Anthropic API key in the CLI's settings for the SSH
// user, which signs the CLI in without a Claude account.
func (s *Service) SetAPIKey(ctx context.Context, h *store.Host, key string) (*ClaudeHost, error) {
	key = strings.TrimSpace(key)
	if !apiKeyPattern.MatchString(key) {
		return nil, invalid("that does not look like an Anthropic API key (they start with sk-ant-)")
	}
	res, err := s.run(ctx, h, asUser(claudeAPIKeyScript), key+"\n")
	if err != nil {
		return nil, err
	}
	if res.ExitCode != 0 {
		return nil, failure(res, "could not store the key on "+h.Name)
	}
	return s.ClaudeStatus(ctx, h)
}
