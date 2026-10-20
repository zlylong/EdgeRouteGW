package v2

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/zlylong/EdgeRouteGW/internal/events"
	"github.com/zlylong/EdgeRouteGW/internal/model"
	"github.com/zlylong/EdgeRouteGW/internal/paths"
	"github.com/zlylong/EdgeRouteGW/internal/reconcile"
	"github.com/zlylong/EdgeRouteGW/internal/render"
	"github.com/zlylong/EdgeRouteGW/internal/secret"
	"github.com/zlylong/EdgeRouteGW/internal/store"
)

type env struct {
	srv *httptest.Server
	st  *store.Store
	rec *reconcile.Reconciler
	t   *testing.T
}

func newEnv(t *testing.T) *env {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "v2.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if _, err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	kr, _ := secret.FromBytes(bytes.Repeat([]byte{7}, 32))
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	bus := events.New(16)
	p := paths.New(t.TempDir())
	rec := reconcile.New(st, kr, render.Deps{Paths: p}, nil, bus, log)
	mux := http.NewServeMux()
	Register(mux, Deps{Store: st, Keyring: kr, Reconciler: rec, Bus: bus, Paths: p, Log: log, StartedAt: time.Now()})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return &env{srv: srv, st: st, rec: rec, t: t}
}

func (e *env) do(method, path string, body any) (int, map[string]any) {
	e.t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, e.srv.URL+path, rd)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer res.Body.Close()
	var out map[string]any
	raw, _ := io.ReadAll(res.Body)
	if len(raw) > 0 && strings.HasPrefix(res.Header.Get("Content-Type"), "application/json") {
		if err := json.Unmarshal(raw, &out); err != nil {
			e.t.Fatalf("%s %s: bad json %q", method, path, raw)
		}
	}
	return res.StatusCode, out
}

func (e *env) must(method, path string, body any, want int) map[string]any {
	e.t.Helper()
	code, out := e.do(method, path, body)
	if code != want {
		e.t.Fatalf("%s %s: status %d want %d: %v", method, path, code, want, out)
	}
	return out
}

func item(out map[string]any) map[string]any { m, _ := out["item"].(map[string]any); return m }

func TestNodeLifecycleAndGeneration(t *testing.T) {
	e := newEnv(t)
	// Missing credential for vless -> 422 with field.
	code, out := e.do("POST", "/api/v2/nodes", map[string]any{"name": "SG", "protocol": "vless", "address": "sg.example.com", "port": 443})
	if code != 422 || out["error"].(map[string]any)["details"].(map[string]any)["field"] != "credential" {
		t.Fatalf("expected credential validation error, got %d %v", code, out)
	}
	out = e.must("POST", "/api/v2/nodes", map[string]any{"name": "SG", "protocol": "VLESS", "address": "sg.example.com", "port": 443, "credential": "uuid-1", "params": `{"type":"tcp","security":"reality","sni":"a","pbk":"b","sid":"c"}`}, 201)
	n := item(out)
	id := n["id"].(string)
	if n["has_credential"] != true || n["protocol"] != "vless" || out["generation"].(float64) != 1 {
		t.Fatalf("created %v", out)
	}
	if _, leaked := n["credential"]; leaked {
		t.Fatal("credential must never be returned")
	}
	// Stored encrypted.
	row, _ := e.st.GetNode(context.Background(), id)
	if bytes.Contains(row.CredentialEnc, []byte("uuid-1")) {
		t.Fatal("credential stored in plaintext")
	}

	// Update without credential keeps it; with "" clears -> then invalid for vless.
	out = e.must("PUT", "/api/v2/nodes/"+id, map[string]any{"name": "SG2", "protocol": "vless", "address": "sg.example.com", "port": 8443}, 200)
	if item(out)["has_credential"] != true || item(out)["port"].(float64) != 8443 || out["generation"].(float64) != 2 {
		t.Fatalf("update %v", out)
	}
	code, _ = e.do("PUT", "/api/v2/nodes/"+id, map[string]any{"name": "SG2", "protocol": "vless", "address": "sg.example.com", "port": 8443, "credential": ""})
	if code != 422 {
		t.Fatalf("clearing a required credential should fail, got %d", code)
	}

	e.must("POST", "/api/v2/nodes/"+id+"/default", nil, 200)
	e.must("POST", "/api/v2/nodes/"+id+"/enabled", map[string]any{"enabled": false}, 200)
	out = e.must("GET", "/api/v2/nodes/"+id, nil, 200)
	if item(out)["is_default"] != true || item(out)["enabled"] != false {
		t.Fatalf("flags %v", item(out))
	}
	code, _ = e.do("GET", "/api/v2/nodes/does-not-exist", nil)
	if code != 404 {
		t.Fatalf("missing node status %d", code)
	}

	// A rule pointing at the node is retargeted when the node is deleted.
	set := e.must("POST", "/api/v2/rule-sets", map[string]any{"name": "main"}, 201)
	setID := item(set)["id"].(string)
	e.must("POST", "/api/v2/rules", map[string]any{"set_id": setID, "match_kind": "domain", "match_value": "a.com", "action_kind": "node", "action_ref": id}, 201)
	out = e.must("DELETE", "/api/v2/nodes/"+id, nil, 200)
	if out["retargeted_rules"].(float64) != 1 {
		t.Fatalf("delete %v", out)
	}
	rules := e.must("GET", "/api/v2/rules", nil, 200)["items"].([]any)
	if rules[0].(map[string]any)["action_kind"] != "proxy" {
		t.Fatalf("rule not retargeted: %v", rules[0])
	}
	meta, _ := e.st.Meta(context.Background())
	if meta.Generation != 7 {
		t.Fatalf("generation %d, want 7 (every write bumps once)", meta.Generation)
	}
	cs := e.must("GET", "/api/v2/changesets", nil, 200)["items"].([]any)
	if len(cs) != 7 || cs[0].(map[string]any)["generation"].(float64) != 7 {
		t.Fatalf("changesets %v", cs)
	}
}

