package hostops

import (
	"context"
	"errors"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/chinmay28/hostman/server/internal/store"
)

// Ollama is the local model server on a host, driven through its own `ollama`
// command. The screen does what anybody does with one from a phone: see which
// models are on the disk and which are loaded in memory, pull another, drop
// one, unload one, and talk to one — which is the Terminal screen with
// `ollama run` typed into it.
//
// Three decisions shape this file.
//
// **Ollama is the host's to install.** Like VirtualBox and deluge, and unlike
// everything HostMan fetches for itself, a model server is a decision about
// what a machine is for — gigabytes of disk and whatever GPU it has — and its
// installer is a `curl | sh` that HostMan should not be the one to run. The
// probe records whether `ollama` is there, and the host's page offers the
// screen only where it is.
//
// **Everything goes through the CLI, as the SSH user.** The command talks to
// the server over HTTP on localhost, so listing, pulling and removing need no
// root whoever the server runs as — the official install runs it as its own
// `ollama` user under systemd, and that is exactly the arrangement this file
// expects to find. The one thing that elevates is starting a stopped server,
// which is `systemctl start` and needs sudo like any other service.
//
// **A pull is detached.** A model is gigabytes, and a phone that locks its
// screen must not be able to cancel a download half way through. So pulling
// starts a job on the host that writes to a log and a state file of its own,
// and the status reads them back — the same arrangement as updating deluge.
// One pull at a time: a second is refused while the first runs, because two
// downloads on one link are each slower than one, and the screen can only
// follow one bar.

// ollamaHome is where the pull's bookkeeping lives, under the SSH user's
// state directory: the user's job, in the user's home, where a reboot that
// clears /tmp cannot lose track of it.
const ollamaHome = `${XDG_STATE_HOME:-$HOME/.local/state}/hostman/ollama`

// ollamaPath puts the official installer's location on the PATH. A login
// shell has it; the non-interactive shell an SSH command runs in may not.
const ollamaPath = `PATH="$PATH:/usr/local/bin:/usr/bin"; export PATH`

// MaxOllamaPullLogBytes is how much of a pull's log comes back. The CLI
// redraws its progress bar many times a second, so a log is mostly the same
// line over and over, and the tail is all that was ever wanted.
const MaxOllamaPullLogBytes = 8 << 10

// ollamaStatusScript reads everything the screen shows in one session. Every
// part is optional and a piece that is not there is an answer rather than an
// error, so the script always exits 0: a host without ollama is a fact, and a
// server that is not running is the thing the screen says next.
var ollamaStatusScript = ollamaPath + `
home="` + ollamaHome + `"
printf '@@have\n'
command -v ollama 2>/dev/null
printf '@@user\n'
id -un 2>/dev/null
printf '@@version\n'
command -v ollama >/dev/null 2>&1 && ollama --version 2>&1
printf '@@unit\n'
command -v systemctl >/dev/null 2>&1 && systemctl show -p LoadState -p ActiveState ollama.service 2>/dev/null
if command -v ollama >/dev/null 2>&1; then
  if out=$(ollama list 2>&1); then
    printf '@@list\n%s\n' "$out"
    printf '@@ps\n'
    ollama ps 2>/dev/null
  else
    printf '@@listerr\n%s\n' "$out"
  fi
fi

# Where a pull got to, if one was ever started here. The runner writes its own
# pid; one that says it is running and is not there any more died without
# saying so, and a reboot in the middle of a download is how that happens.
state=$(cat "$home/pull.state" 2>/dev/null)
if [ "$state" = running ]; then
  pid=$(cat "$home/pull.pid" 2>/dev/null)
  [ -n "$pid" ] && [ -d "/proc/$pid" ] || state=lost
fi
printf '@@pull\n%s\n' "$state"
printf '@@pullmodel\n'
cat "$home/pull.model" 2>/dev/null
printf '@@pulllogsize\n'
wc -c < "$home/pull.log" 2>/dev/null || printf '0\n'
printf '@@pulllog\n'
tail -c ` + strconv.Itoa(MaxOllamaPullLogBytes) + ` "$home/pull.log" 2>/dev/null | base64 2>/dev/null
exit 0
`

