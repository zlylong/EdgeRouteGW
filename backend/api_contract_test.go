package main

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestMutationsOnMissingIDsAnswer404(t *testing.T) {
	r := setupFeatureSuiteRouter(t)
	if _, err := db.Exec("CREATE TABLE IF NOT EXISTS protected_ips (id INTEGER PRIMARY KEY AUTOINCREMENT, value TEXT NOT NULL UNIQUE, remark TEXT, created_at DATETIME DEFAULT CURRENT_TIMESTAMP)"); err != nil {
		t.Fatal(err)
	}
	old := applyNftablesConfigFn
	applyNftablesConfigFn = func() error { return nil }
	defer func() { applyNftablesConfigFn = old }()

	cases := []struct {
		method, target string
		body           string
	}{
		{http.MethodDelete, "/api/rules/999", ""},
		{http.MethodDelete, "/api/nodes/999", ""},
		{http.MethodPut, "/api/nodes/999/toggle", ""},
		{http.MethodPut, "/api/nodes/999/default", ""},
		{http.MethodPut, "/api/nodes/999", `{"Name":"x","Type":"Vless","Address":"1.1.1.1","Port":443}`},
		{http.MethodDelete, "/api/lan_acls/999", ""},
		{http.MethodDelete, "/api/protected_ips/999", ""},
		{http.MethodDelete, "/api/remote_nodes/999", ""},
	}
	for _, tc := range cases {
		var req *http.Request
		if tc.body != "" {
			req = authedJSONRequest(tc.method, tc.target, tc.body)
		} else {
			req = authedRequest(tc.method, tc.target)
		}
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		if w.Code != http.StatusNotFound {
			t.Errorf("%s %s: want 404 got %d body=%s", tc.method, tc.target, w.Code, w.Body.String())
			continue
		}
		resp := decodeJSONMap(t, w.Body.Bytes())
		if resp["error_code"] != errCodeNotFound || resp["success"] != false {
			t.Errorf("%s %s: envelope %v lacks error_code/success", tc.method, tc.target, resp)
		}
	}
	// Existing ids keep working.
	w := httptest.NewRecorder()
	r.ServeHTTP(w, authedRequest(http.MethodDelete, "/api/rules/1"))
	if w.Code != http.StatusOK {
		t.Fatalf("DELETE existing rule: want 200 got %d", w.Code)
	}
}

func TestListEndpointsPageWithLimitOffset(t *testing.T) {
	r := setupFeatureSuiteRouter(t)

	w := httptest.NewRecorder()
	r.ServeHTTP(w, authedRequest(http.MethodGet, "/api/rules?limit=1&offset=1"))
	if w.Code != http.StatusOK {
		t.Fatalf("want 200 got %d", w.Code)
	}
	resp := decodeJSONMap(t, w.Body.Bytes())
	rules := resp["rules"].([]interface{})
	if len(rules) != 1 || w.Header().Get("X-Total-Count") != "2" {
		t.Fatalf("rules page: len=%d total=%q", len(rules), w.Header().Get("X-Total-Count"))
	}
	if _, ok := resp["groups"]; !ok {
		t.Fatal("paged rules response lost the groups key")
	}

	w = httptest.NewRecorder()
	r.ServeHTTP(w, authedRequest(http.MethodGet, "/api/nodes?limit=1"))
	if w.Code != http.StatusOK {
		t.Fatalf("want 200 got %d", w.Code)
	}
	if arr := decodeJSONArray(t, w.Body.Bytes()); len(arr) != 1 || w.Header().Get("X-Total-Count") != "2" {
		t.Fatalf("nodes page: len=%d total=%q", len(arr), w.Header().Get("X-Total-Count"))
	}

	// Unpaged requests are unchanged: no header, full list.
	w = httptest.NewRecorder()
	r.ServeHTTP(w, authedRequest(http.MethodGet, "/api/nodes"))
	if arr := decodeJSONArray(t, w.Body.Bytes()); len(arr) != 2 || w.Header().Get("X-Total-Count") != "" {
		t.Fatalf("unpaged nodes changed: len=%d header=%q", len(arr), w.Header().Get("X-Total-Count"))
	}

	w = httptest.NewRecorder()
	r.ServeHTTP(w, authedRequest(http.MethodGet, "/api/rules?limit=abc"))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("bad limit: want 400 got %d", w.Code)
	}
	w = httptest.NewRecorder()
	r.ServeHTTP(w, authedRequest(http.MethodGet, "/api/remote_nodes?limit=5&offset=10"))
	if w.Code != http.StatusOK || strings.TrimSpace(w.Body.String()) != "[]" {
		t.Fatalf("offset past end must give an empty array: %d %s", w.Code, w.Body.String())
	}
}