func TestImportDryRunAndCommit(t *testing.T) {
	e := newEnv(t)
	links := []string{
		"vless://11111111-2222-3333-4444-555555555555@sg.example.com:443?security=reality&sni=www.apple.com&pbk=P&sid=s&type=tcp#SG",
		"garbage://x",
	}
	out := e.must("POST", "/api/v2/node-imports", map[string]any{"links": links, "dry_run": true}, 200)
	results := out["results"].([]any)
	if out["imported"].(float64) != 0 || len(results) != 2 || results[1].(map[string]any)["error"] == nil {
		t.Fatalf("dry run %v", out)
	}
	if strings.Contains(results[0].(map[string]any)["link"].(string), "11111111") {
		t.Fatal("import result must redact the credential in the link")
	}
	if n, _ := e.st.ListNodes(context.Background()); len(n) != 0 {
		t.Fatal("dry run must not save")
	}
	out = e.must("POST", "/api/v2/node-imports", map[string]any{"links": links}, 201)
	if out["imported"].(float64) != 1 {
		t.Fatalf("import %v", out)
	}
}

func TestRulesValidationAndReorder(t *testing.T) {
	e := newEnv(t)
	setID := item(e.must("POST", "/api/v2/rule-sets", map[string]any{"name": "main"}, 201))["id"].(string)

	// Batch add via comma-separated domains.
	out := e.must("POST", "/api/v2/rules", map[string]any{"set_id": setID, "match_kind": "domain", "match_value": "a.com, **.b.com\nc.com", "action_kind": "proxy"}, 201)
	items := out["items"].([]any)
	if len(items) != 3 {
		t.Fatalf("batch %v", out)
	}
	if items[1].(map[string]any)["match_value"] != "**.b.com" || items[1].(map[string]any)["position"].(float64) != 1 {
		t.Fatalf("rule 1 %v", items[1])
	}
	// Invalid cases.
	for _, bad := range []map[string]any{
		{"set_id": setID, "match_kind": "domain", "match_value": "nodots", "action_kind": "proxy"},
		{"set_id": setID, "match_kind": "ip", "match_value": "300.1.1.1", "action_kind": "direct"},
		{"set_id": setID, "match_kind": "geosite", "match_value": "bad/tag", "action_kind": "direct"},
		{"set_id": setID, "match_kind": "domain", "match_value": "x.com", "action_kind": "node", "action_ref": "missing"},
		{"set_id": setID, "match_kind": "domain", "match_value": "x.com", "action_kind": "direct", "action_ref": "junk"},
		{"set_id": setID, "match_kind": "domain", "match_value": "x.com", "action_kind": "teleport"},
		{"set_id": setID, "match_kind": "domain", "match_value": "x.com", "action_kind": "direct", "unknown_field": 1},
	} {
		if code, _ := e.do("POST", "/api/v2/rules", bad); code != 422 {
			t.Fatalf("expected 422 for %v, got %d", bad, code)
		}
	}
	// IP normalisation.
	out = e.must("POST", "/api/v2/rules", map[string]any{"set_id": setID, "match_kind": "ip", "match_value": "10.1.2.3/8", "action_kind": "direct"}, 201)
	if out["items"].([]any)[0].(map[string]any)["match_value"] != "10.0.0.0/8" {
		t.Fatalf("ip not normalised: %v", out)
	}

	// Reorder: must list all ids.
	ids := []string{}
	for _, it := range e.must("GET", "/api/v2/rules?set_id="+setID, nil, 200)["items"].([]any) {
		ids = append(ids, it.(map[string]any)["id"].(string))
	}
	if code, _ := e.do("POST", "/api/v2/rule-sets/"+setID+"/reorder", map[string]any{"ids": ids[:2]}); code != 422 {
		t.Fatalf("partial reorder should be rejected, got %d", code)
	}
	rev := []string{ids[3], ids[2], ids[1], ids[0]}
	e.must("POST", "/api/v2/rule-sets/"+setID+"/reorder", map[string]any{"ids": rev}, 200)
	after := e.must("GET", "/api/v2/rules?set_id="+setID, nil, 200)["items"].([]any)
	if after[0].(map[string]any)["id"] != ids[3] || after[3].(map[string]any)["id"] != ids[0] {
		t.Fatalf("order after reorder %v", after)
	}

	// Mode B refuses wildcard rules; switching to B with one present is refused too.
	code, out := e.do("PUT", "/api/v2/network", map[string]any{"mode": "B"})
	if code != 422 || !strings.Contains(out["error"].(map[string]any)["message"].(string), "wildcard") {
		t.Fatalf("mode switch with wildcard rule: %d %v", code, out)
	}
}

