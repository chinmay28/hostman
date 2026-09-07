package api

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"net/http"
	"strings"
	"sync"
	"time"
)

// sessionCookie holds the token issued after a successful PIN login.
const sessionCookie = "deployer_session"

const sessionTTL = 30 * 24 * time.Hour

// PinAuth is an optional single-user gate. HostMan runs unauthenticated by
// default (LAN and Tailscale only); setting a PIN turns this on without any
// other change to the API.
type PinAuth struct {
	pin string

	mu     sync.Mutex
	tokens map[string]time.Time
}

// NewPinAuth returns nil when pin is empty, meaning "no authentication".
func NewPinAuth(pin string) *PinAuth {
	if pin == "" {
		return nil
	}
	return &PinAuth{pin: pin, tokens: map[string]time.Time{}}
}

// Middleware rejects unauthenticated requests. A nil *PinAuth is a no-op, so
// callers can wrap unconditionally.
func (a *PinAuth) Middleware(next http.Handler) http.Handler {
	if a == nil {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if isPublicPath(r) || a.authenticated(r) {
			next.ServeHTTP(w, r)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/api/") {
			writeError(w, http.StatusUnauthorized, "PIN required")
			return
		}
		// Let the PWA load; it will show its own login screen.
		next.ServeHTTP(w, r)
	})
}

// isPublicPath lists what stays reachable without a session.
func isPublicPath(r *http.Request) bool {
	switch r.URL.Path {
	case "/api/health", "/api/session":
		return true
	}
	return !strings.HasPrefix(r.URL.Path, "/api/")
}

func (a *PinAuth) authenticated(r *http.Request) bool {
	c, err := r.Cookie(sessionCookie)
	if err != nil || c.Value == "" {
		return false
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	expiry, ok := a.tokens[c.Value]
	if !ok {
		return false
	}
	if time.Now().After(expiry) {
		delete(a.tokens, c.Value)
		return false
	}
	return true
}

func (a *PinAuth) issue() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	token := base64.RawURLEncoding.EncodeToString(buf)
	a.mu.Lock()
	defer a.mu.Unlock()
	// Drop expired tokens while we hold the lock; the set is tiny.
	for t, exp := range a.tokens {
		if time.Now().After(exp) {
			delete(a.tokens, t)
		}
	}
	a.tokens[token] = time.Now().Add(sessionTTL)
	return token, nil
}

// handleSessionStatus tells the UI whether a PIN is required and whether this
// browser already has a valid session.
func (s *Server) handleSessionStatus(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]bool{
		"required":      s.Auth != nil,
		"authenticated": s.Auth == nil || s.Auth.authenticated(r),
	})
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	if s.Auth == nil {
		writeJSON(w, http.StatusOK, map[string]bool{"authenticated": true})
		return
	}
	var body struct {
		PIN string `json:"pin"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if subtle.ConstantTimeCompare([]byte(body.PIN), []byte(s.Auth.pin)) != 1 {
		writeError(w, http.StatusUnauthorized, "incorrect PIN")
		return
	}
	token, err := s.Auth.issue()
	if err != nil {
		s.Log.Error("api: issue session", "err", err)
		writeError(w, http.StatusInternalServerError, "could not start session")
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Expires:  time.Now().Add(sessionTTL),
	})
	writeJSON(w, http.StatusOK, map[string]bool{"authenticated": true})
}