func TestConnectionsAlwaysReturnsArray(t *testing.T) {
	r := setupFeatureSuiteRouter(t)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, authedRequest(http.MethodGet, "/api/connections?ip=203.0.113.250"))
	if w.Code != http.StatusOK {
		t.Fatalf("want 200 got %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), `"data":[]`) {
		t.Fatalf("empty connections must encode as [] not null: %s", w.Body.String())
	}
}

func TestEventsSupportCursorsAndSince(t *testing.T) {
	r := setupFeatureSuiteRouter(t)
	for i := 0; i < 5; i++ {
		logGatewayEvent("info", "test", "seed", "event", map[string]interface{}{"n": i})
	}
	get := func(q string) (int, map[string]interface{}) {
		w := httptest.NewRecorder()
		r.ServeHTTP(w, authedRequest(http.MethodGet, "/api/events"+q))
		if w.Code != http.StatusOK {
			return w.Code, nil
		}
		return w.Code, decodeJSONMap(t, w.Body.Bytes())
	}
	code, resp := get("?module=test&limit=2")
	if code != 200 {
		t.Fatalf("want 200 got %d", code)
	}
	events := resp["events"].([]interface{})
	if len(events) != 2 {
		t.Fatalf("limit=2 returned %d", len(events))
	}
	lastID := int(events[1].(map[string]interface{})["id"].(float64))
	code, resp = get("?module=test&before_id=" + itoa(lastID))
	if code != 200 || len(resp["events"].([]interface{})) != 3 {
		t.Fatalf("before_id paging: code=%d resp=%v", code, resp)
	}
	code, resp = get("?module=test&limit=2&offset=4")
	if code != 200 || len(resp["events"].([]interface{})) != 1 {
		t.Fatalf("offset paging: code=%d resp=%v", code, resp)
	}
	code, resp = get("?module=test&since=1h")
	if code != 200 || len(resp["events"].([]interface{})) != 5 {
		t.Fatalf("since=1h: code=%d resp=%v", code, resp)
	}
	code, resp = get("?module=test&since=" + time.Now().Add(time.Hour).UTC().Format(time.RFC3339))
	if code != 200 || len(resp["events"].([]interface{})) != 0 {
		t.Fatalf("since in the future must return nothing: code=%d resp=%v", code, resp)
	}
	for _, bad := range []string{"?since=yesterday", "?before_id=x", "?offset=-1", "?after_id=-5"} {
		if code, _ := get(bad); code != http.StatusBadRequest {
			t.Errorf("%s: want 400 got %d", bad, code)
		}
	}
}

func itoa(n int) string { return intToString(n) }

func intToString(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	if neg {
		b = append([]byte{'-'}, b...)
	}
	return string(b)
}

func TestJournalctlArgsAreValidatedAndNeverShellInterpreted(t *testing.T) {
	args, err := buildJournalctlArgs("frr", "5000", "15m", "warning", "route add")
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(args, " ")
	if !strings.HasPrefix(joined, "-u frr --no-pager -n 2000 --since ") || !strings.Contains(joined, "-p warning --grep route add") {
		t.Fatalf("unexpected args: %v", args)
	}
	for _, bad := range []struct{ svc, lines, since, prio, grep string }{
		{"sshd", "", "", "", ""},
		{"xray", "ten", "", "", ""},
		{"xray", "", "; rm -rf /", "", ""},
		{"xray", "", "", "loud", ""},
		{"xray", "", "", "", "$(reboot)"},
	} {
		if _, err := buildJournalctlArgs(bad.svc, bad.lines, bad.since, bad.prio, bad.grep); err == nil {
			t.Errorf("%+v accepted", bad)
		}
	}
	r := setupFeatureSuiteRouter(t)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, authedRequest(http.MethodGet, "/api/logs/xray?since=%3B%20rm"))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("injected since: want 400 got %d", w.Code)
	}
}

