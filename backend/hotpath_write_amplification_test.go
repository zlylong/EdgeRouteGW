package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func settingRaw(t *testing.T, key string) (string, bool) {
	t.Helper()
	var raw string
	err := db.QueryRow("SELECT value FROM settings WHERE key=?", key).Scan(&raw)
	if err != nil {
		return "", false
	}
	return raw, true
}

func TestReadIntSettingWithDefaultDoesNotRewriteAnExistingRow(t *testing.T) {
	setupFeatureSuiteRouter(t)
	if _, err := db.Exec("INSERT INTO settings(key, value) VALUES ('ospf_push_batch_limit', ' 300')"); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if got := readIntSettingWithDefault("ospf_push_batch_limit", defaultOspfPushBatchLimit, clampOspfPushBatchLimit); got != 300 {
			t.Fatalf("call %d: got %d want 300", i, got)
		}
	}
	if raw, _ := settingRaw(t, "ospf_push_batch_limit"); raw != " 300" {
		t.Fatalf("existing row was rewritten: %q", raw)
	}

	// Missing key: created once with the default, then left alone.
	if got := readIntSettingWithDefault("ospf_resolve_workers", defaultOspfResolveWorkers, clampOspfResolveWorkers); got != defaultOspfResolveWorkers {
		t.Fatalf("got %d want default %d", got, defaultOspfResolveWorkers)
	}
	if raw, ok := settingRaw(t, "ospf_resolve_workers"); !ok || raw != "16" {
		t.Fatalf("default not persisted: %q %v", raw, ok)
	}

	// Out-of-range value: clamped and the clamped value written back.
	if _, err := db.Exec("INSERT INTO settings(key, value) VALUES ('ospf_push_interval_seconds', '99999')"); err != nil {
		t.Fatal(err)
	}
	got := readIntSettingWithDefault("ospf_push_interval_seconds", defaultOspfPushIntervalSeconds, clampOspfPushIntervalSeconds)
	if got != clampOspfPushIntervalSeconds(99999) {
		t.Fatalf("clamp not applied: %d", got)
	}
	if raw, _ := settingRaw(t, "ospf_push_interval_seconds"); raw == "99999" {
		t.Fatalf("clamped value was not persisted")
	}
}

func TestOspfControllerTickHonoursPushInterval(t *testing.T) {
	setupFeatureSuiteRouter(t)
	if _, err := db.Exec("INSERT OR REPLACE INTO settings(key, value) VALUES ('ospf_push_interval_seconds', '60')"); err != nil {
		t.Fatal(err)
	}
	// A candidate old enough to be pushed on the first tick.
	if _, err := db.Exec("UPDATE routes_table SET first_seen = datetime('now', '-5 minutes') WHERE ip='1.1.1.1/32'"); err != nil {
		t.Fatal(err)
	}

	var calls int32
	oldRunner := runVtyshConfigBatch
	runVtyshConfigBatch = func(config string) (string, error) {
		atomic.AddInt32(&calls, 1)
		return "ok", nil
	}
	defer func() { runVtyshConfigBatch = oldRunner }()

	st := &ospfControllerState{lastReconcile: time.Now()}
	ospfControllerTick(st)
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("first tick: vtysh calls = %d want 1", got)
	}
	// Re-arm a candidate; the second tick inside the interval must not push.
	if _, err := db.Exec("UPDATE routes_table SET status='candidate', first_seen = datetime('now', '-5 minutes') WHERE ip='1.1.1.1/32'"); err != nil {
		t.Fatal(err)
	}
	ospfControllerTick(st)
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("second tick inside push interval pushed again: calls = %d", got)
	}
}

func TestOspfControllerTickInModeADoesNotTouchSettings(t *testing.T) {
	setupFeatureSuiteRouter(t)
	if _, err := db.Exec("UPDATE settings SET value='A' WHERE key='mode'"); err != nil {
		t.Fatal(err)
	}
	st := &ospfControllerState{}
	ospfControllerTick(st)
	ospfControllerTick(st)
	if _, ok := settingRaw(t, "ospf_push_interval_seconds"); ok {
		t.Fatalf("Mode A tick persisted OSPF settings")
	}
	var published int
	if err := db.QueryRow("SELECT COUNT(*) FROM routes_table WHERE status='published'").Scan(&published); err != nil {
		t.Fatal(err)
	}
	if published != 0 {
		t.Fatalf("published routes were not demoted in Mode A: %d", published)
	}
}

