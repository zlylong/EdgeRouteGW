package auth

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/zlylong/EdgeRouteGW/internal/store"
)

// Cookie names. The session cookie is HttpOnly; the CSRF cookie is readable
// by the SPA, which echoes it in X-CSRF-Token on every mutating request.
const (
	SessionCookie = "ergw_session"
	CSRFCookie    = "ergw_csrf"
	CSRFHeader    = "X-CSRF-Token"

	sessionIdle     = 24 * time.Hour
	sessionAbsolute = 7 * 24 * time.Hour
	tokenPrefix     = "ergw_"
)

// Service owns authentication state.
type Service struct {
	Store   *store.Store
	Log     *slog.Logger
	DataDir string
	// Secure marks cookies Secure when the request arrived over TLS or the
	// operator forces it; cleartext LAN deployments cannot set it.
	ForceSecure bool

	limiter *limiter
}

// New builds a Service.
func New(st *store.Store, dataDir string, log *slog.Logger) *Service {
	return &Service{Store: st, Log: log, DataDir: dataDir, limiter: newLimiter()}
}

// BootstrapPasswordFile is where the generated initial password is written.
func (s *Service) BootstrapPasswordFile() string {
	return filepath.Join(s.DataDir, "bootstrap_password.txt")
}

// Bootstrap ensures an admin credential exists. On first run it generates a
// random password, stores its hash and writes the cleartext to a 0600 file
// the operator reads once; changing the password removes the file.
func (s *Service) Bootstrap(ctx context.Context) error {
	_, err := s.Store.Admin(ctx)
	if err == nil {
		return nil
	}
	if !errors.Is(err, store.ErrNoAdmin) {
		return err
	}
	pw, err := GenerateBootstrapPassword()
	if err != nil {
		return err
	}
	hash, err := HashPassword(pw)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(s.DataDir, 0o750); err != nil {
		return err
	}
	if err := os.WriteFile(s.BootstrapPasswordFile(), []byte(pw+"\n"), 0o600); err != nil {
		return fmt.Errorf("write bootstrap password: %w", err)
	}
	if err := s.Store.SetAdminPassword(ctx, hash, true); err != nil {
		return err
	}
	s.Log.Warn("initial admin password generated; read it once and change it", "file", s.BootstrapPasswordFile())
	return nil
}

// Principal is who is making a request.
type Principal struct {
	Kind      string // "session" or "token"
	ID        string // session id or token id
	Name      string // "admin" or the token name
	Scope     string // "write" for sessions; token scope otherwise
	Bootstrap bool   // session still on the generated password
}

// Actor is the string recorded in changesets.
func (p Principal) Actor() string {
	if p.Kind == "token" {
		return "token:" + p.Name
	}
	return p.Name
}

// CanWrite reports whether the principal may mutate.
func (p Principal) CanWrite() bool { return p.Scope == "write" }

type ctxKey int

const principalKey ctxKey = iota

// FromContext returns the authenticated principal, if any.
// WithPrincipal attaches a principal (tests, trusted embedders).
func WithPrincipal(ctx context.Context, p Principal) context.Context {
	return context.WithValue(ctx, principalKey, p)
}

func FromContext(ctx context.Context) (Principal, bool) {
	p, ok := ctx.Value(principalKey).(Principal)
	return p, ok
}

// ErrBadCredentials is returned by Login for a wrong password.
var ErrBadCredentials = errors.New("invalid password")

// ErrRateLimited is returned when the source has failed too often.
var ErrRateLimited = errors.New("too many failed attempts; try again later")

// Login verifies the password and creates a session. remoteIP feeds the
// brute-force limiter.
func (s *Service) Login(ctx context.Context, password, remoteIP, userAgent string) (store.Session, string, error) {
	if wait := s.limiter.check(remoteIP); wait < 0 {
		return store.Session{}, "", ErrRateLimited
	} else if wait > 0 {
		select {
		case <-time.After(wait):
		case <-ctx.Done():
			return store.Session{}, "", ctx.Err()
		}
	}
	admin, err := s.Store.Admin(ctx)
	if err != nil {
		return store.Session{}, "", err
	}
	ok, err := VerifyPassword(admin.PasswordHash, password)
	if err != nil {
		return store.Session{}, "", err
	}
	if !ok {
		s.limiter.fail(remoteIP)
		s.Log.Warn("login failed", "remote", remoteIP)
		return store.Session{}, "", ErrBadCredentials
	}
	s.limiter.reset(remoteIP)
	id, err := randomToken(32)
	if err != nil {
		return store.Session{}, "", err
	}
	csrf, err := randomToken(24)
	if err != nil {
		return store.Session{}, "", err
	}
	now := time.Now()
	sess := store.Session{ID: id, CreatedAt: now, ExpiresAt: now.Add(sessionIdle), LastSeenAt: now, RemoteIP: remoteIP, UserAgent: truncate(userAgent, 200)}
	if err := s.Store.CreateSession(ctx, sess); err != nil {
		return store.Session{}, "", err
	}
	_ = s.Store.PruneSessions(ctx)
	s.Log.Info("login ok", "remote", remoteIP)
	return sess, csrf, nil
}

