// Package deploy runs an app's install command on a host and watches what
// happens afterwards.
package deploy

import (
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/chinmay28/deployer/server/internal/sshx"
	"github.com/chinmay28/deployer/server/internal/store"
)

// placeholder matches {{name}} in a command or health target.
var placeholder = regexp.MustCompile(`\{\{\s*([a-zA-Z_][a-zA-Z0-9_]*)\s*\}\}`)

// Built-in values available to every template alongside the app's own params.
const (
	VarHost     = "host"     // the host's address, as HostMan connects to it
	VarHostName = "hostname" // the host's name in HostMan
	VarUser     = "user"     // the SSH user
)

// Render substitutes {{name}} placeholders. When quote is true each value is
// wrapped in POSIX single quotes, so a parameter can never break out of the
// command it lands in; URLs and unit names are rendered unquoted.
func Render(template string, values map[string]string, quote bool) (string, error) {
	var unknown []string
	out := placeholder.ReplaceAllStringFunc(template, func(match string) string {
		name := placeholder.FindStringSubmatch(match)[1]
		v, ok := values[name]
		if !ok {
			unknown = append(unknown, name)
			return match
		}
		if quote {
			return ShellQuote(v)
		}
		return v
	})
	if len(unknown) > 0 {
		sort.Strings(unknown)
		return "", fmt.Errorf("unknown placeholder(s): %s", strings.Join(dedupe(unknown), ", "))
	}
	return out, nil
}

// ValidateShellTemplate rejects placeholders that sit inside quotes. Values are
// substituted as complete quoted words, so `--name "{{app}}"` would put literal
// quote characters into the value. Catching it when the app is saved is much
// kinder than discovering it in a deployment log.
func ValidateShellTemplate(template string) error {
	var inSingle, inDouble bool
	for i := 0; i < len(template); i++ {
		switch c := template[i]; {
		case c == '\\' && !inSingle:
			i++ // skip whatever is escaped
		case c == '\'' && !inDouble:
			inSingle = !inSingle
		case c == '"' && !inSingle:
			inDouble = !inDouble
		case c == '{' && i+1 < len(template) && template[i+1] == '{' && (inSingle || inDouble):
			name := placeholder.FindString(template[i:])
			if name == "" {
				name = "{{...}}"
			}
			return fmt.Errorf("remove the quotes around %s — parameter values are quoted automatically", name)
		}
	}
	return nil
}

// shellSafe matches values the shell cannot do anything interesting with, so
// they can be left unquoted. Everything outside this set — spaces, quotes,
// $, backticks, ;, |, &, <, >, (, ), *, ?, [, ], {, }, ~, !, #, \, newlines —
// is quoted. Erring toward quoting is the safe direction.
var shellSafe = regexp.MustCompile(`^[A-Za-z0-9._:/@=+,-]+$`)

// ShellQuote renders s as a single POSIX shell word. Ordinary values like
// ports, hostnames and URLs come back unchanged, which keeps the command
// readable in the confirmation sheet and the deployment log.
func ShellQuote(s string) string {
	if shellSafe.MatchString(s) {
		return s
	}
	return sshx.Quote(s)
}

// ResolveParams merges submitted values over an app's defaults and adds the
// built-in host variables, reporting anything required that is still missing.
func ResolveParams(app *store.App, host *store.Host, submitted map[string]string) (values map[string]string, params map[string]string, err error) {
	params = map[string]string{}
	var missing []string
	for _, p := range app.Params {
		v, ok := submitted[p.Name]
		v = strings.TrimSpace(v)
		if !ok || v == "" {
			v = p.Default
		}
		if p.Required && strings.TrimSpace(v) == "" {
			missing = append(missing, p.Label+" ("+p.Name+")")
			continue
		}
		params[p.Name] = v
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		return nil, nil, fmt.Errorf("missing required parameter(s): %s", strings.Join(missing, ", "))
	}

	values = map[string]string{}
	for k, v := range params {
		values[k] = v
	}
	// Built-ins last: an app cannot shadow them with a param of the same name.
	values[VarHost] = host.Address
	values[VarHostName] = host.Name
	values[VarUser] = host.Username
	return values, params, nil
}

// BuildCommand renders an app's install command for a host.
func BuildCommand(app *store.App, host *store.Host, submitted map[string]string) (command string, params map[string]string, err error) {
	return buildFrom(app.InstallCommand, app, host, submitted)
}

// BuildUninstallCommand renders the command that takes an app back off a host.
// It goes through the same parameters as an install, because undoing one
// usually needs to know what it was told: the port it took, the user it made,
// the directory it unpacked into.
func BuildUninstallCommand(app *store.App, host *store.Host, submitted map[string]string) (command string, params map[string]string, err error) {
	return buildFrom(app.UninstallCommand, app, host, submitted)
}

func buildFrom(template string, app *store.App, host *store.Host, submitted map[string]string) (command string, params map[string]string, err error) {
	values, params, err := ResolveParams(app, host, submitted)
	if err != nil {
		return "", nil, err
	}
	rendered, err := Render(template, values, true)
	if err != nil {
		return "", nil, err
	}
	return rendered, params, nil
}

// prelude makes install and uninstall commands behave the same everywhere: a
// failing curl in `curl ... | sudo bash` must fail the deployment rather than
// being masked by bash's exit status, and package managers must not stop to
// ask questions.
const prelude = `set -o pipefail 2>/dev/null || true
export DEBIAN_FRONTEND=noninteractive
`

// WrapCommand returns what actually gets sent over SSH.
func WrapCommand(command string) string {
	return prelude + command
}

func dedupe(in []string) []string {
	seen := map[string]bool{}
	out := in[:0]
	for _, s := range in {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}
