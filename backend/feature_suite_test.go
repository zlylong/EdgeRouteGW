package main

import (
	"container/ring"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	_ "github.com/mattn/go-sqlite3"
)

var featureSuiteMu sync.Mutex

func setupFeatureSuiteRouter(t *testing.T) *gin.Engine {
	t.Helper()
	featureSuiteMu.Lock()
	gin.SetMode(gin.TestMode)

	oldDB := db
	oldCachedGeosite := append([]string(nil), cachedGeosite...)
	oldCachedGeoip := append([]string(nil), cachedGeoip...)
	oldOspfLogs := append([]string(nil), ospfLogs...)
	oldApplyTimer := applyTimer

	applyMutex.Lock()
	if applyTimer != nil {
		applyTimer.Stop()
		applyTimer = nil
	}
	applyMutex.Unlock()
	cachedGeosite = nil
	cachedGeoip = nil
	ospfLogs = nil
	clearSyncMap(&sessions)
	clearSyncMap(&loginAttempts)

	root := t.TempDir()
	t.Setenv("PROXYGW_HOME", root)

	mustMkdirAll(t, filepath.Join(root, "core", "xray"))
	mustMkdirAll(t, filepath.Join(root, "core", "mosdns"))
	mustMkdirAll(t, filepath.Join(root, "core", "frr"))
	mustWriteFile(t, filepath.Join(root, "core", "xray", "config.json"), `{"log":{"loglevel":"warning"}}`)
	mustWriteFile(t, filepath.Join(root, "core", "mosdns", "config.yaml"), "log:\n  level: info\n")
	mustWriteFile(t, filepath.Join(root, "core", "mosdns", "geodata.ver"), "2026-04-20")
	mustWriteFile(t, filepath.Join(root, "core", "frr", "frr.conf"), "router ospf\n ospf router-id 192.168.20.154\n")

	dbPath := filepath.Join(root, "feature.db")
	tdb, err := sql.Open("sqlite3", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	db = tdb
	t.Cleanup(func() {
		applyMutex.Lock()
		if applyTimer != nil {
			applyTimer.Stop()
			applyTimer = nil
		}
		db = oldDB
		cachedGeosite = oldCachedGeosite
		cachedGeoip = oldCachedGeoip
		ospfLogs = oldOspfLogs
		applyTimer = oldApplyTimer
		applyMutex.Unlock()
		clearSyncMap(&sessions)
		clearSyncMap(&loginAttempts)
		_ = tdb.Close()
		featureSuiteMu.Unlock()
	})

	stmts := []string{
		`CREATE TABLE settings (key TEXT PRIMARY KEY, value TEXT);`,
		`CREATE TABLE rules (id INTEGER PRIMARY KEY AUTOINCREMENT, type TEXT, value TEXT, policy TEXT, priority INTEGER NOT NULL DEFAULT 0, group_id TEXT NOT NULL DEFAULT '', group_name TEXT NOT NULL DEFAULT '');`,
		`CREATE TABLE nodes (id INTEGER PRIMARY KEY AUTOINCREMENT, name TEXT, grp TEXT, type TEXT, address TEXT, port INTEGER, uuid TEXT, params TEXT, active BOOLEAN DEFAULT 1, ping INTEGER DEFAULT 0);`,
		`CREATE TABLE lan_acls (id INTEGER PRIMARY KEY AUTOINCREMENT, type TEXT, value TEXT, policy TEXT, remark TEXT, created_at DATETIME DEFAULT CURRENT_TIMESTAMP);`,
		`CREATE TABLE remote_nodes (id INTEGER PRIMARY KEY AUTOINCREMENT, name TEXT, type TEXT, ssh_host TEXT, ssh_port INTEGER, ssh_user TEXT, ssh_auth_type TEXT, ssh_credential TEXT, ssh_host_key TEXT, region TEXT, status TEXT, remark TEXT, created_at DATETIME DEFAULT CURRENT_TIMESTAMP, updated_at DATETIME DEFAULT CURRENT_TIMESTAMP);`,
		`CREATE TABLE remote_node_wg (node_id INTEGER PRIMARY KEY, server_priv TEXT, server_pub TEXT, client_priv TEXT, client_pub TEXT, endpoint TEXT, port INTEGER, tunnel_addr TEXT, client_addr TEXT);`,
		`CREATE TABLE remote_node_vless (node_id INTEGER PRIMARY KEY, uuid TEXT, reality_priv TEXT, reality_pub TEXT, short_id TEXT, server_name TEXT, dest TEXT, port INTEGER, share_link TEXT);`,
		`CREATE TABLE remote_node_history (id INTEGER PRIMARY KEY AUTOINCREMENT, node_id INTEGER, type TEXT, params TEXT, created_at DATETIME DEFAULT CURRENT_TIMESTAMP);`,
		`CREATE TABLE remote_node_logs (id INTEGER PRIMARY KEY AUTOINCREMENT, node_id INTEGER, action TEXT, status TEXT, log_text TEXT, created_at DATETIME DEFAULT CURRENT_TIMESTAMP);`,
		`CREATE TABLE traffic_history (ts DATETIME, up_bytes INTEGER, down_bytes INTEGER);`,
		`CREATE TABLE node_traffic_history (ts DATETIME, node_id INTEGER, up_bytes INTEGER, down_bytes INTEGER);`,
		`CREATE TABLE gateway_events (id INTEGER PRIMARY KEY AUTOINCREMENT, ts DATETIME DEFAULT CURRENT_TIMESTAMP, level TEXT, module TEXT, event_type TEXT, message TEXT, trace_id TEXT, source_ip TEXT, method TEXT, path TEXT, status INTEGER, duration_ms INTEGER, details_json TEXT);`,
		`CREATE TABLE routes_table (ip TEXT PRIMARY KEY, domain TEXT, source TEXT, first_seen DATETIME, last_seen DATETIME, ttl INTEGER, status TEXT, miss_count INTEGER DEFAULT 0);`,
		`CREATE TABLE geosite_expand_cache (tag TEXT NOT NULL, geodata_ver TEXT NOT NULL, domains_json TEXT NOT NULL, skipped_count INTEGER NOT NULL DEFAULT 0, updated_at DATETIME DEFAULT CURRENT_TIMESTAMP, PRIMARY KEY (tag, geodata_ver));`,
		`CREATE TABLE domain_resolve_cache (domain TEXT PRIMARY KEY, ips_json TEXT NOT NULL, dns_ttl INTEGER NOT NULL DEFAULT 300, resolved_at DATETIME NOT NULL, expire_at DATETIME NOT NULL, last_error TEXT NOT NULL DEFAULT '', fail_count INTEGER NOT NULL DEFAULT 0, geodata_ver TEXT NOT NULL DEFAULT '');`,
	}
	for _, stmt := range stmts {
		if _, err := tdb.Exec(stmt); err != nil {
			t.Fatalf("create schema failed: %v", err)
		}
	}

	seed := []string{
		`INSERT INTO settings(key, value) VALUES ('password', 'admin')`,
		`INSERT INTO settings(key, value) VALUES ('dns_local', '223.5.5.5')`,
		`INSERT INTO settings(key, value) VALUES ('dns_remote', '8.8.8.8')`,
		`INSERT INTO settings(key, value) VALUES ('dns_lazy', 'true')`,
		`INSERT INTO settings(key, value) VALUES ('dns_mode', 'smart')`,
		`INSERT INTO settings(key, value) VALUES ('mode', 'B')`,
		`INSERT INTO settings(key, value) VALUES ('cron_enabled', 'false')`,
		`INSERT INTO settings(key, value) VALUES ('cron_time', '04:00')`,
		`INSERT INTO settings(key, value) VALUES ('lan_default_policy', 'proxy')`,
		`INSERT INTO settings(key, value) VALUES ('management_iface', 'eth0')`,
		`INSERT INTO settings(key, value) VALUES ('service_iface', 'eth0')`,
		`INSERT INTO settings(key, value) VALUES ('default_node_id', '2')`,
		`INSERT INTO rules(type, value, policy) VALUES ('domain', 'example.com', 'proxy')`,
		`INSERT INTO rules(type, value, policy) VALUES ('ip', '8.8.8.8/32', 'direct')`,
		`INSERT INTO nodes(id, name, grp, type, address, port, uuid, params, active, ping) VALUES (1, 'n1', 'g1', 'Vmess', '1.1.1.1', 443, 'u1', '{}', 1, 10)`,
		`INSERT INTO nodes(id, name, grp, type, address, port, uuid, params, active, ping) VALUES (2, 'n2', 'g2', 'Vless', '2.2.2.2', 8443, 'u2', '{"flow":"xtls-rprx-vision"}', 1, 20)`,
		`INSERT INTO lan_acls(type, value, policy, remark) VALUES ('ip', '192.168.20.10', 'direct', 'laptop')`,
		`INSERT INTO remote_nodes(id, name, type, ssh_host, ssh_port, ssh_user, ssh_auth_type, ssh_credential, ssh_host_key, region, status, remark) VALUES (2, '192.168.20.152', 'vless', '192.168.20.152', 22, 'root', 'password', 'ENC:test', 'SHA256:test', 'lab', 'Online', 'seed')`,
		`INSERT INTO remote_node_vless(node_id, uuid, reality_priv, reality_pub, short_id, server_name, dest, port, share_link) VALUES (2, 'a64bc5e0-abd8-4015-a904-4ababd2b88ce', 'priv', 'pub', '6c4368e699a21562', 'www.microsoft.com', 'www.microsoft.com:443', 21508, 'vless://a64bc5e0-abd8-4015-a904-4ababd2b88ce@192.168.20.152:21508?security=reality&sni=www.microsoft.com&fp=chrome&pbk=pub&sid=6c4368e699a21562&type=tcp&flow=xtls-rprx-vision&encryption=none#192.168.20.152')`,
		`INSERT INTO remote_node_history(node_id, type, params) VALUES (2, 'vless', '{"port":21508}')`,
		`INSERT INTO remote_node_logs(node_id, action, status, log_text) VALUES (2, 'deploy', 'success', 'Deployment successful')`,
		`INSERT INTO traffic_history(ts, up_bytes, down_bytes) VALUES (datetime('now', '-1 hour'), 100, 200)`,
		`INSERT INTO traffic_history(ts, up_bytes, down_bytes) VALUES (datetime('now', '-2 hour'), 300, 400)`,
		`INSERT INTO node_traffic_history(ts, node_id, up_bytes, down_bytes) VALUES (datetime('now', '-3 hour'), 1, 120, 180)`,
		`INSERT INTO node_traffic_history(ts, node_id, up_bytes, down_bytes) VALUES (datetime('now', '-4 hour'), 2, 50, 70)`,
		`INSERT INTO routes_table(ip, domain, source, first_seen, last_seen, ttl, status, miss_count) VALUES ('8.8.8.8/32', 'google.com', 'seed', datetime('now'), datetime('now'), 300, 'published', 0)`,
		`INSERT INTO routes_table(ip, domain, source, first_seen, last_seen, ttl, status, miss_count) VALUES ('1.1.1.1/32', 'cloudflare.com', 'seed', datetime('now'), datetime('now'), 300, 'candidate', 0)`,
	}
	for _, stmt := range seed {
		if _, err := tdb.Exec(stmt); err != nil {
			t.Fatalf("seed failed: %v", err)
		}
	}

	sessions.Store("test-token", SessionInfo{ExpiresAt: time.Now().Add(time.Hour)})

	r := gin.New()
	registerAPIRoutes(r)
	return r
}

func clearSyncMap(m *sync.Map) {
	m.Range(func(key, _ interface{}) bool {
		m.Delete(key)
		return true
	})
}

func mustMkdirAll(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatal(err)
	}
}

func mustWriteFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func authedJSONRequest(method, target, body string) *http.Request {
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer test-token")
	req.Header.Set("Content-Type", "application/json")
	return req
}

func authedRequest(method, target string) *http.Request {
	req := httptest.NewRequest(method, target, nil)
	req.Header.Set("Authorization", "Bearer test-token")
	return req
}

func decodeJSONMap(t *testing.T, body []byte) map[string]interface{} {
	t.Helper()
	var v map[string]interface{}
	if err := json.Unmarshal(body, &v); err != nil {
		t.Fatal(err)
	}
	return v
}

func decodeJSONArray(t *testing.T, body []byte) []map[string]interface{} {
	t.Helper()
	var v []map[string]interface{}
	if err := json.Unmarshal(body, &v); err != nil {
		t.Fatal(err)
	}
	return v
}

func TestFeatureSuite_AuthConfigAndSystem(t *testing.T) {
	r := setupFeatureSuiteRouter(t)

	t.Run("login succeeds and returns token", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/api/login", strings.NewReader(`{"Password":"admin"}`))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("want 200 got %d", w.Code)
		}
		resp := decodeJSONMap(t, w.Body.Bytes())
		if strings.TrimSpace(resp["token"].(string)) == "" {
			t.Fatal("expected non-empty token")
		}
	})

	t.Run("logout revokes session", func(t *testing.T) {
		w := httptest.NewRecorder()
		r.ServeHTTP(w, authedRequest(http.MethodPost, "/api/logout"))
		if w.Code != http.StatusOK {
			t.Fatalf("want 200 got %d", w.Code)
		}
		protected := httptest.NewRecorder()
		r.ServeHTTP(protected, authedRequest(http.MethodGet, "/api/dns"))
		if protected.Code != http.StatusUnauthorized {
			t.Fatalf("want 401 after logout got %d", protected.Code)
		}
		sessions.Store("test-token", SessionInfo{ExpiresAt: time.Now().Add(time.Hour)})
	})

	t.Run("config endpoints return seeded content", func(t *testing.T) {
		for _, path := range []string{"/api/config/xray", "/api/config/mosdns", "/api/config/frr"} {
			w := httptest.NewRecorder()
			r.ServeHTTP(w, authedRequest(http.MethodGet, path))
			if w.Code != http.StatusOK {
				t.Fatalf("%s want 200 got %d", path, w.Code)
			}
			if strings.TrimSpace(w.Body.String()) == "" {
				t.Fatalf("%s returned empty body", path)
			}
		}
	})

	t.Run("status endpoint returns mode and service snapshot", func(t *testing.T) {
		w := httptest.NewRecorder()
		r.ServeHTTP(w, authedRequest(http.MethodGet, "/api/status"))
		if w.Code != http.StatusOK {
			t.Fatalf("want 200 got %d", w.Code)
		}
		resp := decodeJSONMap(t, w.Body.Bytes())
		if resp["mode"] != "B" {
			t.Fatalf("want mode B got %v", resp["mode"])
		}
	})

	t.Run("cron can save and read back", func(t *testing.T) {
		post := httptest.NewRecorder()
		r.ServeHTTP(post, authedJSONRequest(http.MethodPost, "/api/cron", `{"enabled":true,"time":"03:30","schedule_type":"weekly","weekday":5,"monthday":20}`))
		if post.Code != http.StatusOK {
			t.Fatalf("want 200 got %d", post.Code)
		}
		get := httptest.NewRecorder()
		r.ServeHTTP(get, authedRequest(http.MethodGet, "/api/cron"))
		if get.Code != http.StatusOK {
			t.Fatalf("want 200 got %d", get.Code)
		}
		resp := decodeJSONMap(t, get.Body.Bytes())
		if resp["enabled"] != true || resp["time"] != "03:30" || resp["schedule_type"] != "weekly" || resp["weekday"].(float64) != 5 {
			t.Fatalf("unexpected cron payload: %v", resp)
		}
	})

	t.Run("traffic and ospf summaries reflect seeded rows", func(t *testing.T) {
		traffic := httptest.NewRecorder()
		r.ServeHTTP(traffic, authedRequest(http.MethodGet, "/api/traffic"))
		if traffic.Code != http.StatusOK {
			t.Fatalf("want 200 got %d", traffic.Code)
		}
		trafficResp := decodeJSONMap(t, traffic.Body.Bytes())
		totalMonth := trafficResp["total_month"].(map[string]interface{})
		if totalMonth["up"].(float64) != 400 || totalMonth["down"].(float64) != 600 {
			t.Fatalf("unexpected traffic totals: %v", trafficResp)
		}
		ranking := trafficResp["node_ranking"].([]interface{})
		if len(ranking) != 2 {
			t.Fatalf("unexpected node ranking count: %v", trafficResp)
		}
		top := ranking[0].(map[string]interface{})
		if top["node_id"].(float64) != 1 || top["total_bytes"].(float64) != 300 {
			t.Fatalf("unexpected top node ranking: %v", top)
		}

		ospf := httptest.NewRecorder()
		r.ServeHTTP(ospf, authedRequest(http.MethodGet, "/api/ospf"))
		if ospf.Code != http.StatusOK {
			t.Fatalf("want 200 got %d", ospf.Code)
		}
		ospfResp := decodeJSONMap(t, ospf.Body.Bytes())
		if ospfResp["published"].(float64) != 1 || ospfResp["pending"].(float64) != 1 {
			t.Fatalf("unexpected ospf payload: %v", ospfResp)
		}
		if ospfResp["push_batch_limit"].(float64) != 500 || ospfResp["push_interval_seconds"].(float64) != 10 {
			t.Fatalf("unexpected ospf controller defaults: %v", ospfResp)
		}

		update := httptest.NewRecorder()
		r.ServeHTTP(update, authedJSONRequest(http.MethodPost, "/api/ospf/settings", `{"push_batch_limit":77,"push_interval_seconds":13}`))
		if update.Code != http.StatusOK {
			t.Fatalf("want 200 got %d body=%s", update.Code, update.Body.String())
		}

		ospfReload := httptest.NewRecorder()
		r.ServeHTTP(ospfReload, authedRequest(http.MethodGet, "/api/ospf"))
		if ospfReload.Code != http.StatusOK {
			t.Fatalf("want 200 got %d", ospfReload.Code)
		}
		ospfReloadResp := decodeJSONMap(t, ospfReload.Body.Bytes())
		if ospfReloadResp["push_batch_limit"].(float64) != 77 || ospfReloadResp["push_interval_seconds"].(float64) != 13 {
			t.Fatalf("unexpected ospf controller payload after update: %v", ospfReloadResp)
		}

		events := httptest.NewRecorder()
		r.ServeHTTP(events, authedRequest(http.MethodGet, "/api/events?limit=5"))
		if events.Code != http.StatusOK {
			t.Fatalf("want 200 got %d", events.Code)
		}
		eventsResp := decodeJSONMap(t, events.Body.Bytes())
		if eventsResp["success"] != true {
			t.Fatalf("unexpected events payload: %v", eventsResp)
		}
		if len(eventsResp["events"].([]interface{})) == 0 {
			t.Fatalf("expected non-empty events payload: %v", eventsResp)
		}

		nftStats := httptest.NewRecorder()
		r.ServeHTTP(nftStats, authedRequest(http.MethodGet, "/api/nftables/stats"))
		if nftStats.Code != http.StatusOK {
			t.Fatalf("want 200 got %d", nftStats.Code)
		}
		nftResp := decodeJSONMap(t, nftStats.Body.Bytes())
		if nftResp["success"] != true {
			t.Fatalf("unexpected nft stats payload: %v", nftResp)
		}
	})
}

