package main

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	_ "github.com/mattn/go-sqlite3"
)

func setupTestDB(t *testing.T) (*sql.DB, string) {
	t.Helper()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")
	tdb, err := sql.Open("sqlite3", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	stmts := []string{
		`CREATE TABLE settings (key TEXT PRIMARY KEY, value TEXT);`,
		`CREATE TABLE rules (id INTEGER PRIMARY KEY AUTOINCREMENT, type TEXT, value TEXT, policy TEXT, group_id TEXT NOT NULL DEFAULT '', group_name TEXT NOT NULL DEFAULT '');`,
		`CREATE TABLE nodes (id INTEGER PRIMARY KEY AUTOINCREMENT, name TEXT, grp TEXT, type TEXT, address TEXT, port INTEGER, uuid TEXT, params TEXT, active BOOLEAN DEFAULT 1, ping INTEGER DEFAULT 0);`,
	}
	for _, s := range stmts {
		if _, err := tdb.Exec(s); err != nil {
			t.Fatal(err)
		}
	}
	_, _ = tdb.Exec(`INSERT INTO settings(key,value) VALUES
	('password','admin'),
	('dns_local','223.5.5.5'),
	('dns_remote','8.8.8.8'),
	('dns_lazy','true'),
	('mode','B')`)
	_, _ = tdb.Exec(`INSERT INTO rules(type,value,policy) VALUES('domain','example.com','proxy')`)
	return tdb, dbPath
}

func setupTestRouter(t *testing.T) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	tdb, _ := setupTestDB(t)
	db = tdb
	t.Cleanup(func() {
		db.Close()
	})
	sessions.Store("test-token", SessionInfo{ExpiresAt: time.Now().Add(time.Hour)})
	r := gin.New()
	registerAPIRoutes(r)
	return r
}

func TestLoginSuccess(t *testing.T) {
	r := setupTestRouter(t)
	body := `{"Password":"admin"}`
	req := httptest.NewRequest(http.MethodPost, "/api/login", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("want 200 got %d", w.Code)
	}
	var resp map[string]string
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp["token"] == "" {
		t.Fatalf("unexpected token: %s", resp["token"])
	}
}

func TestDNSUnauthorized(t *testing.T) {
	r := setupTestRouter(t)
	req := httptest.NewRequest(http.MethodGet, "/api/dns", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("want 401 got %d", w.Code)
	}
}

func TestDNSAuthorized(t *testing.T) {
	r := setupTestRouter(t)
	req := httptest.NewRequest(http.MethodGet, "/api/dns", nil)
	req.Header.Set("Authorization", "Bearer test-token")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("want 200 got %d", w.Code)
	}
	var resp map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp["local"] != "223.5.5.5" {
		t.Fatalf("unexpected local dns: %v", resp["local"])
	}
	if resp["mode"] != "smart" {
		t.Fatalf("unexpected dns mode: %v", resp["mode"])
	}
}

func TestRulesAuthorized(t *testing.T) {
	r := setupTestRouter(t)
	req := httptest.NewRequest(http.MethodGet, "/api/rules", nil)
	req.Header.Set("Authorization", "Bearer test-token")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("want 200 got %d", w.Code)
	}
	var payload map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	arr, _ := payload["rules"].([]interface{})
	if len(arr) != 1 {
		t.Fatalf("want 1 rule got %d body=%s", len(arr), w.Body.String())
	}
}

func TestLoginRejectWrongPassword(t *testing.T) {
	r := setupTestRouter(t)
	body := `{"Password": "***"}`
	req := httptest.NewRequest(http.MethodPost, "/api/login", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("want 401 got %d", w.Code)
	}
}