// ollamaPullScript starts a pull and comes straight back. The model's name
// arrives as $1. The minutes of download are detached — nohup setsid, output
// to a log, the exit status recorded by an EXIT trap — because an SSH session
// that ends takes its command with it. What comes back from this call is only
// "it started"; where it got to is what the status script is for.
const ollamaPullScript = ollamaPath + `
set -u
command -v ollama >/dev/null 2>&1 || { echo 'ollama is not installed on this host' >&2; exit 7; }
home="` + ollamaHome + `"
mkdir -p "$home" || { printf 'could not write to %s\n' "$home" >&2; exit 4; }

# A pull already under way is left to finish rather than joined by another.
if [ "$(cat "$home/pull.state" 2>/dev/null)" = running ]; then
  pid=$(cat "$home/pull.pid" 2>/dev/null)
  if [ -n "$pid" ] && [ -d "/proc/$pid" ]; then
    printf '%s\n' "$(cat "$home/pull.model" 2>/dev/null)"
    exit 6
  fi
fi

cat > "$home/pull.sh" <<'PULL'
` + ollamaPullRunScript + `
PULL
chmod 700 "$home/pull.sh" || exit 5
printf '%s\n' "$1" > "$home/pull.model" || exit 7
: > "$home/pull.log" || exit 7
printf 'running\n' > "$home/pull.state" || exit 7
nohup setsid sh "$home/pull.sh" "$home" "$1" >> "$home/pull.log" 2>&1 < /dev/null &
printf 'started\n'
`

// ollamaPullRunScript is the half that takes minutes. It runs detached, so its
// only way of reporting anything is the log it writes and the state file its
// EXIT trap leaves behind. It is a shell around `ollama pull` rather than the
// command itself so that the trap can record how it ended, and so that the
// recorded pid heads a process group a cancel can kill as one.
const ollamaPullRunScript = `set -u
` + ollamaPath + `
home=$1
model=$2
printf '%s\n' "$$" > "$home/pull.pid"
trap 'code=$?; if [ "$code" -eq 0 ]; then printf "ok\n" > "$home/pull.state"; else printf "failed:%s\n" "$code" > "$home/pull.state"; fi' EXIT
ollama pull "$model"`

// ollamaCancelScript stops a pull that is under way. It marks the state first
// and kills second, so a runner whose trap never fires still reads as
// cancelled rather than running; a trap that does fire writes the signal's
// exit status over it, which the parser reads as the same thing.
const ollamaCancelScript = `home="` + ollamaHome + `"
pid=$(cat "$home/pull.pid" 2>/dev/null)
if [ -z "$pid" ] || [ ! -d "/proc/$pid" ]; then
  printf 'nothing\n'
  exit 0
fi
printf 'cancelled\n' > "$home/pull.state"
kill -TERM "-$pid" 2>/dev/null || kill -TERM "$pid" 2>/dev/null
printf 'cancelled\n'
`

// ollamaClearScript forgets a finished pull, so the screen stops showing it.
const ollamaClearScript = `home="` + ollamaHome + `"
if [ "$(cat "$home/pull.state" 2>/dev/null)" = running ]; then
  pid=$(cat "$home/pull.pid" 2>/dev/null)
  if [ -n "$pid" ] && [ -d "/proc/$pid" ]; then
    echo 'a pull is still running; cancel it first' >&2
    exit 6
  fi
fi
rm -f "$home/pull.state" "$home/pull.model" "$home/pull.log" "$home/pull.pid"
`

// ollamaModelScript runs one verb on one model: $1 is the ollama subcommand,
// $2 the model. Which verbs are allowed is decided in Go before anything
// connects, so the case here only says how each is spelled.
const ollamaModelScript = ollamaPath + `
command -v ollama >/dev/null 2>&1 || { echo 'ollama is not installed on this host' >&2; exit 7; }
case "$1" in
  rm) ollama rm "$2" ;;
  stop) ollama stop "$2" ;;
  *) echo "unknown verb $1" >&2; exit 2 ;;
esac
`

