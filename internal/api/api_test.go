package api

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	v2 "github.com/zlylong/EdgeRouteGW/internal/api/v2"
	"github.com/zlylong/EdgeRouteGW/internal/auth"
	"github.com/zlylong/EdgeRouteGW/internal/events"
	"github.com/zlylong/EdgeRouteGW/internal/paths"
	"github.com/zlylong/EdgeRouteGW/internal/reconcile"
	"github.com/zlylong/EdgeRouteGW/internal/render"
	"github.com/zlylong/EdgeRouteGW/internal/store"
)

func newTestServer(t *testing.T) *httptest.Server {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "api.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if _, err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	bus := events.New(16)
	p := paths.New(t.TempDir())
	rec := reconcile.New(st, nil, render.Deps{Paths: p}, nil, bus, log)
	h := NewHandler(Deps{Store: st, Reconciler: rec, Bus: bus, Paths: p, Log: log, StartedAt: time.Now().Add(-5 * time.Second)})
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return srv
}

func TestState(t *testing.T) {
	srv := newTestServer(t)
	res, err := http.Get(srv.URL + "/api/v2/state")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		t.Fatalf("status %d", res.StatusCode)
	}
	if ct := res.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Fatalf("content-type %q", ct)
	}
	if res.Header.Get("X-Request-ID") == "" {
		t.Fatal("missing X-Request-ID")
	}
	if res.Header.Get("Content-Security-Policy") == "" || res.Header.Get("X-Content-Type-Options") != "nosniff" {
		t.Fatal("security headers missing")
	}
	var body v2.StateResponse
	if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body.Mode != "A" || body.Generation != 0 || body.SchemaVersion != "0002_builtin_ruleset" || body.UptimeSeconds < 5 {
		t.Fatalf("unexpected state: %+v", body)
	}
}

func TestRequestIDEcho(t *testing.T) {
	srv := newTestServer(t)
	req, _ := http.NewRequest("GET", srv.URL+"/healthz", nil)
	req.Header.Set("X-Request-ID", "trace-123_ABC")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if got := res.Header.Get("X-Request-ID"); got != "trace-123_ABC" {
		t.Fatalf("request id not echoed: %q", got)
	}
	req.Header.Set("X-Request-ID", "bad id with spaces")
	res, _ = http.DefaultClient.Do(req)
	res.Body.Close()
	if got := res.Header.Get("X-Request-ID"); got == "bad id with spaces" || got == "" {
		t.Fatalf("invalid request id should be replaced, got %q", got)
	}
}

func TestUnknownAPIIsJSON404(t *testing.T) {
	srv := newTestServer(t)
	res, err := http.Get(srv.URL + "/api/v2/nope")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != 404 {
		t.Fatalf("status %d", res.StatusCode)
	}
	var body ErrorBody
	if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
		t.Fatalf("404 under /api must be JSON: %v", err)
	}
	if body.Error.Code != CodeNotFound {
		t.Fatalf("code %q", body.Error.Code)
	}
}

func TestMethodNotAllowed(t *testing.T) {
	srv := newTestServer(t)
	res, err := http.Post(srv.URL+"/api/v2/state", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusMethodNotAllowed || res.Header.Get("Allow") != "GET" {
		t.Fatalf("status %d allow %q", res.StatusCode, res.Header.Get("Allow"))
	}
}

func TestSPAFallback(t *testing.T) {
	srv := newTestServer(t)
	for _, p := range []string{"/", "/nodes", "/routing/sets/abc"} {
		res, err := http.Get(srv.URL + p)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(res.Body)
		res.Body.Close()
		if res.StatusCode != 200 || !strings.HasPrefix(res.Header.Get("Content-Type"), "text/html") || !strings.Contains(string(b), "EdgeRouteGW") {
			t.Fatalf("%s: status %d ct %q", p, res.StatusCode, res.Header.Get("Content-Type"))
		}
		if res.Header.Get("Cache-Control") != "no-store" {
			t.Fatalf("%s: index must be no-store, got %q", p, res.Header.Get("Cache-Control"))
		}
	}
}

func TestHealthz(t *testing.T) {
	srv := newTestServer(t)
	res, err := http.Get(srv.URL + "/healthz")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		t.Fatalf("status %d", res.StatusCode)
	}
}

