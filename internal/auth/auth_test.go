package auth

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/zlylong/EdgeRouteGW/internal/store"
)

func newSvc(t *testing.T) (*Service, string) {
	t.Helper()
	ctx := context.Background()
	dir := t.TempDir()
	st, err := store.Open(ctx, filepath.Join(dir, "a.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if _, err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	s := New(st, dir, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err := s.Bootstrap(ctx); err != nil {
		t.Fatal(err)
	}
	pw, err := os.ReadFile(s.BootstrapPasswordFile())
	if err != nil {
		t.Fatal(err)
	}
	return s, strings.TrimSpace(string(pw))
}

func TestHashAndVerify(t *testing.T) {
	h, err := HashPassword("correct horse")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(h, "$argon2id$v=19$m=65536,t=3,p=1$") {
		t.Fatalf("hash format %q", h)
	}
	ok, err := VerifyPassword(h, "correct horse")
	if err != nil || !ok {
		t.Fatalf("verify %v %v", ok, err)
	}
	ok, _ = VerifyPassword(h, "wrong")
	if ok {
		t.Fatal("wrong password verified")
	}
	if _, err := VerifyPassword("$bcrypt$x", "x"); err == nil {
		t.Fatal("foreign format accepted")
	}
}

func TestBootstrapLoginChangePassword(t *testing.T) {
	s, pw := newSvc(t)
	ctx := context.Background()
	if len(pw) != 20 {
		t.Fatalf("bootstrap password %q", pw)
	}
	st, _ := os.Stat(s.BootstrapPasswordFile())
	if st.Mode().Perm() != 0o600 {
		t.Fatalf("bootstrap file perm %o", st.Mode().Perm())
	}
	// Second bootstrap is a no-op (does not rotate the password).
	if err := s.Bootstrap(ctx); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.Login(ctx, "nope", "10.0.0.1", "ua"); err != ErrBadCredentials {
		t.Fatalf("expected bad credentials, got %v", err)
	}
	sess, csrf, err := s.Login(ctx, pw, "10.0.0.1", "ua")
	if err != nil || sess.ID == "" || csrf == "" {
		t.Fatalf("login %v", err)
	}
	other, _, _ := s.Login(ctx, pw, "10.0.0.2", "ua2")

	if err := s.ChangePassword(ctx, sess.ID, pw, "short"); err == nil {
		t.Fatal("weak password accepted")
	}
	if err := s.ChangePassword(ctx, sess.ID, "wrong", "longenoughpassword"); err != ErrBadCredentials {
		t.Fatalf("expected bad credentials, got %v", err)
	}
	if err := s.ChangePassword(ctx, sess.ID, pw, "longenoughpassword"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(s.BootstrapPasswordFile()); !os.IsNotExist(err) {
		t.Fatal("bootstrap file must be removed after password change")
	}
	admin, _ := s.Store.Admin(ctx)
	if admin.Bootstrap {
		t.Fatal("bootstrap flag still set")
	}
	if _, err := s.Store.GetSession(ctx, other.ID); err == nil {
		t.Fatal("other sessions must be revoked on password change")
	}
	if _, err := s.Store.GetSession(ctx, sess.ID); err != nil {
		t.Fatal("current session must survive password change")
	}
	if _, _, err := s.Login(ctx, "longenoughpassword", "10.0.0.1", "ua"); err != nil {
		t.Fatalf("login with new password: %v", err)
	}
}

func TestLimiterLocksAfterTenFailures(t *testing.T) {
	s, _ := newSvc(t)
	s.limiter = newLimiter()
	for i := 0; i < limitLockAfter; i++ {
		s.limiter.fail("1.2.3.4")
	}
	if s.limiter.check("1.2.3.4") >= 0 {
		t.Fatal("expected lock")
	}
	if _, _, err := s.Login(context.Background(), "x", "1.2.3.4", ""); err != ErrRateLimited {
		t.Fatalf("expected rate limit, got %v", err)
	}
	if s.limiter.check("5.6.7.8") != 0 {
		t.Fatal("other ip must be unaffected")
	}
}

func TestMiddlewareCookieCSRFAndTokens(t *testing.T) {
	s, pw := newSvc(t)
	ctx := context.Background()
	sess, csrf, err := s.Login(ctx, pw, "10.0.0.1", "ua")
	if err != nil {
		t.Fatal(err)
	}
	var seen Principal
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen, _ = FromContext(r.Context())
		w.WriteHeader(204)
	})
	h := s.Middleware(func(r *http.Request) bool { return r.URL.Path == "/open" })(next)

	do := func(method, path string, cookies bool, csrfHeader, bearer string) int {
		req := httptest.NewRequest(method, path, nil)
		if cookies {
			req.AddCookie(&http.Cookie{Name: SessionCookie, Value: sess.ID})
			req.AddCookie(&http.Cookie{Name: CSRFCookie, Value: csrf})
		}
		if csrfHeader != "" {
			req.Header.Set(CSRFHeader, csrfHeader)
		}
		if bearer != "" {
			req.Header.Set("Authorization", "Bearer "+bearer)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec.Code
	}
	if do("GET", "/open", false, "", "") != 204 {
		t.Fatal("exempt path blocked")
	}
	if do("GET", "/x", false, "", "") != 401 {
		t.Fatal("anonymous allowed")
	}
	if do("GET", "/x", true, "", "") != 204 || seen.Kind != "session" || !seen.Bootstrap {
		t.Fatalf("cookie GET failed: %+v", seen)
	}
	if do("POST", "/x", true, "", "") != 403 {
		t.Fatal("POST without CSRF header must be refused")
	}
	if do("POST", "/x", true, "wrong", "") != 403 {
		t.Fatal("POST with wrong CSRF header must be refused")
	}
	if do("POST", "/x", true, csrf, "") != 204 {
		t.Fatal("POST with CSRF header refused")
	}

	ro, roSecret, err := s.CreateToken(ctx, "ci", "read", 0)
	if err != nil {
		t.Fatal(err)
	}
	_, rwSecret, _ := s.CreateToken(ctx, "deploy", "write", time.Hour)
	if do("GET", "/x", false, "", roSecret) != 204 || seen.Kind != "token" || seen.Name != "ci" {
		t.Fatalf("read token GET failed: %+v", seen)
	}
	if do("POST", "/x", false, "", roSecret) != 403 {
		t.Fatal("read token must not mutate")
	}
	if do("POST", "/x", false, "", rwSecret) != 204 {
		t.Fatal("write token POST refused")
	}
	if do("GET", "/x", false, "", "ergw_bogus") != 401 || do("GET", "/x", false, "", "not-a-token") != 401 {
		t.Fatal("bogus token accepted")
	}
	if err := s.Store.DeleteAPIToken(ctx, ro.ID); err != nil {
		t.Fatal(err)
	}
	if do("GET", "/x", false, "", roSecret) != 401 {
		t.Fatal("revoked token still accepted")
	}
	if _, _, err := s.CreateToken(ctx, "", "read", 0); err == nil {
		t.Fatal("empty token name accepted")
	}
}
