package v2

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/zlylong/EdgeRouteGW/internal/auth"
	"github.com/zlylong/EdgeRouteGW/internal/store"
)

// registerAuth mounts the auth endpoints. Login is exempt from the auth
// middleware (see api.NewHandler); everything else here requires a session.
func (d Deps) registerAuth(r *router) {
	r.handle(http.MethodPost, "/api/v2/auth/login", d.login)
	r.handle(http.MethodPost, "/api/v2/auth/logout", d.logout)
	r.handle(http.MethodGet, "/api/v2/auth/me", d.me)
	r.handle(http.MethodPost, "/api/v2/auth/password", d.changePassword)
	r.handle(http.MethodGet, "/api/v2/auth/sessions", d.listSessions)
	r.handle(http.MethodDelete, "/api/v2/auth/sessions/{id}", d.deleteSession)
	r.handle(http.MethodGet, "/api/v2/auth/tokens", d.listTokens)
	r.handle(http.MethodPost, "/api/v2/auth/tokens", d.createToken)
	r.handle(http.MethodDelete, "/api/v2/auth/tokens/{id}", d.deleteToken)
}

// MeResponse describes the caller.
type MeResponse struct {
	Authenticated bool   `json:"authenticated"`
	Kind          string `json:"kind,omitempty"`
	Name          string `json:"name,omitempty"`
	Scope         string `json:"scope,omitempty"`
	// MustChangePassword is true while the generated bootstrap password is
	// still in use; the UI forces a change before anything else.
	MustChangePassword bool   `json:"must_change_password"`
	CSRFToken          string `json:"csrf_token,omitempty"`
}

func (d Deps) me(w http.ResponseWriter, r *http.Request) {
	p, ok := auth.FromContext(r.Context())
	if !ok {
		writeJSON(w, http.StatusOK, MeResponse{})
		return
	}
	res := MeResponse{Authenticated: true, Kind: p.Kind, Name: p.Name, Scope: p.Scope, MustChangePassword: p.Bootstrap}
	if c, err := r.Cookie(auth.CSRFCookie); err == nil {
		res.CSRFToken = c.Value
	}
	writeJSON(w, http.StatusOK, res)
}

func (d Deps) login(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Password string `json:"password"`
	}
	if err := decode(r, &in); err != nil {
		d.fail(w, r, err)
		return
	}
	sess, csrf, err := d.Auth.Login(r.Context(), in.Password, auth.RemoteIP(r), r.UserAgent())
	switch {
	case errors.Is(err, auth.ErrBadCredentials):
		writeErr(w, http.StatusUnauthorized, "BAD_CREDENTIALS", "wrong password", nil)
		return
	case errors.Is(err, auth.ErrRateLimited):
		w.Header().Set("Retry-After", "900")
		writeErr(w, http.StatusTooManyRequests, "RATE_LIMITED", err.Error(), nil)
		return
	case err != nil:
		d.fail(w, r, err)
		return
	}
	d.Auth.SetSessionCookies(w, r, sess, csrf)
	admin, _ := d.Store.Admin(r.Context())
	writeJSON(w, http.StatusOK, MeResponse{Authenticated: true, Kind: "session", Name: "admin", Scope: "write", MustChangePassword: admin.Bootstrap, CSRFToken: csrf})
}

func (d Deps) logout(w http.ResponseWriter, r *http.Request) {
	if p, ok := auth.FromContext(r.Context()); ok && p.Kind == "session" {
		_ = d.Auth.Logout(r.Context(), p.ID)
	}
	d.Auth.ClearSessionCookies(w)
	writeJSON(w, http.StatusOK, MeResponse{})
}

func (d Deps) changePassword(w http.ResponseWriter, r *http.Request) {
	p, ok := auth.FromContext(r.Context())
	if !ok || p.Kind != "session" {
		writeErr(w, http.StatusForbidden, "SESSION_REQUIRED", "password changes require a browser session", nil)
		return
	}
	var in struct {
		OldPassword string `json:"old_password"`
		NewPassword string `json:"new_password"`
		KeepTokens  bool   `json:"keep_tokens"` // default: revoke every API token
	}
	if err := decode(r, &in); err != nil {
		d.fail(w, r, err)
		return
	}
	err := d.Auth.ChangePassword(r.Context(), p.ID, in.OldPassword, in.NewPassword, in.KeepTokens)
	var pe *auth.PolicyError
	switch {
	case errors.Is(err, auth.ErrBadCredentials):
		writeErr(w, http.StatusUnauthorized, "BAD_CREDENTIALS", "current password is wrong", nil)
	case errors.As(err, &pe):
		writeErr(w, http.StatusUnprocessableEntity, codeValidation, pe.Msg, map[string]any{"field": "new_password"})
	case err != nil:
		d.fail(w, r, err)
	default:
		writeJSON(w, http.StatusOK, map[string]any{"changed": true})
	}
}