func TestDNSAndNetworkValidation(t *testing.T) {
	e := newEnv(t)
	out := e.must("PUT", "/api/v2/dns", map[string]any{"local_upstreams": []string{"223.5.5.5"}, "remote_upstreams": []string{"https://dns.google/dns-query", "1.1.1.1"}, "cache_size": 4096, "lazy_cache": true, "lazy_cache_ttl": 3600, "log_level": "WARN"}, 200)
	if item(out)["log_level"] != "warn" || item(out)["cache_size"].(float64) != 4096 {
		t.Fatalf("dns %v", out)
	}
	if code, _ := e.do("PUT", "/api/v2/dns", map[string]any{"local_upstreams": []string{"1.1.1.1; rm -rf /"}, "remote_upstreams": []string{"1.1.1.1"}}); code != 422 {
		t.Fatalf("shell-ish upstream accepted: %d", code)
	}
	if code, _ := e.do("PUT", "/api/v2/dns", map[string]any{"local_upstreams": []string{}, "remote_upstreams": []string{"1.1.1.1"}}); code != 422 {
		t.Fatalf("empty upstream list accepted: %d", code)
	}

	out = e.must("PUT", "/api/v2/network", map[string]any{"mode": "c", "tproxy_port": 0, "ospf_push_batch": 99999, "ospf_publish_allowlist": []string{"1.2.3.4"}}, 200)
	n := item(out)
	if n["mode"] != "C" || n["tproxy_port"].(float64) != 12345 || n["ospf_push_batch"].(float64) != 5000 || n["ospf_publish_allowlist"].([]any)[0] != "1.2.3.4/32" || n["fakeip_pool"] != "198.18.0.0/16" {
		t.Fatalf("network %v", n)
	}
	for _, bad := range []map[string]any{{"mode": "D"}, {"mode": "A", "tproxy_port": 80}, {"mode": "A", "fakeip_pool": "fd00::/64"}, {"mode": "A", "lan_default_policy": "block"}} {
		if code, _ := e.do("PUT", "/api/v2/network", bad); code != 422 {
			t.Fatalf("expected 422 for %v, got %d", bad, code)
		}
	}
	// A state read reflects the mode.
	st := e.must("GET", "/api/v2/state", nil, 200)
	if st["mode"] != "C" || st["pending"] != true {
		t.Fatalf("state %v", st)
	}
}