func TestGetCronDoesNotRewriteExistingSettings(t *testing.T) {
	r := setupFeatureSuiteRouter(t)
	if _, err := db.Exec("UPDATE settings SET value='04:00 ' WHERE key='cron_time'"); err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, authedRequest(http.MethodGet, "/api/cron"))
	if w.Code != http.StatusOK {
		t.Fatalf("want 200 got %d", w.Code)
	}
	if raw, _ := settingRaw(t, "cron_time"); raw != "04:00 " {
		t.Fatalf("GET /api/cron rewrote cron_time: %q", raw)
	}
	if raw, ok := settingRaw(t, "cron_weekday"); !ok || raw != "1" {
		t.Fatalf("missing cron_weekday default not created: %q %v", raw, ok)
	}
}

func TestEnsureDefaultNetworkRoleSettingsLeavesExistingRowsAlone(t *testing.T) {
	setupFeatureSuiteRouter(t)
	if _, err := db.Exec("UPDATE settings SET value='eth0 ' WHERE key='management_iface'"); err != nil {
		t.Fatal(err)
	}
	ensureDefaultNetworkRoleSettings()
	if raw, _ := settingRaw(t, "management_iface"); raw != "eth0 " {
		t.Fatalf("existing management_iface rewritten: %q", raw)
	}
}

func TestStatusProbesVersionsOnceWithinTTL(t *testing.T) {
	r := setupFeatureSuiteRouter(t)
	invalidateStatusStaticInfo()
	var probes int32
	oldProbe := probeVersion
	oldUnit := probeUnitActive
	probeVersion = func(name string, args ...string) (string, error) {
		atomic.AddInt32(&probes, 1)
		return "Xray 25.1.1 (Xray, Penetrates Everything.)", nil
	}
	probeUnitActive = func(unit string) bool { return false }
	defer func() {
		probeVersion = oldProbe
		probeUnitActive = oldUnit
		invalidateStatusStaticInfo()
	}()

	for i := 0; i < 3; i++ {
		w := httptest.NewRecorder()
		r.ServeHTTP(w, authedRequest(http.MethodGet, "/api/status"))
		if w.Code != http.StatusOK {
			t.Fatalf("want 200 got %d", w.Code)
		}
	}
	// xray + mosdns version probes; frr is inactive so vtysh is skipped.
	if got := atomic.LoadInt32(&probes); got != 2 {
		t.Fatalf("version probes across 3 polls = %d, want 2 (cached)", got)
	}
	invalidateStatusStaticInfo()
	w := httptest.NewRecorder()
	r.ServeHTTP(w, authedRequest(http.MethodGet, "/api/status"))
	if got := atomic.LoadInt32(&probes); got != 4 {
		t.Fatalf("after invalidation probes = %d, want 4", got)
	}
	start := time.Now()
	w = httptest.NewRecorder()
	r.ServeHTTP(w, authedRequest(http.MethodGet, "/api/status"))
	if cost := time.Since(start); cost > 150*time.Millisecond {
		t.Fatalf("cached status poll took %s; it must not sleep or fork", cost)
	}
}

func TestIsExpectedProbeFailure(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want bool
	}{
		{"systemctl", []string{"is-active", "--quiet", "frr"}, true},
		{"systemctl", []string{"restart", "frr"}, false},
		{"/root/proxygw/core/xray/xray", []string{"api", "statsquery", "-server=127.0.0.1:10085"}, true},
		{"/root/proxygw/core/xray/xray", []string{"-test", "-c", "x.json"}, false},
		{"nft", []string{"list", "ruleset"}, false},
	}
	for _, tc := range cases {
		if got := isExpectedProbeFailure(tc.name, tc.args); got != tc.want {
			t.Errorf("%s %v: got %v want %v", tc.name, tc.args, got, tc.want)
		}
	}
}