func TestFeatureSuite_DNSRulesAndNodes(t *testing.T) {
	r := setupFeatureSuiteRouter(t)

	t.Run("dns endpoint returns configured defaults", func(t *testing.T) {
		w := httptest.NewRecorder()
		r.ServeHTTP(w, authedRequest(http.MethodGet, "/api/dns"))
		if w.Code != http.StatusOK {
			t.Fatalf("want 200 got %d", w.Code)
		}
		resp := decodeJSONMap(t, w.Body.Bytes())
		if resp["local"] != "223.5.5.5" || resp["remote"] != "8.8.8.8" || resp["mode"] != "smart" {
			t.Fatalf("unexpected dns payload: %v", resp)
		}
	})

	t.Run("dns rejects invalid upstream payload", func(t *testing.T) {
		w := httptest.NewRecorder()
		r.ServeHTTP(w, authedJSONRequest(http.MethodPost, "/api/dns", `{"Local":"\n","Remote":"8.8.8.8","Lazy":true,"Mode":"smart"}`))
		if w.Code != http.StatusBadRequest {
			t.Fatalf("want 400 got %d", w.Code)
		}
	})

	t.Run("rules support list create validation and delete", func(t *testing.T) {
		list := httptest.NewRecorder()
		r.ServeHTTP(list, authedRequest(http.MethodGet, "/api/rules"))
		if list.Code != http.StatusOK {
			t.Fatalf("want 200 got %d", list.Code)
		}
		payload := decodeJSONMap(t, list.Body.Bytes())
		arr := payload["rules"].([]interface{})
		if len(arr) != 2 {
			t.Fatalf("want 2 rules got %d", len(arr))
		}

		invalid := httptest.NewRecorder()
		r.ServeHTTP(invalid, authedJSONRequest(http.MethodPost, "/api/rules", `{"Type":"ip","Value":"not-an-ip","Policy":"direct"}`))
		if invalid.Code != http.StatusBadRequest {
			t.Fatalf("want 400 got %d", invalid.Code)
		}

		create := httptest.NewRecorder()
		r.ServeHTTP(create, authedJSONRequest(http.MethodPost, "/api/rules", `{"Type":"domain","Value":"google.com","Policy":"proxy"}`))
		if create.Code != http.StatusOK {
			t.Fatalf("want 200 got %d", create.Code)
		}

		dup := httptest.NewRecorder()
		r.ServeHTTP(dup, authedJSONRequest(http.MethodPost, "/api/rules", `{"Type":"domain","Value":"google.com","Policy":"direct"}`))
		if dup.Code != http.StatusConflict {
			t.Fatalf("duplicate rule should be rejected with 409, got %d body=%s", dup.Code, dup.Body.String())
		}
		var ruleCount int
		if err := db.QueryRow("SELECT COUNT(*) FROM rules WHERE value='google.com' AND policy='proxy'").Scan(&ruleCount); err != nil {
			t.Fatal(err)
		}
		if ruleCount != 1 {
			t.Fatalf("expected inserted rule, count=%d", ruleCount)
		}

		haCreate := httptest.NewRecorder()
		r.ServeHTTP(haCreate, authedJSONRequest(http.MethodPost, "/api/rules", `{"Type":"domain","Value":"ha-check.example","Policy":"ha-1-2"}`))
		if haCreate.Code != http.StatusOK {
			t.Fatalf("ha policy create should be accepted, got %d body=%s", haCreate.Code, haCreate.Body.String())
		}
		if err := db.QueryRow("SELECT COUNT(*) FROM rules WHERE value='ha-check.example' AND policy='ha-1-2'").Scan(&ruleCount); err != nil {
			t.Fatal(err)
		}
		if ruleCount != 1 {
			t.Fatalf("expected inserted ha rule, count=%d", ruleCount)
		}

		var googleID, haID int
		if err := db.QueryRow("SELECT id FROM rules WHERE value='google.com' AND policy='proxy'").Scan(&googleID); err != nil {
			t.Fatal(err)
		}
		if err := db.QueryRow("SELECT id FROM rules WHERE value='ha-check.example' AND policy='ha-1-2'").Scan(&haID); err != nil {
			t.Fatal(err)
		}
		reorder := httptest.NewRecorder()
		r.ServeHTTP(reorder, authedJSONRequest(http.MethodPut, "/api/rules/reorder", fmt.Sprintf(`{"ids":[%d,%d]}`, haID, googleID)))
		if reorder.Code != http.StatusOK {
			t.Fatalf("reorder should succeed, got %d body=%s", reorder.Code, reorder.Body.String())
		}
		var pGoogle, pHA int
		if err := db.QueryRow("SELECT priority FROM rules WHERE id=?", googleID).Scan(&pGoogle); err != nil {
			t.Fatal(err)
		}
		if err := db.QueryRow("SELECT priority FROM rules WHERE id=?", haID).Scan(&pHA); err != nil {
			t.Fatal(err)
		}
		if !(pHA < pGoogle) {
			t.Fatalf("expected reordered priority, got ha=%d google=%d", pHA, pGoogle)
		}

		remove := httptest.NewRecorder()
		r.ServeHTTP(remove, authedRequest(http.MethodDelete, "/api/rules/1"))
		if remove.Code != http.StatusOK {
			t.Fatalf("want 200 got %d", remove.Code)
		}
		if err := db.QueryRow("SELECT COUNT(*) FROM rules WHERE id=1").Scan(&ruleCount); err != nil {
			t.Fatal(err)
		}
		if ruleCount != 0 {
			t.Fatalf("expected rule 1 deleted, count=%d", ruleCount)
		}
	})

	t.Run("nodes support list create update toggle default import and delete", func(t *testing.T) {
		list := httptest.NewRecorder()
		r.ServeHTTP(list, authedRequest(http.MethodGet, "/api/nodes"))
		if list.Code != http.StatusOK {
			t.Fatalf("want 200 got %d", list.Code)
		}
		nodes := decodeJSONArray(t, list.Body.Bytes())
		if len(nodes) != 2 {
			t.Fatalf("want 2 nodes got %d", len(nodes))
		}
		var defaultSeen bool
		for _, node := range nodes {
			if int(node["id"].(float64)) == 2 && node["is_default"] == true {
				defaultSeen = true
			}
		}
		if !defaultSeen {
			t.Fatal("expected node 2 to be default")
		}

		create := httptest.NewRecorder()
		r.ServeHTTP(create, authedJSONRequest(http.MethodPost, "/api/nodes", `{"Name":"n3","Group":"g3","Type":"Vless","Address":"3.3.3.3","Port":443,"UUID":"u3","Params":"{}"}`))
		if create.Code != http.StatusOK {
			t.Fatalf("want 200 got %d", create.Code)
		}
		var createdCount int
		if err := db.QueryRow("SELECT COUNT(*) FROM nodes WHERE name='n3' AND address='3.3.3.3'").Scan(&createdCount); err != nil {
			t.Fatal(err)
		}
		if createdCount != 1 {
			t.Fatalf("expected inserted node, count=%d", createdCount)
		}

		update := httptest.NewRecorder()
		r.ServeHTTP(update, authedJSONRequest(http.MethodPut, "/api/nodes/1", `{"Name":"n1-edit","Group":"g9","Type":"Vmess","Address":"9.9.9.9","Port":9443,"UUID":"u9","Params":"{}"}`))
		if update.Code != http.StatusOK {
			t.Fatalf("want 200 got %d", update.Code)
		}
		var updatedName string
		var updatedPort int
		if err := db.QueryRow("SELECT name, port FROM nodes WHERE id=1").Scan(&updatedName, &updatedPort); err != nil {
			t.Fatal(err)
		}
		if updatedName != "n1-edit" || updatedPort != 9443 {
			t.Fatalf("unexpected updated node: %s %d", updatedName, updatedPort)
		}

		toggle := httptest.NewRecorder()
		r.ServeHTTP(toggle, authedRequest(http.MethodPut, "/api/nodes/1/toggle"))
		if toggle.Code != http.StatusOK {
			t.Fatalf("want 200 got %d", toggle.Code)
		}
		var active bool
		if err := db.QueryRow("SELECT active FROM nodes WHERE id=1").Scan(&active); err != nil {
			t.Fatal(err)
		}
		if active {
			t.Fatal("expected node 1 to be toggled inactive")
		}

		setDefault := httptest.NewRecorder()
		r.ServeHTTP(setDefault, authedRequest(http.MethodPut, "/api/nodes/1/default"))
		if setDefault.Code != http.StatusOK {
			t.Fatalf("want 200 got %d", setDefault.Code)
		}
		var defaultNode string
		if err := db.QueryRow("SELECT value FROM settings WHERE key='default_node_id'").Scan(&defaultNode); err != nil {
			t.Fatal(err)
		}
		if defaultNode != "1" {
			t.Fatalf("expected default node 1, got %s", defaultNode)
		}

		getFailover := httptest.NewRecorder()
		r.ServeHTTP(getFailover, authedRequest(http.MethodGet, "/api/nodes/failover_mode"))
		if getFailover.Code != http.StatusOK {
			t.Fatalf("want 200 got %d", getFailover.Code)
		}
		var failoverPayload map[string]string
		if err := json.Unmarshal(getFailover.Body.Bytes(), &failoverPayload); err != nil {
			t.Fatal(err)
		}
		if failoverPayload["mode"] != "normal" {
			t.Fatalf("expected default failover mode normal, got %q", failoverPayload["mode"])
		}

		setFailover := httptest.NewRecorder()
		r.ServeHTTP(setFailover, authedJSONRequest(http.MethodPut, "/api/nodes/failover_mode", `{"mode":"strict"}`))
		if setFailover.Code != http.StatusOK {
			t.Fatalf("want 200 got %d body=%s", setFailover.Code, setFailover.Body.String())
		}
		var failoverMode string
		if err := db.QueryRow("SELECT value FROM settings WHERE key='node_failover_mode'").Scan(&failoverMode); err != nil {
			t.Fatal(err)
		}
		if failoverMode != "strict" {
			t.Fatalf("expected failover mode strict, got %q", failoverMode)
		}

		badFailover := httptest.NewRecorder()
		r.ServeHTTP(badFailover, authedJSONRequest(http.MethodPut, "/api/nodes/failover_mode", `{"mode":"invalid"}`))
		if badFailover.Code != http.StatusBadRequest {
			t.Fatalf("want 400 got %d", badFailover.Code)
		}

		shareLink := "vless://123e4567-e89b-12d3-a456-426614174000@hk.example.com:443?security=reality&sni=www.microsoft.com&fp=chrome&pbk=abc123&sid=abcd&type=tcp&flow=xtls-rprx-vision&encryption=none#hk-node"
		importReq := httptest.NewRecorder()
		r.ServeHTTP(importReq, authedJSONRequest(http.MethodPost, "/api/nodes/import", `{"Url":"`+shareLink+`"}`))
		if importReq.Code != http.StatusOK {
			t.Fatalf("want 200 got %d body=%s", importReq.Code, importReq.Body.String())
		}
		var importedCount int
		if err := db.QueryRow("SELECT COUNT(*) FROM nodes WHERE name='hk-node' AND address='hk.example.com' AND type='Vless'").Scan(&importedCount); err != nil {
			t.Fatal(err)
		}
		if importedCount != 1 {
			t.Fatalf("expected imported node, count=%d", importedCount)
		}

		deleteReq := httptest.NewRecorder()
		r.ServeHTTP(deleteReq, authedRequest(http.MethodDelete, "/api/nodes/2"))
		if deleteReq.Code != http.StatusOK {
			t.Fatalf("want 200 got %d", deleteReq.Code)
		}
		var deletedCount int
		if err := db.QueryRow("SELECT COUNT(*) FROM nodes WHERE id=2").Scan(&deletedCount); err != nil {
			t.Fatal(err)
		}
		if deletedCount != 0 {
			t.Fatalf("expected node 2 deleted, count=%d", deletedCount)
		}
	})

	t.Run("connections include matched rule metadata", func(t *testing.T) {
		res, err := db.Exec("INSERT INTO rules(type, value, policy) VALUES ('domain', '**.example.net', 'proxy')")
		if err != nil {
			t.Fatal(err)
		}
		ruleID64, err := res.LastInsertId()
		if err != nil {
			t.Fatal(err)
		}
		expectedRuleID := int(ruleID64)

		connRingMutex.Lock()
		connRing = ring.New(200)
		connRing.Value = ConnectionRecord{
			Time:    "2026/04/23 06:00:00",
			Client:  "192.168.20.10:12345",
			Network: "tcp",
			Target:  "www.example.net:443",
			Policy:  "proxy",
		}
		connRing = connRing.Next()
		connRingMutex.Unlock()

		w := httptest.NewRecorder()
		r.ServeHTTP(w, authedRequest(http.MethodGet, "/api/connections?ip=192.168.20.10"))
		if w.Code != http.StatusOK {
			t.Fatalf("want 200 got %d", w.Code)
		}
		resp := decodeJSONMap(t, w.Body.Bytes())
		data := resp["data"].([]interface{})
		if len(data) != 1 {
			t.Fatalf("unexpected connections payload: %v", resp)
		}
		item := data[0].(map[string]interface{})
		ruleID, ok := item["rule_id"].(float64)
		if !ok {
			t.Fatalf("rule_id missing in connection metadata: %v", item)
		}
		if item["target_domain"].(string) != "www.example.net" {
			t.Fatalf("unexpected target_domain: %v", item)
		}
		if int(ruleID) != expectedRuleID || item["match_value"].(string) != "**.example.net" {
			t.Fatalf("unexpected connection rule metadata: %v", item)
		}
	})

	t.Run("connections prefer mapped domain when target is ip and routes_table stores /32", func(t *testing.T) {
		res, err := db.Exec("INSERT INTO rules(type, value, policy) VALUES ('domain', 'mapped-only.trace.test', 'proxy')")
		if err != nil {
			t.Fatalf("insert mapped-domain rule: %v", err)
		}
		ruleID64, err := res.LastInsertId()
		if err != nil {
			t.Fatalf("last insert id: %v", err)
		}
		expectedRuleID := int(ruleID64)
		if _, err := db.Exec("INSERT OR REPLACE INTO routes_table(ip, domain, source, first_seen, last_seen, ttl, status, miss_count) VALUES (?, ?, 'static', datetime('now', '-10 seconds'), datetime('now'), 300, 'published', 0)", "203.0.113.9/32", "mapped-only.trace.test"); err != nil {
			t.Fatalf("seed routes_table: %v", err)
		}
		connRingMutex.Lock()
		connRing = ring.New(200)
		connRing.Value = ConnectionRecord{
			Time:    "2026/04/23 06:01:00",
			Client:  "192.168.20.10:12346",
			Network: "tcp",
			Target:  "203.0.113.9:443",
			Policy:  "proxy",
		}
		connRing = connRing.Next()
		connRingMutex.Unlock()

		w := httptest.NewRecorder()
		r.ServeHTTP(w, authedRequest(http.MethodGet, "/api/connections?ip=192.168.20.10"))
		if w.Code != http.StatusOK {
			t.Fatalf("want 200 got %d", w.Code)
		}
		resp := decodeJSONMap(t, w.Body.Bytes())
		data := resp["data"].([]interface{})
		if len(data) != 1 {
			t.Fatalf("unexpected connections payload: %v", resp)
		}
		item := data[0].(map[string]interface{})
		if item["target_domain"].(string) != "mapped-only.trace.test" {
			t.Fatalf("unexpected mapped target_domain: %v", item)
		}
		ruleID, ok := item["rule_id"].(float64)
		if !ok {
			t.Fatalf("mapped rule_id missing: %v", item)
		}
		if int(ruleID) != expectedRuleID || item["match_value"].(string) != "mapped-only.trace.test" {
			t.Fatalf("unexpected mapped connection rule metadata: %v", item)
		}
	})

	t.Run("lan acl list returns seeded data", func(t *testing.T) {
		w := httptest.NewRecorder()
		r.ServeHTTP(w, authedRequest(http.MethodGet, "/api/lan_acls"))
		if w.Code != http.StatusOK {
			t.Fatalf("want 200 got %d", w.Code)
		}
		resp := decodeJSONMap(t, w.Body.Bytes())
		acls := resp["acls"].([]interface{})
		if len(acls) != 1 || resp["default_policy"] != "proxy" {
			t.Fatalf("unexpected lan acl payload: %v", resp)
		}
	})
}