func TestRulesRejectWildcardDomainOutsideModeA(t *testing.T) {
	r := setupTestRouter(t)
	req := httptest.NewRequest(http.MethodPost, "/api/rules", strings.NewReader(`{"Type":"domain","Value":"*.c.com","Policy":"proxy"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer test-token")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("want 400 got %d body=%s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "Mode A") {
		t.Fatalf("unexpected body: %s", w.Body.String())
	}
}

func TestRulesAllowWildcardDomainInModeA(t *testing.T) {
	r := setupTestRouter(t)
	if _, err := db.Exec(`INSERT OR REPLACE INTO settings(key,value) VALUES('mode','A')`); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/rules", strings.NewReader(`{"Type":"domain","Value":"*.c.com","Policy":"proxy"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer test-token")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("want 200 got %d body=%s", w.Code, w.Body.String())
	}
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM rules WHERE type='domain' AND value='*.c.com'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("unexpected count: %d", count)
	}
}

func TestRulesRejectInvalidGeositeTag(t *testing.T) {
	r := setupTestRouter(t)

	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "core", "mosdns"), 0o755); err != nil {
		t.Fatal(err)
	}
	oldHome := os.Getenv("PROXYGW_HOME")
	if err := os.Setenv("PROXYGW_HOME", root); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Setenv("PROXYGW_HOME", oldHome) })
	writeTestGeoData(t)

	req := httptest.NewRequest(http.MethodPost, "/api/rules", strings.NewReader(`{"Type":"geosite","Value":"anthropic","Policy":"proxy"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer test-token")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("want 400 got %d body=%s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "invalid geosite tag") {
		t.Fatalf("unexpected body: %s", w.Body.String())
	}
}

func TestRulesRejectInvalidGeoIPTag(t *testing.T) {
	r := setupTestRouter(t)

	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "core", "mosdns"), 0o755); err != nil {
		t.Fatal(err)
	}
	oldHome := os.Getenv("PROXYGW_HOME")
	if err := os.Setenv("PROXYGW_HOME", root); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Setenv("PROXYGW_HOME", oldHome) })
	writeTestGeoData(t)

	req := httptest.NewRequest(http.MethodPost, "/api/rules", strings.NewReader(`{"Type":"geoip","Value":"not-a-real-tag","Policy":"proxy"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer test-token")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("want 400 got %d body=%s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "invalid geoip tag") {
		t.Fatalf("unexpected body: %s", w.Body.String())
	}
}

func TestRulesRejectInvalidNodePolicy(t *testing.T) {
	r := setupTestRouter(t)
	req := httptest.NewRequest(http.MethodPost, "/api/rules", strings.NewReader(`{"Type":"domain","Value":"example.org","Policy":"proxy-999"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer test-token")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("want 400 got %d body=%s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "node 999 not found") {
		t.Fatalf("unexpected body: %s", w.Body.String())
	}
}

func TestRulesBatchCreateRenameFilterAndDeleteGroup(t *testing.T) {
	r := setupTestRouter(t)
	if _, err := db.Exec(`INSERT OR REPLACE INTO settings(key,value) VALUES('mode','A')`); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/rules", strings.NewReader(`{"Type":"domain","Value":"a.com, b.com，c.com","Policy":"proxy"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer test-token")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("want 200 got %d body=%s", w.Code, w.Body.String())
	}
	var resp map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if int(resp["count"].(float64)) != 3 {
		t.Fatalf("unexpected count: %v", resp)
	}
	groupID, _ := resp["group_id"].(string)
	if groupID == "" {
		t.Fatalf("missing group_id: %v", resp)
	}
	rows, err := db.Query(`SELECT value, group_id, group_name FROM rules WHERE value IN ('a.com','b.com','c.com') ORDER BY value`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	seen := 0
	for rows.Next() {
		var value, gotGroup, gotName string
		if err := rows.Scan(&value, &gotGroup, &gotName); err != nil {
			t.Fatal(err)
		}
		if gotGroup != groupID || gotName != "" {
			t.Fatalf("rule %s unexpected grouping (%q,%q)", value, gotGroup, gotName)
		}
		seen++
	}
	if seen != 3 {
		t.Fatalf("unexpected inserted rows: %d", seen)
	}

	renameReq := httptest.NewRequest(http.MethodPut, "/api/rules/group/"+groupID, strings.NewReader(`{"group_name":"海外域名组"}`))
	renameReq.Header.Set("Content-Type", "application/json")
	renameReq.Header.Set("Authorization", "Bearer test-token")
	renameW := httptest.NewRecorder()
	r.ServeHTTP(renameW, renameReq)
	if renameW.Code != http.StatusOK {
		t.Fatalf("want 200 got %d body=%s", renameW.Code, renameW.Body.String())
	}
	var renamed int
	if err := db.QueryRow(`SELECT COUNT(*) FROM rules WHERE group_id=? AND group_name='海外域名组'`, groupID).Scan(&renamed); err != nil {
		t.Fatal(err)
	}
	if renamed != 3 {
		t.Fatalf("group rename failed, count=%d", renamed)
	}

	listReq := httptest.NewRequest(http.MethodGet, "/api/rules?group_id="+groupID, nil)
	listReq.Header.Set("Authorization", "Bearer test-token")
	listW := httptest.NewRecorder()
	r.ServeHTTP(listW, listReq)
	if listW.Code != http.StatusOK {
		t.Fatalf("want 200 got %d body=%s", listW.Code, listW.Body.String())
	}
	var payload2 map[string]interface{}
	if err := json.Unmarshal(listW.Body.Bytes(), &payload2); err != nil {
		t.Fatal(err)
	}
	ruleList, _ := payload2["rules"].([]interface{})
	if len(ruleList) != 3 {
		t.Fatalf("unexpected filtered list len=%d body=%s", len(ruleList), listW.Body.String())
	}
	for _, raw := range ruleList {
		item := raw.(map[string]interface{})
		if item["group_id"] != groupID || item["group_name"] != "海外域名组" {
			t.Fatalf("unexpected filtered item: %v", item)
		}
	}

	delReq := httptest.NewRequest(http.MethodDelete, "/api/rules/group/"+groupID, nil)
	delReq.Header.Set("Authorization", "Bearer test-token")
	delW := httptest.NewRecorder()
	r.ServeHTTP(delW, delReq)
	if delW.Code != http.StatusOK {
		t.Fatalf("want 200 got %d body=%s", delW.Code, delW.Body.String())
	}
	var left int
	if err := db.QueryRow(`SELECT COUNT(*) FROM rules WHERE group_id=?`, groupID).Scan(&left); err != nil {
		t.Fatal(err)
	}
	if left != 0 {
		t.Fatalf("group delete failed, left=%d", left)
	}
}

func TestMain(m *testing.M) {
	os.Exit(m.Run())
}

func TestCronSaveAndReadBack(t *testing.T) {
	r := setupTestRouter(t)

	postBody := `{"Enabled":true,"Time":"03:30"}`
	reqPost := httptest.NewRequest(http.MethodPost, "/api/cron", strings.NewReader(postBody))
	reqPost.Header.Set("Content-Type", "application/json")
	reqPost.Header.Set("Authorization", "Bearer test-token")
	wPost := httptest.NewRecorder()
	r.ServeHTTP(wPost, reqPost)
	if wPost.Code != http.StatusOK {
		t.Fatalf("want 200 got %d", wPost.Code)
	}

	reqGet := httptest.NewRequest(http.MethodGet, "/api/cron", nil)
	reqGet.Header.Set("Authorization", "Bearer test-token")
	wGet := httptest.NewRecorder()
	r.ServeHTTP(wGet, reqGet)
	if wGet.Code != http.StatusOK {
		t.Fatalf("want 200 got %d", wGet.Code)
	}
	var resp map[string]interface{}
	if err := json.Unmarshal(wGet.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp["enabled"] != true {
		t.Fatalf("unexpected enabled: %v", resp["enabled"])
	}
	if resp["time"] != "03:30" {
		t.Fatalf("unexpected time: %v", resp["time"])
	}
}

func TestPasswordChangeFlow(t *testing.T) {
	r := setupTestRouter(t)
	newPassword := "newpass88"
	body := `{"Old":"admin","New":"` + newPassword + `"}`
	req := httptest.NewRequest(http.MethodPost, "/api/password", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer test-token")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("want 200 got %d", w.Code)
	}

	login := httptest.NewRequest(http.MethodPost, "/api/login", strings.NewReader(`{"Password":"`+newPassword+`"}`))
	login.Header.Set("Content-Type", "application/json")
	lw := httptest.NewRecorder()
	r.ServeHTTP(lw, login)
	if lw.Code != http.StatusOK {
		t.Fatalf("login with new password failed: %d", lw.Code)
	}
}

func TestNodeUpdateEndpointExists(t *testing.T) {
	r := setupTestRouter(t)
	_, _ = db.Exec("INSERT INTO nodes(name,grp,type,address,port,uuid,params,active,ping) VALUES('n1','g1','Vmess','1.1.1.1',443,'u1','{}',1,0)")
	body := `{"Name":"n1-edit","Group":"g2","Type":"Vmess","Address":"2.2.2.2","Port":8443,"UUID":"u2","Params":"{}"}`
	req := httptest.NewRequest(http.MethodPut, "/api/nodes/1", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer test-token")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("want 200 got %d", w.Code)
	}
	var name string
	_ = db.QueryRow("SELECT name FROM nodes WHERE id=1").Scan(&name)
	if name != "n1-edit" {
		t.Fatalf("update not applied, got %s", name)
	}
}