// Logout destroys a session.
func (s *Service) Logout(ctx context.Context, sessionID string) error {
	return s.Store.DeleteSession(ctx, sessionID)
}

// ChangePassword verifies the old password, stores the new hash, clears the
// bootstrap flag and file, revokes every other session and, unless
// keepTokens is set, every API token (a token minted during a session
// compromise must not outlive the password change).
func (s *Service) ChangePassword(ctx context.Context, sessionID, oldPw, newPw string, keepTokens bool) error {
	admin, err := s.Store.Admin(ctx)
	if err != nil {
		return err
	}
	ok, err := VerifyPassword(admin.PasswordHash, oldPw)
	if err != nil {
		return err
	}
	if !ok {
		return ErrBadCredentials
	}
	if err := ValidateNewPassword(newPw); err != nil {
		return &PolicyError{Msg: err.Error()}
	}
	hash, err := HashPassword(newPw)
	if err != nil {
		return err
	}
	if err := s.Store.SetAdminPassword(ctx, hash, false); err != nil {
		return err
	}
	_ = os.Remove(s.BootstrapPasswordFile())
	n, _ := s.Store.DeleteOtherSessions(ctx, sessionID)
	var t int64
	if !keepTokens {
		t, _ = s.Store.DeleteAllAPITokens(ctx)
	}
	s.Log.Info("admin password changed", "revoked_sessions", n, "revoked_tokens", t)
	return nil
}

// PolicyError is a client-side mistake (weak password, bad token name).
type PolicyError struct{ Msg string }

func (e *PolicyError) Error() string { return e.Msg }

// CreateToken mints an API token; the cleartext is returned exactly once.
func (s *Service) CreateToken(ctx context.Context, name, scope string, ttl time.Duration) (store.APIToken, string, error) {
	name = strings.TrimSpace(name)
	if name == "" || len(name) > 64 {
		return store.APIToken{}, "", &PolicyError{Msg: "token name must be 1-64 characters"}
	}
	if scope != "read" && scope != "write" {
		return store.APIToken{}, "", &PolicyError{Msg: "scope must be read or write"}
	}
	secret, err := randomToken(32)
	if err != nil {
		return store.APIToken{}, "", err
	}
	t := store.APIToken{ID: uuid.NewString(), Name: name, Scope: scope, CreatedAt: time.Now()}
	if ttl > 0 {
		exp := time.Now().Add(ttl)
		t.ExpiresAt = &exp
	}
	cleartext := tokenPrefix + secret
	if err := s.Store.CreateAPIToken(ctx, t, hashToken(cleartext)); err != nil {
		return store.APIToken{}, "", err
	}
	return t, cleartext, nil
}

func hashToken(cleartext string) string {
	sum := sha256.Sum256([]byte(cleartext))
	return hex.EncodeToString(sum[:])
}

// Authenticate resolves the request's principal from a Bearer token or the
// session cookie. It returns ok=false when neither is valid.
func (s *Service) Authenticate(r *http.Request) (Principal, bool) {
	ctx := r.Context()
	if h := r.Header.Get("Authorization"); strings.HasPrefix(h, "Bearer ") {
		raw := strings.TrimSpace(strings.TrimPrefix(h, "Bearer "))
		if !strings.HasPrefix(raw, tokenPrefix) {
			return Principal{}, false
		}
		t, err := s.Store.APITokenByHash(ctx, hashToken(raw))
		if err != nil {
			return Principal{}, false
		}
		_ = s.Store.TouchAPIToken(ctx, t.ID)
		return Principal{Kind: "token", ID: t.ID, Name: t.Name, Scope: t.Scope}, true
	}
	c, err := r.Cookie(SessionCookie)
	if err != nil || c.Value == "" {
		return Principal{}, false
	}
	sess, err := s.Store.GetSession(ctx, c.Value)
	if err != nil {
		return Principal{}, false
	}
	// Sliding idle expiry capped by the absolute lifetime; touch at most
	// every few minutes to avoid a write per request.
	if time.Since(sess.LastSeenAt) > 5*time.Minute {
		exp := time.Now().Add(sessionIdle)
		if abs := sess.CreatedAt.Add(sessionAbsolute); exp.After(abs) {
			exp = abs
		}
		_ = s.Store.TouchSession(ctx, sess.ID, exp)
	}
	admin, err := s.Store.Admin(ctx)
	if err != nil {
		return Principal{}, false
	}
	return Principal{Kind: "session", ID: sess.ID, Name: "admin", Scope: "write", Bootstrap: admin.Bootstrap}, true
}