func TestFeatureSuite_RemoteNodeViews(t *testing.T) {
	r := setupFeatureSuiteRouter(t)

	t.Run("remote nodes list returns seeded node", func(t *testing.T) {
		w := httptest.NewRecorder()
		r.ServeHTTP(w, authedRequest(http.MethodGet, "/api/remote_nodes"))
		if w.Code != http.StatusOK {
			t.Fatalf("want 200 got %d", w.Code)
		}
		arr := decodeJSONArray(t, w.Body.Bytes())
		if len(arr) != 1 || arr[0]["status"] != "Online" {
			t.Fatalf("unexpected remote node list: %v", arr)
		}
	})

	t.Run("remote node details expose generated vless share link", func(t *testing.T) {
		w := httptest.NewRecorder()
		r.ServeHTTP(w, authedRequest(http.MethodGet, "/api/remote_nodes/2"))
		if w.Code != http.StatusOK {
			t.Fatalf("want 200 got %d", w.Code)
		}
		resp := decodeJSONMap(t, w.Body.Bytes())
		vless := resp["vless"].(map[string]interface{})
		if !strings.Contains(vless["share_link"].(string), "vless://") {
			t.Fatalf("unexpected remote details payload: %v", resp)
		}
	})

	t.Run("remote node history returns stored rollback snapshot", func(t *testing.T) {
		w := httptest.NewRecorder()
		r.ServeHTTP(w, authedRequest(http.MethodGet, "/api/remote_nodes/2/history"))
		if w.Code != http.StatusOK {
			t.Fatalf("want 200 got %d", w.Code)
		}
		arr := decodeJSONArray(t, w.Body.Bytes())
		if len(arr) != 1 {
			t.Fatalf("want 1 history row got %d", len(arr))
		}
	})
}

func TestFeatureSuite_NodeImportAcceptsRemoteVLESSShareLink(t *testing.T) {
	r := setupFeatureSuiteRouter(t)
	shareLink := "vless://a64bc5e0-abd8-4015-a904-4ababd2b88ce@192.168.20.152:21508?security=reality&sni=www.microsoft.com&fp=chrome&pbk=pub&sid=6c4368e699a21562&type=tcp&flow=xtls-rprx-vision&encryption=none#192.168.20.152"
	encoded := base64.StdEncoding.EncodeToString([]byte(`{"v":"2","ps":"ignored","add":"vm.example.com","port":"443","id":"123e4567-e89b-12d3-a456-426614174999"}`))

	cases := []struct {
		name string
		url  string
	}{
		{name: "vless share link", url: shareLink},
		{name: "vmess share link", url: "vmess://" + encoded},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			r.ServeHTTP(w, authedJSONRequest(http.MethodPost, "/api/nodes/import", `{"Url":"`+tc.url+`"}`))
			if w.Code != http.StatusOK {
				t.Fatalf("want 200 got %d body=%s", w.Code, w.Body.String())
			}
		})
	}
}