// ollamaStartScript brings a stopped server up. Where the official installer
// left a unit, that is the way — through sudo, because starting a system
// service is root's — and where there is none, `ollama serve` is started
// detached as the SSH user, so it belongs to whoever asked and keeps running
// after this session ends. Either way it waits for the server to answer, so
// the status that follows shows models rather than "not running".
const ollamaStartScript = ollamaPath + `
command -v ollama >/dev/null 2>&1 || { echo 'ollama is not installed on this host' >&2; exit 7; }
home="` + ollamaHome + `"
if command -v systemctl >/dev/null 2>&1 && systemctl cat ollama.service >/dev/null 2>&1; then
  if [ "$(id -u 2>/dev/null)" = 0 ]; then
    systemctl start ollama.service || exit 8
  else
    sudo -n systemctl start ollama.service 2>/dev/null || { echo 'starting the ollama service needs passwordless sudo' >&2; exit 3; }
  fi
else
  mkdir -p "$home" || exit 4
  nohup setsid ollama serve >> "$home/serve.log" 2>&1 < /dev/null &
fi
n=0
while [ "$n" -lt 20 ]; do
  ollama list >/dev/null 2>&1 && exit 0
  n=$((n + 1))
  sleep 0.5
done
echo 'the server did not answer after starting' >&2
exit 9
`

// OllamaHost is what a host says about ollama: whether it is there, whether
// the server answers, what is on the disk and in memory, and where a pull
// got to.
type OllamaHost struct {
	// Installed is whether the ollama command is on the host. The probe
	// records the same fact so the host page knows whether to offer the
	// screen; this is the live answer when the screen is open.
	Installed bool `json:"installed"`
	// Version is ollama's own, e.g. 0.6.2, read from the command whether or
	// not the server is up.
	Version string `json:"version,omitempty"`
	// User is who asked: the SSH user, who is also who a pull runs as.
	User string `json:"user"`
	// Running is whether the server answered. When it is false the lists are
	// empty and Error says what the command said instead.
	Running bool `json:"running"`
	// Error is what ollama printed when it could not be asked.
	Error string `json:"error,omitempty"`
	// Unit is systemd's word for ollama.service — active, inactive, failed —
	// or empty where there is no such unit, which is how a server started by
	// hand looks.
	Unit string `json:"unit,omitempty"`
	// Models is what is on the disk, by name.
	Models []OllamaModel `json:"models"`
	// Loaded is what is in memory right now, as `ollama ps` reports it.
	Loaded []OllamaLoaded `json:"loaded"`
	// Pull is the pull that is running or the last one that ran, until it
	// is cleared; nil where none was ever started here.
	Pull *OllamaPull `json:"pull,omitempty"`
}

// OllamaModel is one model on the disk, as `ollama list` describes it.
type OllamaModel struct {
	// Name is what ollama knows it by, tag included: llama3.2:latest.
	Name string `json:"name"`
	ID   string `json:"id"`
	// Size is ollama's own figure, e.g. "4.7 GB"; SizeBytes is the same
	// number for adding up.
	Size      string `json:"size"`
	SizeBytes int64  `json:"sizeBytes"`
	// Modified is ollama's own relative wording: "2 weeks ago".
	Modified string `json:"modified"`
}

// OllamaLoaded is one model in memory, as `ollama ps` describes it.
type OllamaLoaded struct {
	Name string `json:"name"`
	ID   string `json:"id"`
	Size string `json:"size"`
	// Processor is where it runs: "100% GPU", "100% CPU", or a split.
	Processor string `json:"processor"`
	// Context is the context window it was loaded with, where ollama says.
	Context string `json:"context,omitempty"`
	// Until is when it unloads if nothing asks for it: "4 minutes from now".
	Until string `json:"until"`
}

// OllamaPull is where a pull got to, for the screen to follow.
type OllamaPull struct {
	// Model is what was asked for.
	Model string `json:"model"`
	// State is running, ok, failed, cancelled or lost — the last where the
	// runner disappeared without recording how it ended.
	State string `json:"state"`
	// Exit is the status behind a failed pull.
	Exit int `json:"exit,omitempty"`
	// Percent is the last figure the progress bar showed, or -1 where it
	// has not shown one yet.
	Percent int `json:"percent"`
	// Status is the last line ollama printed, with the bar drawing stripped:
	// "pulling 8934d96d3f08... 42%", "verifying sha256 digest", or the error.
	Status string `json:"status,omitempty"`
	// Log is the tail of the pull's log, cleaned of the redrawing.
	Log string `json:"log,omitempty"`
}

// OllamaActions are the verbs the screen offers, by the name a request uses.
// "remove" and "stop" name a model; "start", "cancel" and "clear" do not.
var OllamaActions = map[string]bool{
	"remove": true,
	"stop":   true,
	"start":  true,
	"cancel": true,
	"clear":  true,
}

// ollamaActionNames is OllamaActions' keys in the order a message lists them.
var ollamaActionNames = []string{"remove", "stop", "start", "cancel", "clear"}