func TestPersistTrafficMinuteWritesAllRowsInOneTransaction(t *testing.T) {
	setupFeatureSuiteRouter(t)
	before := func(table string) int {
		var n int
		if err := db.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	th, nth := before("traffic_history"), before("node_traffic_history")
	if err := persistTrafficMinute(10, 20, map[int]trafficBytes{1: {up: 5, down: 6}, 2: {up: 0, down: 0}, 3: {up: 1, down: 0}}); err != nil {
		t.Fatal(err)
	}
	if got := before("traffic_history") - th; got != 1 {
		t.Fatalf("traffic_history rows added = %d want 1", got)
	}
	if got := before("node_traffic_history") - nth; got != 2 {
		t.Fatalf("node_traffic_history rows added = %d want 2 (zero rows skipped)", got)
	}
	if err := persistTrafficMinute(0, 0, nil); err != nil {
		t.Fatal(err)
	}
	if got := before("traffic_history") - th; got != 1 {
		t.Fatalf("empty minute wrote a row")
	}
}

func TestExpiredDNSCachePruneKeepsUnexpiredRows(t *testing.T) {
	setupFeatureSuiteRouter(t)
	now := time.Now().Unix()
	rows := []struct {
		domain   string
		expireAt interface{}
	}{
		{"future.example", now + 600},
		{"past.example", now - 600},
		{"legacy-text.example", "300"},
	}
	for _, r := range rows {
		if _, err := db.Exec("INSERT INTO domain_resolve_cache(domain, ips_json, resolved_at, expire_at) VALUES (?, '[\"1.2.3.4\"]', ?, ?)", r.domain, now-1, r.expireAt); err != nil {
			t.Fatal(err)
		}
	}
	var q *dbPruneQuery
	for i := range dbPruneQueries {
		if dbPruneQueries[i].name == "Expired DNS Cache" {
			q = &dbPruneQueries[i]
		}
	}
	if q == nil {
		t.Fatal("prune query for the DNS cache not found")
	}
	if _, err := db.Exec(q.sql); err != nil {
		t.Fatal(err)
	}
	var left []string
	rs, err := db.Query("SELECT domain FROM domain_resolve_cache ORDER BY domain")
	if err != nil {
		t.Fatal(err)
	}
	defer rs.Close()
	for rs.Next() {
		var d string
		_ = rs.Scan(&d)
		left = append(left, d)
	}
	if len(left) != 1 || left[0] != "future.example" {
		t.Fatalf("rows left after prune = %v, want only future.example", left)
	}
}

func TestDomainGeoIPLockTableEnsuredOncePerDB(t *testing.T) {
	setupFeatureSuiteRouter(t)
	domainGeoIPLockEnsureMu.Lock()
	domainGeoIPLockEnsured = nil
	domainGeoIPLockEnsureMu.Unlock()

	ensureDomainGeoIPLockTable()
	if domainGeoIPLockEnsured != getDB() {
		t.Fatal("table not recorded as ensured for the active DB")
	}
	if _, err := db.Exec("DROP TABLE domain_geoip_lock"); err != nil {
		t.Fatal(err)
	}
	ensureDomainGeoIPLockTable()
	var n int
	if err := db.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='domain_geoip_lock'").Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatal("DDL was re-issued on the same DB; the once-per-DB guard is not working")
	}
	domainGeoIPLockEnsureMu.Lock()
	domainGeoIPLockEnsured = nil
	domainGeoIPLockEnsureMu.Unlock()
	ensureDomainGeoIPLockTable()
	if err := db.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='domain_geoip_lock'").Scan(&n); err != nil || n != 1 {
		t.Fatalf("table not recreated after guard reset: n=%d err=%v", n, err)
	}
}

func TestValidateSessionSlidesAtMostHourly(t *testing.T) {
	clearSyncMap(&sessions)
	defer clearSyncMap(&sessions)

	fresh := time.Now().Add(sessionTTL - time.Minute)
	sessions.Store("fresh", SessionInfo{ExpiresAt: fresh})
	if !validateSession("fresh") {
		t.Fatal("fresh session rejected")
	}
	if v, _ := sessions.Load("fresh"); !v.(SessionInfo).ExpiresAt.Equal(fresh) {
		t.Fatal("fresh session was re-stored on validate")
	}

	old := time.Now().Add(time.Hour)
	sessions.Store("aged", SessionInfo{ExpiresAt: old})
	if !validateSession("aged") {
		t.Fatal("aged session rejected")
	}
	if v, _ := sessions.Load("aged"); !v.(SessionInfo).ExpiresAt.After(old.Add(20 * time.Hour)) {
		t.Fatal("aged session was not slid forward")
	}

	sessions.Store("expired", SessionInfo{ExpiresAt: time.Now().Add(-time.Minute)})
	if _, err := createSession(); err != nil {
		t.Fatal(err)
	}
	if _, ok := sessions.Load("expired"); ok {
		t.Fatal("expired session survived createSession pruning")
	}
}