// SessionView hides the session id (it is the bearer secret) and marks the
// caller's own session.
type SessionView struct {
	ID         string    `json:"id"` // short prefix, enough to revoke
	Current    bool      `json:"current"`
	CreatedAt  time.Time `json:"created_at"`
	LastSeenAt time.Time `json:"last_seen_at"`
	ExpiresAt  time.Time `json:"expires_at"`
	RemoteIP   string    `json:"remote_ip"`
	UserAgent  string    `json:"user_agent"`
}

func (d Deps) listSessions(w http.ResponseWriter, r *http.Request) {
	p, _ := auth.FromContext(r.Context())
	sessions, err := d.Store.ListSessions(r.Context())
	if err != nil {
		d.fail(w, r, err)
		return
	}
	items := make([]SessionView, 0, len(sessions))
	for _, s := range sessions {
		items = append(items, SessionView{ID: shortID(s.ID), Current: s.ID == p.ID, CreatedAt: s.CreatedAt, LastSeenAt: s.LastSeenAt, ExpiresAt: s.ExpiresAt, RemoteIP: s.RemoteIP, UserAgent: s.UserAgent})
	}
	writeJSON(w, http.StatusOK, listResp[SessionView]{Items: items})
}

func shortID(id string) string {
	if len(id) > 12 {
		return id[:12]
	}
	return id
}

func (d Deps) deleteSession(w http.ResponseWriter, r *http.Request) {
	prefix := r.PathValue("id")
	sessions, err := d.Store.ListSessions(r.Context())
	if err != nil {
		d.fail(w, r, err)
		return
	}
	for _, s := range sessions {
		if strings.HasPrefix(s.ID, prefix) {
			if err := d.Store.DeleteSession(r.Context(), s.ID); err != nil {
				d.fail(w, r, err)
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"deleted": 1})
			return
		}
	}
	d.fail(w, r, store.ErrNotFound)
}

// TokenInput creates an API token.
type TokenInput struct {
	Name      string `json:"name"`
	Scope     string `json:"scope"`
	ExpiresIn string `json:"expires_in"` // Go duration such as "720h"; empty = never
}

func (d Deps) listTokens(w http.ResponseWriter, r *http.Request) {
	items, err := d.Store.ListAPITokens(r.Context())
	if err != nil {
		d.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, listResp[store.APIToken]{Items: items})
}

func (d Deps) createToken(w http.ResponseWriter, r *http.Request) {
	if p, ok := auth.FromContext(r.Context()); !ok || p.Kind != "session" {
		writeErr(w, http.StatusForbidden, "SESSION_REQUIRED", "tokens can only be created from a browser session", nil)
		return
	}
	var in TokenInput
	if err := decode(r, &in); err != nil {
		d.fail(w, r, err)
		return
	}
	var ttl time.Duration
	if in.ExpiresIn != "" {
		v, err := time.ParseDuration(in.ExpiresIn)
		if err != nil || v <= 0 {
			d.fail(w, r, invalid("expires_in", "must be a positive duration like 720h"))
			return
		}
		ttl = v
	}
	t, cleartext, err := d.Auth.CreateToken(r.Context(), in.Name, strings.ToLower(in.Scope), ttl)
	var pe *auth.PolicyError
	if errors.As(err, &pe) {
		d.fail(w, r, invalid("name", "%s", pe.Msg))
		return
	}
	if err != nil {
		d.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"item": t, "token": cleartext})
}

func (d Deps) deleteToken(w http.ResponseWriter, r *http.Request) {
	if err := d.Store.DeleteAPIToken(r.Context(), r.PathValue("id")); err != nil {
		d.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"deleted": 1})
}