func TestRemoteNodeLogsEndpoint(t *testing.T) {
	r := setupFeatureSuiteRouter(t)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, authedRequest(http.MethodGet, "/api/remote_nodes/2/logs"))
	if w.Code != http.StatusOK {
		t.Fatalf("want 200 got %d body=%s", w.Code, w.Body.String())
	}
	resp := decodeJSONMap(t, w.Body.Bytes())
	logs := resp["logs"].([]interface{})
	if len(logs) != 1 || logs[0].(map[string]interface{})["log_text"] != "Deployment successful" {
		t.Fatalf("unexpected logs payload: %v", resp)
	}
	w = httptest.NewRecorder()
	r.ServeHTTP(w, authedRequest(http.MethodGet, "/api/remote_nodes/999/logs"))
	if w.Code != http.StatusNotFound {
		t.Fatalf("missing node: want 404 got %d", w.Code)
	}
}

func TestBackgroundHealthCheckUpdatesStatus(t *testing.T) {
	setupFeatureSuiteRouter(t)
	oldConnect := getRemoteConnect()
	defer func() { setRemoteConnect(oldConnect) }()

	setRemoteConnect(func(host string, port int, user string, authType string, credential string, expectedHostKey string) (remoteSSHClient, error) {
		return nil, errors.New("dial tcp: connection refused")
	})
	runRemoteHealthCheckOnce()
	var status string
	if err := db.QueryRow("SELECT status FROM remote_nodes WHERE id=2").Scan(&status); err != nil || status != "Offline" {
		t.Fatalf("unreachable node status = %q err=%v, want Offline", status, err)
	}

	setRemoteConnect(func(host string, port int, user string, authType string, credential string, expectedHostKey string) (remoteSSHClient, error) {
		return &fakeSSHClient{run: func(cmd string) (string, string, error) { return "active\n", "", nil }}, nil
	})
	runRemoteHealthCheckOnce()
	if err := db.QueryRow("SELECT status FROM remote_nodes WHERE id=2").Scan(&status); err != nil || status != "Online" {
		t.Fatalf("reachable node status = %q, want Online", status)
	}

	// Nodes mid-deploy are left alone.
	if _, err := db.Exec("UPDATE remote_nodes SET status='Deploying' WHERE id=2"); err != nil {
		t.Fatal(err)
	}
	runRemoteHealthCheckOnce()
	if err := db.QueryRow("SELECT status FROM remote_nodes WHERE id=2").Scan(&status); err != nil || status != "Deploying" {
		t.Fatalf("deploying node was probed: status=%q", status)
	}
}

func TestPingReportsStartedAndCanWait(t *testing.T) {
	r := setupFeatureSuiteRouter(t)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, authedRequest(http.MethodPost, "/api/nodes/ping?wait=1"))
	if w.Code != http.StatusOK {
		t.Fatalf("want 200 got %d", w.Code)
	}
	resp := decodeJSONMap(t, w.Body.Bytes())
	if resp["started"].(float64) != 2 {
		t.Fatalf("started = %v want 2", resp["started"])
	}
	if _, ok := resp["completed"]; !ok {
		t.Fatal("completed flag missing")
	}
}

func TestGeoQueryExpansionIsBoundedAndFlagged(t *testing.T) {
	r := setupFeatureSuiteRouter(t)
	writeTestGeoData(t)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, authedRequest(http.MethodGet, "/api/geo/query?input=geoip:!cn&limit=1"))
	if w.Code != http.StatusOK {
		t.Fatalf("want 200 got %d body=%s", w.Code, w.Body.String())
	}
	resp := decodeJSONMap(t, w.Body.Bytes())
	if resp["truncated"] != true || resp["count"].(float64) != 2 || len(resp["values"].([]interface{})) != 1 {
		t.Fatalf("unexpected expansion payload: %v", resp)
	}
	w = httptest.NewRecorder()
	r.ServeHTTP(w, authedRequest(http.MethodGet, "/api/geo/query?input=geosite:gfw"))
	resp = decodeJSONMap(t, w.Body.Bytes())
	if resp["truncated"] != false || !reflect.DeepEqual(resp["values"], []interface{}{"domain:google.com", "full:youtube.com"}) {
		t.Fatalf("unexpected geosite payload: %v", resp)
	}
}
