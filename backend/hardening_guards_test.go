package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

// The guard and the lock both short-circuit in gin.TestMode, so the tests
// that exercise them have to leave it.
func withReleaseMode(t *testing.T) {
	t.Helper()
	old := gin.Mode()
	gin.SetMode(gin.ReleaseMode)
	t.Cleanup(func() { gin.SetMode(old) })
}

func newGuardTestCtx(method, target string) (*gin.Context, *httptest.ResponseRecorder) {
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(method, target, nil)
	return c, w
}

func TestHighRiskLockGroupSharesConfigWriters(t *testing.T) {
	for _, a := range []string{"apply_config", "mode_switch", "network_config"} {
		if g := highRiskLockGroup(a); g != "config_writers" {
			t.Errorf("%s -> %q, want config_writers", a, g)
		}
	}
	for _, a := range []string{"ospf_settings", "ospf_reset_pending", "hostkey_repin"} {
		if g := highRiskLockGroup(a); g != a {
			t.Errorf("%s -> %q, want a lock of its own", a, g)
		}
	}
}

// apply_config and mode_switch both regenerate the Xray, Mosdns and nftables
// configs, and neither applyMosdnsConfig nor applyNftablesConfig has a lock of
// its own. A per-action lock let the two run concurrently and interleave their
// writes to the same files.
func TestConfigWritersExcludeEachOther(t *testing.T) {
	setupFeatureSuiteRouter(t)
	withReleaseMode(t)

	c1, _ := newGuardTestCtx(http.MethodPost, "/api/mode")
	release, ok := tryAcquireHighRiskMutationLock(c1, "mode_switch")
	if !ok {
		t.Fatal("first acquire failed")
	}
	defer release()

	c2, w2 := newGuardTestCtx(http.MethodPost, "/api/apply")
	if _, ok := tryAcquireHighRiskMutationLock(c2, "apply_config"); ok {
		t.Fatal("apply_config acquired while mode_switch was in flight; they write the same files")
	}
	if w2.Code != http.StatusConflict || !strings.Contains(w2.Body.String(), "HIGH_RISK_ACTION_BUSY") {
		t.Fatalf("want 409 HIGH_RISK_ACTION_BUSY, got %d %s", w2.Code, w2.Body.String())
	}

	// Unrelated actions must not be caught by the shared group.
	c3, _ := newGuardTestCtx(http.MethodPost, "/api/remote_nodes/1/hostkey")
	rel3, ok := tryAcquireHighRiskMutationLock(c3, "hostkey_repin")
	if !ok {
		t.Fatal("hostkey_repin was blocked by the config_writers group")
	}
	rel3()
}

func TestLockReleaseFreesTheGroup(t *testing.T) {
	setupFeatureSuiteRouter(t)
	withReleaseMode(t)

	c1, _ := newGuardTestCtx(http.MethodPost, "/api/mode")
	release, ok := tryAcquireHighRiskMutationLock(c1, "mode_switch")
	if !ok {
		t.Fatal("acquire")
	}
	release()

	c2, _ := newGuardTestCtx(http.MethodPost, "/api/apply")
	rel2, ok := tryAcquireHighRiskMutationLock(c2, "apply_config")
	if !ok {
		t.Fatal("group still held after release")
	}
	rel2()
}

// dig has no "--" terminator, so a name that starts with '-', '+' or '@' is
// an option, a query flag or a server. The validators upstream reject such
// values, but this is the single place a string is handed to a root-run
// subprocess, so it must refuse on its own.
func TestDigRefusesOptionLikeNames(t *testing.T) {
	for _, name := range []string{"-f/etc/passwd", "+trace", "@127.0.0.1", "-", ""} {
		_, err := lookupIPv4WithDNSServer(name, "127.0.0.1", false)
		if err == nil || !strings.Contains(err.Error(), "option-like") {
			t.Errorf("%q: got err=%v, want a refusal before dig is invoked", name, err)
		}
	}
}

// After a re-pin, deploy and check will authenticate to whatever presents
// the new key. That is the one operation that can silently redirect stored
// SSH credentials, and it must not be a bare authenticated POST.
func TestHostKeyRepinRequiresConfirmToken(t *testing.T) {
	setupFeatureSuiteRouter(t)
	withReleaseMode(t)

	c, w := newGuardTestCtx(http.MethodPost, "/api/remote_nodes/1/hostkey")
	c.Params = gin.Params{{Key: "id", Value: "1"}}
	updateRemoteNodeHostKey(c)
	if w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), "HIGH_RISK_CONFIRM_REQUIRED") {
		t.Fatalf("re-pin without confirm token: got %d %s, want 403 HIGH_RISK_CONFIRM_REQUIRED", w.Code, w.Body.String())
	}

	c2, w2 := newGuardTestCtx(http.MethodPost, "/api/remote_nodes/1/hostkey?confirm=APPLY")
	c2.Params = gin.Params{{Key: "id", Value: "1"}}
	updateRemoteNodeHostKey(c2)
	if w2.Code == http.StatusForbidden {
		t.Fatalf("confirm token not honoured: %d %s", w2.Code, w2.Body.String())
	}
}