func TestPlanArtifactsAndReconcileNow(t *testing.T) {
	e := newEnv(t)
	out := e.must("GET", "/api/v2/plan", nil, 200)
	if out["artifact_sizes"].(map[string]any)["xray"].(float64) == 0 {
		t.Fatalf("plan %v", out)
	}
	req, _ := http.NewRequest("GET", e.srv.URL+"/api/v2/artifacts/nftables", nil)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != 200 || !strings.Contains(string(b), "table inet edgeroutegw") {
		t.Fatalf("artifact %d %q", res.StatusCode, b)
	}
	if code, _ := e.do("GET", "/api/v2/artifacts/frr", nil); code != 404 {
		t.Fatalf("unknown artifact %d", code)
	}
	e.must("POST", "/api/v2/device-policies", map[string]any{"kind": "mac", "value": "AA-BB-CC-DD-EE-FF", "action": "reject"}, 201)
	out = e.must("POST", "/api/v2/reconcile", nil, 200)
	state := out["state"].(map[string]any)
	if state["pending"] != false || state["last_applied_generation"].(float64) != 1 {
		t.Fatalf("after reconcile %v", state)
	}
	logs := e.must("GET", "/api/v2/apply-log", nil, 200)["items"].([]any)
	if len(logs) != 0 {
		t.Fatalf("no drivers -> no apply log entries, got %v", logs)
	}
}

func TestDeviceAndBypassValidation(t *testing.T) {
	e := newEnv(t)
	out := e.must("POST", "/api/v2/device-policies", map[string]any{"kind": "ip", "value": "192.168.1.0/24", "action": "direct"}, 201)
	if item(out)["kind"] != "cidr" || item(out)["value"] != "192.168.1.0/24" {
		t.Fatalf("policy %v", out)
	}
	if code, _ := e.do("POST", "/api/v2/device-policies", map[string]any{"kind": "ip", "value": "192.168.1.0/24", "action": "proxy"}); code != 409 {
		t.Fatalf("duplicate device should be 409, got %d", code)
	}
	if code, _ := e.do("POST", "/api/v2/bypass", map[string]any{"cidr": "2001:db8::/32"}); code != 422 {
		t.Fatalf("ipv6 bypass should be rejected, got %d", code)
	}
	out = e.must("POST", "/api/v2/bypass", map[string]any{"cidr": "203.0.113.9"}, 201)
	if item(out)["cidr"] != "203.0.113.9/32" {
		t.Fatalf("bypass %v", out)
	}
}

func TestSSEStreamSendsStateThenEvents(t *testing.T) {
	e := newEnv(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", e.srv.URL+"/api/v2/events/stream", nil)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.Header.Get("Content-Type") != "text/event-stream" {
		t.Fatalf("content-type %q", res.Header.Get("Content-Type"))
	}
	rd := bufio.NewReader(res.Body)
	readEvent := func() (string, string) {
		var typ, data string
		for {
			line, err := rd.ReadString('\n')
			if err != nil {
				t.Fatalf("stream ended: %v", err)
			}
			line = strings.TrimRight(line, "\n")
			switch {
			case strings.HasPrefix(line, "event: "):
				typ = strings.TrimPrefix(line, "event: ")
			case strings.HasPrefix(line, "data: "):
				data = strings.TrimPrefix(line, "data: ")
			case line == "" && typ != "":
				return typ, data
			}
		}
	}
	typ, data := readEvent()
	if typ != "state" || !strings.Contains(data, `"schema_version":"0001_init"`) {
		t.Fatalf("first event %s %s", typ, data)
	}
	// A write produces state.changed followed by a fresh state snapshot.
	go func() {
		time.Sleep(50 * time.Millisecond)
		e.must("POST", "/api/v2/rule-sets", map[string]any{"name": "x"}, 201)
	}()
	typ, data = readEvent()
	if typ != string(events.StateChanged) || !strings.Contains(data, `"generation":1`) {
		t.Fatalf("second event %s %s", typ, data)
	}
	typ, data = readEvent()
	if typ != "state" || !strings.Contains(data, `"generation":1`) {
		t.Fatalf("third event %s %s", typ, data)
	}
	_ = fmt.Sprint(model.ModeA)
}