// ollamaModelPattern is a model reference as ollama spells one: a name with
// an optional namespace, registry and tag — llama3.2, llama3.2:3b,
// library/llama3.2, hf.co/org/model:Q4_K_M. It starts with a letter or digit,
// so a name can never be read as an option, and it carries no space or quote,
// so it is one argument whatever the shell.
var ollamaModelPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/-]{0,255}$`)

// ollamaVersionPattern finds the number in whatever `ollama --version`
// prints: "ollama version is 0.6.2", or "Warning: client version is 0.6.2"
// when the server is down.
var ollamaVersionPattern = regexp.MustCompile(`\d+\.\d+\.\d+`)

// ollamaPercentPattern is the figure a progress line carries.
var ollamaPercentPattern = regexp.MustCompile(`(\d{1,3})%`)

// progressEscapes is the terminal escape sequences a progress bar is drawn
// with: cursor movement, clearing a line, hiding and showing the cursor.
var progressEscapes = regexp.MustCompile(`\x1b\[[0-9;?]*[A-Za-z]`)

// OllamaStatus reads ollama's state on h.
func (s *Service) OllamaStatus(ctx context.Context, h *store.Host) (*OllamaHost, error) {
	res, err := s.run(ctx, h, asUser(ollamaStatusScript), "")
	if err != nil {
		return nil, err
	}
	if res.ExitCode != 0 {
		return nil, failure(res, "could not read ollama's state")
	}
	return parseOllamaHost(res.Stdout), nil
}

// PullOllamaModel starts pulling a model and answers with the status as it
// is afterwards, with the pull under way. The name is checked before anything
// connects. A pull already running is refused by name rather than joined.
func (s *Service) PullOllamaModel(ctx context.Context, h *store.Host, model string) (*OllamaHost, error) {
	model = strings.TrimSpace(model)
	if !ollamaModelPattern.MatchString(model) {
		return nil, invalid("that is not a model name: ollama knows models as name:tag, like llama3.2:3b")
	}
	client, err := s.conn.Connect(ctx, h)
	if err != nil {
		return nil, err
	}
	defer client.Close()

	res, err := client.Run(ctx, asUser(ollamaPullScript, model))
	if err != nil {
		return nil, err
	}
	switch res.ExitCode {
	case 0:
	case 6:
		busy := strings.TrimSpace(res.Stdout)
		if busy == "" {
			busy = "another model"
		}
		return nil, invalid("%s is still pulling %s; wait for it, or cancel it", h.Name, busy)
	case 7:
		return nil, invalid("ollama is not installed on %s", h.Name)
	default:
		return nil, failure(res, "could not start pulling "+model)
	}
	after, err := client.Run(ctx, asUser(ollamaStatusScript))
	if err != nil {
		return nil, err
	}
	return parseOllamaHost(after.Stdout), nil
}

// OllamaAction runs one verb and answers with the status afterwards. The verb
// is checked against OllamaActions, and a model's name against the shape of
// one, before anything connects.
func (s *Service) OllamaAction(ctx context.Context, h *store.Host, action, model string) (*OllamaHost, error) {
	if !OllamaActions[action] {
		return nil, invalid("an ollama action is %s", joinNames(ollamaActionNames))
	}
	model = strings.TrimSpace(model)
	var cmd string
	switch action {
	case "remove", "stop":
		if !ollamaModelPattern.MatchString(model) {
			return nil, invalid("that is not a model name")
		}
		verb := map[string]string{"remove": "rm", "stop": "stop"}[action]
		cmd = asUser(ollamaModelScript, verb, model)
	case "start":
		cmd = asUser(ollamaStartScript)
	case "cancel":
		cmd = asUser(ollamaCancelScript)
	case "clear":
		cmd = asUser(ollamaClearScript)
	}
	client, err := s.conn.Connect(ctx, h)
	if err != nil {
		return nil, err
	}
	defer client.Close()

	res, err := client.Run(ctx, cmd)
	if err != nil {
		return nil, err
	}
	switch res.ExitCode {
	case 0:
	case 3:
		return nil, errNeedsRoot(h.Username, h.Name)
	case 6:
		return nil, invalid("%s is still pulling a model; cancel it first", h.Name)
	case 7:
		return nil, invalid("ollama is not installed on %s", h.Name)
	default:
		return nil, ollamaFailure(res.Stderr, res.Stdout, action, model)
	}
	after, err := client.Run(ctx, asUser(ollamaStatusScript))
	if err != nil {
		return nil, err
	}
	return parseOllamaHost(after.Stdout), nil
}

// parseOllamaHost turns the status script's output into an OllamaHost.
func parseOllamaHost(out string) *OllamaHost {
	got := sections(out)
	host := &OllamaHost{
		Installed: first(got["have"]) != "",
		User:      first(got["user"]),
		Version:   ollamaVersionPattern.FindString(strings.Join(got["version"], "\n")),
		Models:    []OllamaModel{},
		Loaded:    []OllamaLoaded{},
	}
	for _, line := range got["unit"] {
		if state, ok := strings.CutPrefix(line, "ActiveState="); ok {
			host.Unit = strings.TrimSpace(state)
		}
		if state, ok := strings.CutPrefix(line, "LoadState="); ok && strings.TrimSpace(state) == "not-found" {
			host.Unit = ""
			break
		}
	}
	if list, ok := got["list"]; ok {
		host.Running = true
		for _, row := range parseTable(list) {
			host.Models = append(host.Models, OllamaModel{
				Name:      row["NAME"],
				ID:        row["ID"],
				Size:      row["SIZE"],
				SizeBytes: humanBytes(row["SIZE"]),
				Modified:  row["MODIFIED"],
			})
		}
		for _, row := range parseTable(got["ps"]) {
			host.Loaded = append(host.Loaded, OllamaLoaded{
				Name:      row["NAME"],
				ID:        row["ID"],
				Size:      row["SIZE"],
				Processor: row["PROCESSOR"],
				Context:   row["CONTEXT"],
				Until:     row["UNTIL"],
			})
		}
	} else if errLines := got["listerr"]; len(errLines) > 0 {
		host.Error = ollamaErrorLine(errLines)
	}
	sort.SliceStable(host.Models, func(i, j int) bool {
		return strings.ToLower(host.Models[i].Name) < strings.ToLower(host.Models[j].Name)
	})
	host.Pull = parseOllamaPull(got)
	return host
}

// parseOllamaPull reads where a pull got to out of the status sections, or
// nothing where none was ever started on this host.
func parseOllamaPull(got map[string][]string) *OllamaPull {
	state := first(got["pull"])
	if state == "" {
		return nil
	}
	pull := &OllamaPull{Model: first(got["pullmodel"]), State: state, Percent: -1}
	if code, ok := strings.CutPrefix(state, "failed:"); ok {
		pull.State = "failed"
		pull.Exit, _ = strconv.Atoi(strings.TrimSpace(code))
		// 143 is SIGTERM's exit status and 130 is Ctrl-C's: a pull that was
		// stopped on purpose, not one that went wrong.
		if pull.Exit == 143 || pull.Exit == 130 {
			pull.State = "cancelled"
		}
	}
	raw, _ := decodeLog(got["pulllog"], first(got["pulllogsize"]))
	lines := cleanProgress(raw)
	if len(lines) > 0 {
		pull.Status = lines[len(lines)-1]
		pull.Log = strings.Join(lines, "\n")
	}
	pull.Percent = lastPercent(lines)
	if pull.State == "ok" {
		pull.Percent = 100
	}
	return pull
}

// cleanProgress turns what a progress bar wrote into lines a person can read.
// The CLI redraws in place — carriage returns, cursor moves — and draws every
// layer again on every tick, so the log is the same few lines over and over
// with the figures moving. Each redraw becomes a line, the escapes go, the
// bar's block characters go, and a line that redraws one already seen
// replaces it where it stood. What is left is the story of the pull, one step
// per line in the order the steps began, each at the point it got to.
func cleanProgress(raw string) []string {
	raw = progressEscapes.ReplaceAllString(raw, "")
	raw = strings.ReplaceAll(raw, "\r", "\n")
	var lines []string
	at := map[string]int{}
	for _, line := range strings.Split(raw, "\n") {
		line = strings.Map(func(r rune) rune {
			// The bar is drawn in block elements and the spinner in braille;
			// neither is a word.
			if (r >= 0x2580 && r <= 0x259F) || (r >= 0x2800 && r <= 0x28FF) {
				return -1
			}
			return r
		}, line)
		line = strings.Join(strings.Fields(line), " ")
		if line == "" {
			continue
		}
		key := progressKey(line)
		if i, seen := at[key]; seen {
			lines[i] = line
			continue
		}
		at[key] = len(lines)
		lines = append(lines, line)
	}
	// What the screen shows is the end of the story.
	const keep = 12
	if len(lines) > keep {
		lines = lines[len(lines)-keep:]
	}
	return lines
}

// progressKey is the part of a progress line that stays the same between
// redraws: everything before its percentage. A layer's line is "pulling
// <digest>... 42% ..." and its digest is what tells it from the next layer;
// a line with no figure, "verifying sha256 digest", is its own key.
func progressKey(line string) string {
	if loc := progressFigure.FindStringIndex(line); loc != nil {
		return strings.TrimSpace(line[:loc[0]])
	}
	return line
}

// progressFigure is where a progress line's moving part begins.
var progressFigure = regexp.MustCompile(`\s\d{1,3}%`)

// lastPercent is the last figure any line of the log carried, or -1.
func lastPercent(lines []string) int {
	for i := len(lines) - 1; i >= 0; i-- {
		if m := ollamaPercentPattern.FindAllStringSubmatch(lines[i], -1); len(m) > 0 {
			n, _ := strconv.Atoi(m[len(m)-1][1])
			if n >= 0 && n <= 100 {
				return n
			}
		}
	}
	return -1
}

// parseTable reads the aligned table the CLI prints: a header of column
// names, then one row per line. The header's column starts are where each
// row's values begin — the CLI pads every row to the same widths — which is
// what lets a value with spaces in it, "4.7 GB" or "2 weeks ago", come
// through whole. The names are the header's own words, upper case.
func parseTable(lines []string) []map[string]string {
	if len(lines) == 0 {
		return nil
	}
	header := lines[0]
	type column struct {
		name  string
		start int
	}
	var columns []column
	inWord := false
	for i, r := range header {
		if r == ' ' || r == '\t' {
			inWord = false
			continue
		}
		if !inWord {
			columns = append(columns, column{start: i})
			inWord = true
		}
		columns[len(columns)-1].name += string(r)
	}
	if len(columns) == 0 {
		return nil
	}
	var rows []map[string]string
	for _, line := range lines[1:] {
		if strings.TrimSpace(line) == "" {
			continue
		}
		row := map[string]string{}
		for i, col := range columns {
			end := len(line)
			if i+1 < len(columns) && columns[i+1].start < end {
				end = columns[i+1].start
			}
			if col.start >= len(line) {
				row[col.name] = ""
				continue
			}
			row[col.name] = strings.TrimSpace(line[col.start:end])
		}
		rows = append(rows, row)
	}
	return rows
}

// humanBytes reads ollama's own size wording back into bytes: "4.7 GB" is
// 4.7 × 10⁹, because ollama counts in powers of a thousand. Anything it cannot
// read is 0, which is better than a guess in a sum.
func humanBytes(s string) int64 {
	fields := strings.Fields(s)
	if len(fields) != 2 {
		return 0
	}
	n, err := strconv.ParseFloat(fields[0], 64)
	if err != nil || n < 0 {
		return 0
	}
	units := map[string]float64{"B": 1, "KB": 1e3, "MB": 1e6, "GB": 1e9, "TB": 1e12}
	unit, ok := units[strings.ToUpper(fields[1])]
	if !ok {
		return 0
	}
	return int64(math.Round(n * unit))
}

// ollamaErrorLine is the sentence in what ollama printed when it could not
// be asked. It prefixes its complaints with "Error: ", which the screen does
// not need told.
func ollamaErrorLine(lines []string) string {
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if msg, ok := strings.CutPrefix(line, "Error: "); ok {
			return msg
		}
	}
	for i := len(lines) - 1; i >= 0; i-- {
		if line := strings.TrimSpace(lines[i]); line != "" {
			return line
		}
	}
	return "ollama did not answer"
}

// ollamaFailure turns what ollama printed into one sentence. It is the host
// saying no — a model that is not there, a server that is not up — rather
// than HostMan refusing, so it is not an invalid request.
func ollamaFailure(stderr, stdout, action, model string) error {
	if strings.TrimSpace(stderr+stdout) == "" {
		if model != "" {
			return errors.New("could not " + action + " " + model)
		}
		return errors.New("could not " + action)
	}
	return errors.New(ollamaErrorLine(strings.Split(stderr+"\n"+stdout, "\n")))
}
