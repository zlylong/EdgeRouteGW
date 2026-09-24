package main

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

func TestLoginLockoutHoldsUnderConcurrentBurst(t *testing.T) {
	r := setupFeatureSuiteRouter(t)
	req := loginRequest("")
	peer, _, _ := strings.Cut(req.RemoteAddr, ":")
	loginAttemptsMu.Lock()
	loginAttempts[peer] = &LoginAttempt{Count: 10, LastSeen: time.Now()}
	loginAttemptsMu.Unlock()

	const burst = 8
	var start sync.WaitGroup
	var done sync.WaitGroup
	start.Add(1)
	codes := make([]int, burst)
	for i := 0; i < burst; i++ {
		done.Add(1)
		go func(i int) {
			defer done.Done()
			start.Wait()
			w := httptest.NewRecorder()
			r.ServeHTTP(w, loginRequest(""))
			codes[i] = w.Code
		}(i)
	}
	start.Done()
	done.Wait()

	unauthorized, tooMany := 0, 0
	for _, c := range codes {
		switch c {
		case http.StatusUnauthorized:
			unauthorized++
		case http.StatusTooManyRequests:
			tooMany++
		default:
			t.Fatalf("unexpected status %d in burst", c)
		}
	}
	// Count was 10: exactly one more guess is allowed (the 11th), every
	// concurrent sibling must see the incremented counter and be locked out.
	if unauthorized != 1 || tooMany != burst-1 {
		t.Fatalf("burst of %d at the lockout edge: %d guesses evaluated, %d locked out; want 1 and %d", burst, unauthorized, tooMany, burst-1)
	}

	var n int
	if err := db.QueryRow("SELECT COUNT(*) FROM gateway_events WHERE module='auth' AND event_type IN ('login_failed','login_locked_out')").Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n == 0 {
		t.Fatal("failed/locked-out logins were not recorded as gateway events")
	}
}

func TestBuildXrayDigestURLMatchesDownloadAsset(t *testing.T) {
	asset, err := xrayAssetName()
	if err != nil {
		t.Skip("architecture is not a release target")
	}
	for _, v := range []string{"latest", "", "v26.3.27"} {
		dl, err := buildXrayDownloadURL(v)
		if err != nil {
			t.Fatal(err)
		}
		dg, err := buildXrayDigestURL(v)
		if err != nil {
			t.Fatal(err)
		}
		if dg != dl+".dgst" || !strings.Contains(dg, asset+".dgst") {
			t.Fatalf("digest URL %q does not belong to download %q", dg, dl)
		}
	}
	for _, v := range badVersions {
		if u, err := buildXrayDigestURL(v); err == nil {
			t.Errorf("buildXrayDigestURL(%q) = %s, want error", v, u)
		}
	}
}

func withFakeGitHub(t *testing.T, handler http.HandlerFunc) {
	t.Helper()
	srv := httptest.NewServer(handler)
	oldAPI, oldDL := githubAPIBase, githubDownloadBase
	githubAPIBase, githubDownloadBase = srv.URL, srv.URL
	t.Cleanup(func() {
		githubAPIBase, githubDownloadBase = oldAPI, oldDL
		srv.Close()
	})
}