func TestAuthFlowThroughHandler(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	st, err := store.Open(ctx, filepath.Join(dir, "api.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if _, err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	bus := events.New(16)
	p := paths.New(dir)
	rec := reconcile.New(st, nil, render.Deps{Paths: p}, nil, bus, log)
	authSvc := auth.New(st, dir, log)
	if err := authSvc.Bootstrap(ctx); err != nil {
		t.Fatal(err)
	}
	pwb, _ := os.ReadFile(authSvc.BootstrapPasswordFile())
	pw := strings.TrimSpace(string(pwb))
	srv := httptest.NewServer(NewHandler(Deps{Store: st, Reconciler: rec, Bus: bus, Paths: p, Log: log, StartedAt: time.Now(), Auth: authSvc}))
	t.Cleanup(srv.Close)
	jar, _ := cookiejar.New(nil)
	client := &http.Client{Jar: jar}

	// Anonymous: state is 401 JSON, /me says unauthenticated, SPA still served.
	res, _ := client.Get(srv.URL + "/api/v2/state")
	if res.StatusCode != 401 {
		t.Fatalf("anonymous state %d", res.StatusCode)
	}
	res.Body.Close()
	res, _ = client.Get(srv.URL + "/api/v2/auth/me")
	var me v2.MeResponse
	_ = json.NewDecoder(res.Body).Decode(&me)
	res.Body.Close()
	if me.Authenticated {
		t.Fatal("anonymous /me must say unauthenticated")
	}
	res, _ = client.Get(srv.URL + "/nodes")
	if res.StatusCode != 200 {
		t.Fatal("SPA must be served without login")
	}
	res.Body.Close()

	// Wrong then right password.
	res, _ = client.Post(srv.URL+"/api/v2/auth/login", "application/json", strings.NewReader(`{"password":"nope"}`))
	if res.StatusCode != 401 {
		t.Fatalf("bad login %d", res.StatusCode)
	}
	res.Body.Close()
	res, _ = client.Post(srv.URL+"/api/v2/auth/login", "application/json", strings.NewReader(`{"password":"`+pw+`"}`))
	if res.StatusCode != 200 {
		t.Fatalf("login %d", res.StatusCode)
	}
	_ = json.NewDecoder(res.Body).Decode(&me)
	res.Body.Close()
	if !me.Authenticated || !me.MustChangePassword || me.CSRFToken == "" {
		t.Fatalf("login response %+v", me)
	}
	var sessionCookie *http.Cookie
	for _, c := range jar.Cookies(mustURL(srv.URL)) {
		if c.Name == auth.SessionCookie {
			sessionCookie = c
		}
	}
	if sessionCookie == nil {
		t.Fatal("session cookie not set")
	}

	// /me is exempt from rejection but must still see the session.
	res, _ = client.Get(srv.URL + "/api/v2/auth/me")
	_ = json.NewDecoder(res.Body).Decode(&me)
	res.Body.Close()
	if !me.Authenticated || me.Kind != "session" || me.CSRFToken == "" {
		t.Fatalf("/me after login %+v", me)
	}

	// Read works with the cookie; a write needs the CSRF header.
	res, _ = client.Get(srv.URL + "/api/v2/state")
	if res.StatusCode != 200 {
		t.Fatalf("state after login %d", res.StatusCode)
	}
	res.Body.Close()
	req, _ := http.NewRequest("POST", srv.URL+"/api/v2/rule-sets", strings.NewReader(`{"name":"x"}`))
	req.Header.Set("Content-Type", "application/json")
	res, _ = client.Do(req)
	if res.StatusCode != 403 {
		t.Fatalf("write without CSRF %d", res.StatusCode)
	}
	res.Body.Close()
	req, _ = http.NewRequest("POST", srv.URL+"/api/v2/rule-sets", strings.NewReader(`{"name":"x"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(auth.CSRFHeader, me.CSRFToken)
	res, _ = client.Do(req)
	if res.StatusCode != 201 {
		t.Fatalf("write with CSRF %d", res.StatusCode)
	}
	res.Body.Close()
	cs, _ := st.ListChangesets(ctx, 1)
	if len(cs) != 1 || cs[0].Actor != "admin" {
		t.Fatalf("actor should be the session principal: %+v", cs)
	}

	// Logout clears the session.
	req, _ = http.NewRequest("POST", srv.URL+"/api/v2/auth/logout", nil)
	req.Header.Set(auth.CSRFHeader, me.CSRFToken)
	res, _ = client.Do(req)
	res.Body.Close()
	res, _ = client.Get(srv.URL + "/api/v2/state")
	if res.StatusCode != 401 {
		t.Fatalf("state after logout %d", res.StatusCode)
	}
	res.Body.Close()
}

func mustURL(s string) *url.URL {
	u, err := url.Parse(s)
	if err != nil {
		panic(err)
	}
	return u
}