func TestInsertRulesBatchAssignsSequentialPriorities(t *testing.T) {
	setupFeatureSuiteRouter(t)
	repo := NewRulesRepository()
	if err := repo.InsertRulesBatch("domain", []string{"a.example", "b.example", "c.example"}, "proxy", "g1", "G1"); err != nil {
		t.Fatal(err)
	}
	rows, err := db.Query("SELECT value, priority FROM rules WHERE group_id='g1' ORDER BY priority")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var prev int
	n := 0
	for rows.Next() {
		var v string
		var p int
		if err := rows.Scan(&v, &p); err != nil {
			t.Fatal(err)
		}
		if n > 0 && p != prev+1 {
			t.Fatalf("priorities not sequential: %s has %d after %d", v, p, prev)
		}
		prev = p
		n++
	}
	if n != 3 {
		t.Fatalf("inserted %d rows want 3", n)
	}
}

func TestCommandExecutorAcquireGivesUpOnCancelledContext(t *testing.T) {
	e := newCommandExecutor(1, time.Second)
	release, err := e.acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	start := time.Now()
	res := e.runCombinedOutputCtx(ctx, "true")
	if !errors.Is(res.Err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", res.Err)
	}
	if time.Since(start) > 500*time.Millisecond {
		t.Fatal("cancelled context still waited on the semaphore")
	}
}

func TestServiceLogsAreCachedBetweenPolls(t *testing.T) {
	r := setupFeatureSuiteRouter(t)
	syslogCacheMu.Lock()
	syslogCache = map[string]syslogCacheEntry{}
	syslogCacheMu.Unlock()
	var calls int32
	old := runJournalctl
	runJournalctl = func(args ...string) ([]byte, error) {
		atomic.AddInt32(&calls, 1)
		return []byte("line1\nline2\n"), nil
	}
	defer func() { runJournalctl = old }()
	for i := 0; i < 3; i++ {
		w := httptest.NewRecorder()
		r.ServeHTTP(w, authedRequest(http.MethodGet, "/api/logs/xray"))
		if w.Code != http.StatusOK {
			t.Fatalf("want 200 got %d body=%s", w.Code, w.Body.String())
		}
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("journalctl forked %d times across 3 polls, want 1", got)
	}
}

func TestSQLiteDSNAppliesPragmasPerConnection(t *testing.T) {
	d, err := openSQLite(t.TempDir() + "/pool.db")
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	d.SetMaxOpenConns(4)
	ctx := context.Background()
	var conns []interface{ Close() error }
	defer func() {
		for _, c := range conns {
			_ = c.Close()
		}
	}()
	for i := 0; i < 3; i++ {
		conn, err := d.Conn(ctx)
		if err != nil {
			t.Fatal(err)
		}
		conns = append(conns, conn)
		var timeout int
		if err := conn.QueryRowContext(ctx, "PRAGMA busy_timeout").Scan(&timeout); err != nil {
			t.Fatal(err)
		}
		if timeout != 5000 {
			t.Fatalf("conn %d busy_timeout = %d, want 5000 on every pooled connection", i, timeout)
		}
		var mode string
		if err := conn.QueryRowContext(ctx, "PRAGMA journal_mode").Scan(&mode); err != nil {
			t.Fatal(err)
		}
		if mode != "wal" {
			t.Fatalf("conn %d journal_mode = %q, want wal", i, mode)
		}
		var sync int
		if err := conn.QueryRowContext(ctx, "PRAGMA synchronous").Scan(&sync); err != nil {
			t.Fatal(err)
		}
		if sync != 1 {
			t.Fatalf("conn %d synchronous = %d, want 1 (NORMAL)", i, sync)
		}
		var fk int
		if err := conn.QueryRowContext(ctx, "PRAGMA foreign_keys").Scan(&fk); err != nil {
			t.Fatal(err)
		}
		if fk != 0 {
			t.Fatalf("foreign_keys enabled; remote_node_history ON DELETE CASCADE would change delete semantics")
		}
	}
}

func TestApplyOspfBatchesSurviveClosedDatabase(t *testing.T) {
	setupFeatureSuiteRouter(t)
	oldRunner := runVtyshConfigBatch
	runVtyshConfigBatch = func(config string) (string, error) { return "ok", nil }
	defer func() { runVtyshConfigBatch = oldRunner }()

	closed, err := openSQLite(t.TempDir() + "/closed.db")
	if err != nil {
		t.Fatal(err)
	}
	_ = closed.Close()
	live := getDB()
	setDB(closed)
	defer setDB(live)

	if applyOspfAddBatch([]string{"1.1.1.1/32"}) {
		t.Fatal("add batch reported success although the DB write failed")
	}
	if applyOspfDeleteBatch([]string{"8.8.8.8/32"}) {
		t.Fatal("delete batch reported success although the DB write failed")
	}
}