func TestVersionListsReturnEmptyArrayAndPropagateUpstreamErrors(t *testing.T) {
	r := setupFeatureSuiteRouter(t)
	withFakeGitHub(t, func(w http.ResponseWriter, req *http.Request) {
		switch req.URL.Path {
		case "/repos/XTLS/Xray-core/releases":
			_, _ = w.Write([]byte(`[]`))
		case "/repos/IrineSistiana/mosdns/releases":
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"message":"API rate limit exceeded"}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	})

	w := httptest.NewRecorder()
	r.ServeHTTP(w, authedRequest(http.MethodGet, "/api/xray/versions"))
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"versions":[]`) {
		t.Fatalf("empty release list: code=%d body=%s (want versions:[] not null)", w.Code, w.Body.String())
	}

	w = httptest.NewRecorder()
	r.ServeHTTP(w, authedRequest(http.MethodGet, "/api/mosdns/versions"))
	if w.Code != http.StatusBadGateway {
		t.Fatalf("rate-limited upstream: code=%d body=%s, want 502", w.Code, w.Body.String())
	}
}

func TestGeodataUpdateRejectsMalformedReleaseTag(t *testing.T) {
	setupFeatureSuiteRouter(t)
	withFakeGitHub(t, func(w http.ResponseWriter, req *http.Request) {
		if strings.HasSuffix(req.URL.Path, "/releases/latest") {
			_, _ = w.Write([]byte(`{"tag_name":"../../evil/releases/download/x"}`))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	})
	if _, _, err := getGeoDataVersionAndHash(); err == nil || !strings.Contains(err.Error(), "unexpected geodata release tag") {
		t.Fatalf("malformed tag accepted: err=%v", err)
	}
}

func TestGetRemoteFileContentIsBounded(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.Copy(w, io.LimitReader(zeroReader{}, remoteTextLimit+10))
	}))
	defer srv.Close()
	if _, err := getRemoteFileContent(srv.URL); err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("oversized response accepted: %v", err)
	}
}

type zeroReader struct{}

func (zeroReader) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = 'a'
	}
	return len(p), nil
}

func TestNewHTTPServerTimeouts(t *testing.T) {
	srv := newHTTPServer("127.0.0.1:0", http.NotFoundHandler())
	if srv.ReadHeaderTimeout <= 0 || srv.IdleTimeout <= 0 || srv.MaxHeaderBytes <= 0 {
		t.Fatalf("server lacks timeouts: %+v", srv)
	}
	if srv.WriteTimeout != 0 {
		t.Fatalf("WriteTimeout must stay 0: component updates and deploys are long-running")
	}
}

func TestRouterSetsSecurityCacheAndCompressionHeaders(t *testing.T) {
	setupFeatureSuiteRouter(t)
	r := NewAppController().BuildRouter()
	big := strings.Repeat(`{"k":"value"},`, 400)
	r.GET("/__big_json", func(c *gin.Context) {
		c.Data(http.StatusOK, "application/json", []byte("["+strings.TrimSuffix(big, ",")+"]"))
	})
	r.GET("/__png", func(c *gin.Context) {
		c.Header("Content-Length", "4096")
		c.Data(http.StatusOK, "image/png", bytes.Repeat([]byte{0}, 4096))
	})

	req := httptest.NewRequest(http.MethodGet, "/__big_json", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	for h, want := range map[string]string{
		"X-Content-Type-Options": "nosniff",
		"X-Frame-Options":        "DENY",
		"Referrer-Policy":        "same-origin",
	} {
		if got := w.Header().Get(h); got != want {
			t.Errorf("%s = %q want %q", h, got, want)
		}
	}
	if w.Header().Get("Content-Security-Policy-Report-Only") == "" {
		t.Error("CSP report-only header missing")
	}
	if w.Header().Get("Content-Encoding") != "gzip" {
		t.Fatalf("JSON not gzip-compressed for a gzip-accepting client: %v", w.Header())
	}
	zr, err := gzip.NewReader(w.Body)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(zr)
	var decoded []map[string]string
	if err := json.Unmarshal(body, &decoded); err != nil || len(decoded) != 400 {
		t.Fatalf("decompressed body is not the JSON the handler wrote: err=%v n=%d", err, len(decoded))
	}

	req = httptest.NewRequest(http.MethodGet, "/__big_json", nil)
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Header().Get("Content-Encoding") != "" {
		t.Fatal("response compressed for a client that did not ask for gzip")
	}

	req = httptest.NewRequest(http.MethodGet, "/__png", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Header().Get("Content-Encoding") != "" {
		t.Fatal("binary content type was gzip-compressed")
	}

	for _, tc := range []struct{ path, want string }{
		{"/ui/", "no-cache, no-store, must-revalidate"},
		{"/ui/index.html", "no-cache, no-store, must-revalidate"},
		{"/ui/libs/vue.global.prod.js", "public, max-age=3600"},
	} {
		w = httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, tc.path, nil))
		if got := w.Header().Get("Cache-Control"); got != tc.want {
			t.Errorf("Cache-Control for %s = %q want %q", tc.path, got, tc.want)
		}
	}
}

func TestCheckRemoteNodeTimesOutHungCommand(t *testing.T) {
	r := setupFeatureSuiteRouter(t)
	oldConnect := getRemoteConnect()
	defer func() { setRemoteConnect(oldConnect) }()
	setRemoteConnect(func(host string, port int, user string, authType string, credential string, expectedHostKey string) (remoteSSHClient, error) {
		return &fakeSSHClient{run: func(cmd string) (string, string, error) {
			time.Sleep(2 * time.Second)
			return "active", "", nil
		}}, nil
	})
	// The fake honours the timeout the controller passes; shrink it so the
	// test does not wait 15 seconds.
	start := time.Now()
	client, _ := getRemoteConnect()("h", 22, "root", "password", "x", "")
	if _, _, err := client.RunCommandWithTimeout("sleep", 50*time.Millisecond); err == nil {
		t.Fatal("fake client did not time out")
	}
	if time.Since(start) > time.Second {
		t.Fatal("timeout did not cut the command short")
	}
	_ = r
}

func TestRemoteNodeHistoryRedactsPrivateKeys(t *testing.T) {
	r := setupFeatureSuiteRouter(t)
	if _, err := db.Exec(`INSERT INTO remote_node_history(node_id, type, params) VALUES (2, 'vless', '{"uuid":"u","reality_priv":"SECRET-PRIV","reality_pub":"pub","server_priv":"S1","client_priv":"C1","port":443}')`); err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, authedRequest(http.MethodGet, "/api/remote_nodes/2/history"))
	if w.Code != http.StatusOK {
		t.Fatalf("want 200 got %d", w.Code)
	}
	body := w.Body.String()
	for _, secret := range []string{"SECRET-PRIV", "reality_priv", "server_priv", "client_priv"} {
		if strings.Contains(body, secret) {
			t.Fatalf("history response leaks %q: %s", secret, body)
		}
	}
	if !strings.Contains(body, `reality_pub`) || !strings.Contains(body, `port`) {
		t.Fatalf("public fields were dropped too: %s", body)
	}
	// The database still holds the full set so a rollback can restore it.
	var raw string
	if err := db.QueryRow("SELECT params FROM remote_node_history WHERE node_id=2 ORDER BY id DESC LIMIT 1").Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(raw, "SECRET-PRIV") {
		t.Fatal("redaction modified the stored history row")
	}
}

func TestRemoteNodeRequestValidation(t *testing.T) {
	r := setupFeatureSuiteRouter(t)
	oldStarter := startDeployRoutine
	defer func() { startDeployRoutine = oldStarter }()
	startDeployRoutine = func(id int64, req RemoteNodeReq, isUpdate bool, params map[string]interface{}) {}

	bad := []string{
		`{"type":"ssh","ssh_host":"10.0.0.9","ssh_user":"root","ssh_auth_type":"password","ssh_credential":"x"}`,
		`{"type":"vless","ssh_host":"10.0.0.9; rm -rf /","ssh_user":"root","ssh_auth_type":"password","ssh_credential":"x"}`,
		`{"type":"vless","ssh_host":"10.0.0.9","ssh_port":70000,"ssh_user":"root","ssh_auth_type":"password","ssh_credential":"x"}`,
		`{"type":"vless","ssh_host":"10.0.0.9","ssh_user":"root x","ssh_auth_type":"password","ssh_credential":"x"}`,
		`{"type":"vless","ssh_host":"10.0.0.9","ssh_user":"root","ssh_auth_type":"agent","ssh_credential":"x"}`,
		`{"type":"vless","ssh_host":"10.0.0.9","ssh_user":"root","ssh_auth_type":"password","ssh_credential":""}`,
		`{"type":"vless","ssh_host":"10.0.0.9","ssh_user":"root","ssh_auth_type":"password","ssh_credential":"x","port":99999}`,
	}
	for _, body := range bad {
		w := httptest.NewRecorder()
		r.ServeHTTP(w, authedJSONRequest(http.MethodPost, "/api/remote_nodes", body))
		if w.Code != http.StatusBadRequest {
			t.Errorf("body %s: want 400 got %d (%s)", body, w.Code, w.Body.String())
		}
	}

	var items []string
	for i := 0; i < remoteNodeBatchLimit+1; i++ {
		items = append(items, `{"type":"vless","ssh_host":"10.0.0.9","ssh_user":"root","ssh_auth_type":"password","ssh_credential":"x"}`)
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, authedJSONRequest(http.MethodPost, "/api/remote_nodes/batch", "["+strings.Join(items, ",")+"]"))
	if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "batch too large") {
		t.Fatalf("oversized batch: want 400 got %d (%s)", w.Code, w.Body.String())
	}

	w = httptest.NewRecorder()
	r.ServeHTTP(w, authedJSONRequest(http.MethodPost, "/api/remote_nodes/abc/rollback", `{"history_id":1}`))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("non-numeric id: want 400 got %d", w.Code)
	}

	if _, err := db.Exec("UPDATE remote_nodes SET status='Deploying' WHERE id=2"); err != nil {
		t.Fatal(err)
	}
	w = httptest.NewRecorder()
	r.ServeHTTP(w, authedRequest(http.MethodPost, "/api/remote_nodes/2/regenerate"))
	if w.Code != http.StatusConflict {
		t.Fatalf("regenerate during deploy: want 409 got %d (%s)", w.Code, w.Body.String())
	}
	w = httptest.NewRecorder()
	r.ServeHTTP(w, authedJSONRequest(http.MethodPost, "/api/remote_nodes/2/rollback", `{"history_id":1}`))
	if w.Code != http.StatusConflict {
		t.Fatalf("rollback during deploy: want 409 got %d (%s)", w.Code, w.Body.String())
	}
}

func TestDNSAndLanAclRejectValuesThatWouldInjectConfig(t *testing.T) {
	r := setupFeatureSuiteRouter(t)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, authedJSONRequest(http.MethodPost, "/api/dns", `{"Local":"223.5.5.5","Remote":"8.8.8.8","Lazy":true,"Mode":"smart","log_level":"info\n  file: /etc/passwd"}`))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("log_level with newline: want 400 got %d (%s)", w.Code, w.Body.String())
	}
	w = httptest.NewRecorder()
	r.ServeHTTP(w, authedJSONRequest(http.MethodPost, "/api/dns", `{"Local":"223.5.5.5","Remote":"8.8.8.8","Lazy":true,"Mode":"weird"}`))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("unknown dns mode: want 400 got %d", w.Code)
	}
	w = httptest.NewRecorder()
	r.ServeHTTP(w, authedJSONRequest(http.MethodPost, "/api/lan_acls/default_policy", `{"policy":"drop; flush ruleset"}`))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("bad default policy: want 400 got %d (%s)", w.Code, w.Body.String())
	}
	if got := renderMosdnsConfig("1.1.1.1", "8.8.8.8", false, "smart", "info\n  file: /x", 0, 0); !strings.Contains(got, "level: info\n") || strings.Contains(got, "/x") {
		t.Fatal("renderMosdnsConfig did not neutralise an injected log level")
	}
}

func TestTraceIDHeaderIsValidated(t *testing.T) {
	r := setupFeatureSuiteRouter(t)
	for _, tc := range []struct {
		in     string
		echoed bool
	}{
		{"abc-123.trace_9", true},
		{strings.Repeat("a", 65), false},
		{"has space", false},
		{"<script>", false},
	} {
		req := authedRequest(http.MethodGet, "/api/rules")
		req.Header.Set("X-Trace-ID", tc.in)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		got := w.Header().Get("X-Trace-ID")
		if tc.echoed && got != tc.in {
			t.Errorf("valid trace id %q was replaced with %q", tc.in, got)
		}
		if !tc.echoed && (got == tc.in || got == "") {
			t.Errorf("invalid trace id %q was accepted (got %q)", tc.in, got)
		}
	}
}

func TestUpdateComponentIsSerialised(t *testing.T) {
	r := setupFeatureSuiteRouter(t)
	// The lock is bypassed in gin's test mode; exercise the real thing.
	gin.SetMode(gin.ReleaseMode)
	defer gin.SetMode(gin.TestMode)
	release, ok := tryAcquireHighRiskMutationLock(newGuardOnlyCtx(), "component_update")
	if !ok {
		t.Fatal("could not take the update lock in the test")
	}
	defer release()
	w := httptest.NewRecorder()
	r.ServeHTTP(w, authedJSONRequest(http.MethodPost, "/api/update/xray", `{"version":"latest"}`))
	if w.Code != http.StatusConflict {
		t.Fatalf("overlapping update: want 409 got %d (%s)", w.Code, w.Body.String())
	}
}

func newGuardOnlyCtx() *gin.Context {
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/api/update/xray", nil)
	return c
}

var _ = errors.New
