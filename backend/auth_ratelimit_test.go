package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

// loginRequest builds an unauthenticated POST /api/login carrying a wrong
// password and, optionally, a forged X-Forwarded-For.
func loginRequest(forwardedFor string) *http.Request {
	req := httptest.NewRequest(http.MethodPost, "/api/login", strings.NewReader(`{"Password":"wrong-password"}`))
	req.Header.Set("Content-Type", "application/json")
	if forwardedFor != "" {
		req.Header.Set("X-Forwarded-For", forwardedFor)
	}
	return req
}

func TestLoginRateLimitIgnoresForwardedForHeader(t *testing.T) {
	r := setupFeatureSuiteRouter(t)

	// Every request arrives from the same peer but claims a different client
	// address. Keying the counter off that header would hand each request an
	// unused bucket, so neither the delay nor the lockout would ever trigger.
	forged := []string{
		"203.0.113.1", "203.0.113.2", "203.0.113.3",
		"198.51.100.7", "198.51.100.8", "10.9.9.9",
	}
	for _, ff := range forged {
		w := httptest.NewRecorder()
		r.ServeHTTP(w, loginRequest(ff))
		if w.Code != http.StatusUnauthorized {
			t.Fatalf("X-Forwarded-For %s: want 401 got %d body=%s", ff, w.Code, w.Body.String())
		}
	}

	loginAttemptsMu.Lock()
	defer loginAttemptsMu.Unlock()
	if len(loginAttempts) != 1 {
		keys := make([]string, 0, len(loginAttempts))
		for k := range loginAttempts {
			keys = append(keys, k)
		}
		t.Fatalf("forged headers created %d buckets (%v), want 1 keyed on the peer", len(loginAttempts), keys)
	}
	for _, data := range loginAttempts {
		if data.Count != len(forged) {
			t.Errorf("attempt count = %d, want %d: the failures must accumulate in one bucket", data.Count, len(forged))
		}
	}
}

func TestLoginRateLimitPrunesStaleBuckets(t *testing.T) {
	r := setupFeatureSuiteRouter(t)

	loginAttemptsMu.Lock()
	loginAttempts["203.0.113.55"] = &LoginAttempt{Count: 9, LastSeen: time.Now().Add(-loginAttemptWindow - time.Minute)}
	loginAttempts["203.0.113.56"] = &LoginAttempt{Count: 3, LastSeen: time.Now().Add(-2 * loginAttemptWindow)}
	loginAttemptsMu.Unlock()

	w := httptest.NewRecorder()
	r.ServeHTTP(w, loginRequest(""))
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("want 401 got %d", w.Code)
	}

	// Buckets used to be removed only on a successful login, so a failed-login
	// flood left one entry per source address behind forever.
	loginAttemptsMu.Lock()
	defer loginAttemptsMu.Unlock()
	for _, stale := range []string{"203.0.113.55", "203.0.113.56"} {
		if _, ok := loginAttempts[stale]; ok {
			t.Errorf("stale bucket %s survived the prune", stale)
		}
	}
}

func TestLoginRateLimitLocksOutAfterRepeatedFailures(t *testing.T) {
	r := setupFeatureSuiteRouter(t)

	// Seed the bucket for this peer just below the lockout so the test does not
	// have to sit through the 2s delay applied to attempts 7 through 10.
	req := loginRequest("")
	peer, _, _ := strings.Cut(req.RemoteAddr, ":")
	loginAttemptsMu.Lock()
	loginAttempts[peer] = &LoginAttempt{Count: 11, LastSeen: time.Now()}
	loginAttemptsMu.Unlock()

	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusTooManyRequests {
		t.Fatalf("want 429 after exceeding the attempt ceiling, got %d body=%s", w.Code, w.Body.String())
	}
}

func TestBuildRouterDoesNotTrustForwardedFor(t *testing.T) {
	setupFeatureSuiteRouter(t)

	r := NewAppController().BuildRouter()
	var seen string
	r.GET("/__clientip_probe", func(c *gin.Context) {
		seen = c.ClientIP()
		c.Status(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodGet, "/__clientip_probe", nil)
	req.Header.Set("X-Forwarded-For", "203.0.113.99")
	req.Header.Set("X-Real-IP", "203.0.113.98")
	r.ServeHTTP(httptest.NewRecorder(), req)

	// gin trusts every peer as a proxy unless told otherwise, which would make
	// ClientIP() return the forged header and put a caller-controlled value into
	// the source_ip column of every security event.
	peer, _, _ := strings.Cut(req.RemoteAddr, ":")
	if seen != peer {
		t.Errorf("ClientIP() = %q, want the peer address %q: forwarding headers must not be trusted", seen, peer)
	}
}
