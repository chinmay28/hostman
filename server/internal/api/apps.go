package api

import (
	"net/http"
	"regexp"
	"strings"

	"github.com/chinmay28/deployer/server/internal/deploy"
	"github.com/chinmay28/deployer/server/internal/store"
)

var paramNamePattern = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_]*$`)

// appInput is the editable shape of an app.
type appInput struct {
	Name             string        `json:"name"`
	Description      string        `json:"description"`
	InstallCommand   string        `json:"installCommand"`
	UninstallCommand string        `json:"uninstallCommand"`
	Params           []store.Param `json:"params"`
	HealthType       string        `json:"healthType"`
	HealthTarget     string        `json:"healthTarget"`
}

func (in *appInput) normalize() string {
	in.Name = strings.TrimSpace(in.Name)
	in.Description = strings.TrimSpace(in.Description)
	in.InstallCommand = strings.TrimSpace(in.InstallCommand)
	in.UninstallCommand = strings.TrimSpace(in.UninstallCommand)
	in.HealthTarget = strings.TrimSpace(in.HealthTarget)
	if in.HealthType == "" {
		in.HealthType = store.HealthNone
	}
	if in.Params == nil {
		in.Params = []store.Param{}
	}

	switch {
	case in.Name == "":
		return "name is required"
	case in.InstallCommand == "":
		return "an install command is required, for example: curl -fsSL https://example.com/quickstart.sh | sudo bash"
	}

	seen := map[string]bool{}
	for i := range in.Params {
		p := &in.Params[i]
		p.Name = strings.TrimSpace(p.Name)
		p.Label = strings.TrimSpace(p.Label)
		p.Default = strings.TrimSpace(p.Default)
		if !paramNamePattern.MatchString(p.Name) {
			return "parameter names must start with a letter and contain only letters, digits and underscores: " + p.Name
		}
		if seen[p.Name] {
			return "duplicate parameter: " + p.Name
		}
		seen[p.Name] = true
		if p.Label == "" {
			p.Label = p.Name
		}
	}

	switch in.HealthType {
	case store.HealthNone:
		in.HealthTarget = ""
	case store.HealthHTTP:
		if in.HealthTarget == "" {
			return "an HTTP health check needs a URL, for example: http://{HOST}:8787/"
		}
		if !strings.HasPrefix(in.HealthTarget, "http://") && !strings.HasPrefix(in.HealthTarget, "https://") {
			return "the health check URL must start with http:// or https://"
		}
	case store.HealthSystemd:
		if in.HealthTarget == "" {
			return "a systemd health check needs a unit name, for example: countroster.service"
		}
	default:
		return "health check type must be none, http or systemd"
	}

	// Catch placeholder mistakes now rather than at deploy time. An uninstall
	// command is optional, but one that is there gets the same treatment: a
	// command nobody discovers is wrong until they are trying to remove
	// something is the worst time to find out.
	values := builtinPlaceholders()
	for _, p := range in.Params {
		values[p.Name] = p.Default
	}
	for _, c := range []struct{ label, command string }{
		{"install command", in.InstallCommand},
		{"uninstall command", in.UninstallCommand},
	} {
		if c.command == "" {
			continue
		}
		if err := deploy.ValidateShellTemplate(c.command); err != nil {
			return c.label + ": " + err.Error()
		}
		if _, err := deploy.Render(c.command, values, true); err != nil {
			return c.label + " has " + err.Error() + " — declare it as a parameter first"
		}
	}
	if in.HealthTarget != "" {
		if _, err := deploy.RenderTarget(in.HealthTarget, values, false); err != nil {
			return "health check target has " + err.Error()
		}
	}
	return ""
}

// builtinPlaceholders are always available to a template.
func builtinPlaceholders() map[string]string {
	return map[string]string{
		deploy.VarHost:     "",
		deploy.VarHostName: "",
		deploy.VarUser:     "",
	}
}

func (in *appInput) apply(a *store.App) {
	a.Name = in.Name
	a.Description = in.Description
	a.InstallCommand = in.InstallCommand
	a.UninstallCommand = in.UninstallCommand
	a.Params = in.Params
	a.HealthType = in.HealthType
	a.HealthTarget = in.HealthTarget
}

func (s *Server) handleListApps(w http.ResponseWriter, r *http.Request) {
	apps, err := s.DB.ListApps(r.Context())
	if err != nil {
		s.writeStoreError(w, err, "list apps")
		return
	}
	writeJSON(w, http.StatusOK, apps)
}

func (s *Server) handleGetApp(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid app id")
		return
	}
	app, err := s.DB.GetApp(r.Context(), id)
	if err != nil {
		s.writeStoreError(w, err, "get app")
		return
	}
	writeJSON(w, http.StatusOK, app)
}

func (s *Server) handleCreateApp(w http.ResponseWriter, r *http.Request) {
	var in appInput
	if err := decodeJSON(r, &in); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if msg := in.normalize(); msg != "" {
		writeError(w, http.StatusBadRequest, msg)
		return
	}
	app := &store.App{}
	in.apply(app)
	created, err := s.DB.CreateApp(r.Context(), app)
	if err != nil {
		s.writeStoreError(w, err, "create app")
		return
	}
	writeJSON(w, http.StatusCreated, created)
}

func (s *Server) handleUpdateApp(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid app id")
		return
	}
	app, err := s.DB.GetApp(r.Context(), id)
	if err != nil {
		s.writeStoreError(w, err, "update app")
		return
	}
	in := appInput{
		Name: app.Name, Description: app.Description, InstallCommand: app.InstallCommand,
		UninstallCommand: app.UninstallCommand, Params: app.Params,
		HealthType: app.HealthType, HealthTarget: app.HealthTarget,
	}
	if err := decodeJSON(r, &in); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if msg := in.normalize(); msg != "" {
		writeError(w, http.StatusBadRequest, msg)
		return
	}
	in.apply(app)
	if err := s.DB.UpdateApp(r.Context(), app); err != nil {
		s.writeStoreError(w, err, "update app")
		return
	}
	writeJSON(w, http.StatusOK, app)
}

// handleDeleteApp removes an app from HostMan. Nothing is uninstalled: an app
// that is still on a host is taken off it one host at a time, through
// handleUninstall, because that is a command that can fail and has a log worth
// watching.
func (s *Server) handleDeleteApp(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid app id")
		return
	}
	if err := s.DB.DeleteApp(r.Context(), id); err != nil {
		s.writeStoreError(w, err, "delete app")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