// SetSessionCookies writes the session and CSRF cookies.
func (s *Service) SetSessionCookies(w http.ResponseWriter, r *http.Request, sess store.Session, csrf string) {
	secure := s.ForceSecure || r.TLS != nil
	http.SetCookie(w, &http.Cookie{Name: SessionCookie, Value: sess.ID, Path: "/", HttpOnly: true, Secure: secure, SameSite: http.SameSiteStrictMode, Expires: sess.CreatedAt.Add(sessionAbsolute)})
	http.SetCookie(w, &http.Cookie{Name: CSRFCookie, Value: csrf, Path: "/", HttpOnly: false, Secure: secure, SameSite: http.SameSiteStrictMode, Expires: sess.CreatedAt.Add(sessionAbsolute)})
}

// ClearSessionCookies expires both cookies.
func (s *Service) ClearSessionCookies(w http.ResponseWriter) {
	for _, name := range []string{SessionCookie, CSRFCookie} {
		http.SetCookie(w, &http.Cookie{Name: name, Value: "", Path: "/", MaxAge: -1, HttpOnly: name == SessionCookie, SameSite: http.SameSiteStrictMode})
	}
}

// Middleware enforces authentication on everything except the exempt paths.
// Cookie-authenticated mutations must carry the CSRF header matching the
// CSRF cookie; read-scope tokens may only use safe methods.
func (s *Service) Middleware(exempt func(r *http.Request) bool) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			p, ok := s.Authenticate(r)
			if exempt != nil && exempt(r) {
				// Exempt paths (login, /me) still learn who is calling when
				// valid credentials are present, so /me can describe the
				// session; they just never reject.
				if ok {
					r = r.WithContext(context.WithValue(r.Context(), principalKey, p))
				}
				next.ServeHTTP(w, r)
				return
			}
			if !ok {
				w.Header().Set("Cache-Control", "no-store")
				writeJSON(w, http.StatusUnauthorized, `{"error":{"code":"UNAUTHENTICATED","message":"login required"}}`)
				return
			}
			if p.Kind == "session" && p.Bootstrap && !strings.HasPrefix(r.URL.Path, "/api/v2/auth/") {
				// The generated bootstrap password only unlocks the
				// password change itself; the router guard in the UI is
				// not a security boundary.
				writeJSON(w, http.StatusForbidden, `{"error":{"code":"PASSWORD_CHANGE_REQUIRED","message":"change the bootstrap password first"}}`)
				return
			}
			if !safeMethod(r.Method) {
				if p.Kind == "session" {
					c, err := r.Cookie(CSRFCookie)
					if err != nil || c.Value == "" || c.Value != r.Header.Get(CSRFHeader) {
						writeJSON(w, http.StatusForbidden, `{"error":{"code":"CSRF","message":"missing or mismatched CSRF token"}}`)
						return
					}
				}
				if !p.CanWrite() {
					writeJSON(w, http.StatusForbidden, `{"error":{"code":"READ_ONLY","message":"token scope is read-only"}}`)
					return
				}
			}
			next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), principalKey, p)))
		})
	}
}

func safeMethod(m string) bool {
	return m == http.MethodGet || m == http.MethodHead || m == http.MethodOptions
}

func writeJSON(w http.ResponseWriter, status int, body string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_, _ = w.Write([]byte(body + "\n"))
}

// RemoteIP extracts the client IP (no proxy headers: the gateway is the
// edge, and trusting X-Forwarded-For would let any LAN host spoof it).
func RemoteIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func truncate(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}

// limiter tracks login failures per source IP: after 5 failures each attempt
// is delayed 2 s; after 10 the source is locked for 15 minutes.
type limiter struct {
	mu    sync.Mutex
	fails map[string]*failRecord
}

type failRecord struct {
	count int
	last  time.Time
}

const (
	limitDelayAfter = 5
	limitLockAfter  = 10
	limitDelay      = 2 * time.Second
	limitLock       = 15 * time.Minute
)

func newLimiter() *limiter { return &limiter{fails: map[string]*failRecord{}} }

// check returns <0 when locked, >0 for a delay to apply, 0 otherwise.
func (l *limiter) check(ip string) time.Duration {
	l.mu.Lock()
	defer l.mu.Unlock()
	rec, ok := l.fails[ip]
	if !ok {
		return 0
	}
	if time.Since(rec.last) > limitLock {
		delete(l.fails, ip)
		return 0
	}
	switch {
	case rec.count >= limitLockAfter:
		return -1
	case rec.count >= limitDelayAfter:
		return limitDelay
	}
	return 0
}

func (l *limiter) fail(ip string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	rec, ok := l.fails[ip]
	if !ok {
		rec = &failRecord{}
		l.fails[ip] = rec
	}
	rec.count++
	rec.last = time.Now()
}

func (l *limiter) reset(ip string) {
	l.mu.Lock()
	delete(l.fails, ip)
	l.mu.Unlock()
}
