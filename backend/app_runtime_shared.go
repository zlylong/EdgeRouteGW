package main

import (
	"database/sql"
	"log"
	"os"
	"runtime/debug"
	"strconv"
	"strings"
	"sync"
	"time"
)

var (
	cachedGeosite []string
	cachedGeoip   []string
	cacheMutex    sync.Mutex
)

// goSafe runs fn in a goroutine with panic recovery, logging the stack trace.
// Use this instead of bare "go fn()" for all background goroutines.
func goSafe(fn func()) {
	go func() {
		defer func() {
			if r := recover(); r != nil {
				log.Printf("[PANIC] goroutine recovered: %v\n%s", r, debug.Stack())
			}
		}()
		fn()
	}()
}

var ospfLogs []string
var ospfLogsMu sync.RWMutex
var syncStaticRoutesToOSPFFunc = syncStaticRoutesToOSPF

var (
	staticRouteSyncMu      sync.Mutex
	staticRouteSyncRunning bool
	staticRouteSyncPending bool
)

var (
	domainGeoIPMatchCacheMu sync.Mutex
	domainGeoIPMatchCache   = map[string]domainGeoIPMatchCacheEntry{}
)

const (
	defaultOspfPushBatchLimit      = 500
	defaultOspfPushIntervalSeconds = 10
	defaultOspfResolveWorkers      = 16
	defaultOspfReconcileInterval   = 45 * time.Second
	domainGeoIPMatchCacheTTL       = 10 * time.Minute
	domainGeoIPMatchCacheMax       = 200000
)

type domainGeoIPMatchCacheEntry struct {
	tags      []string
	expiresAt time.Time
}

type routeState struct {
	ttl    int
	domain string
}

func addOspfLog(msg string) {
	ospfLogsMu.Lock()
	defer ospfLogsMu.Unlock()
	ospfLogs = append([]string{time.Now().Format("15:04:05") + " " + msg}, ospfLogs...)
	if len(ospfLogs) > 50 {
		ospfLogs = ospfLogs[:50]
	}
}

func getOspfLogsSnapshot() []string {
	ospfLogsMu.RLock()
	defer ospfLogsMu.RUnlock()
	out := make([]string, len(ospfLogs))
	copy(out, ospfLogs)
	return out
}

type ospfControllerSettings struct {
	PushBatchLimit      int
	PushIntervalSeconds int
	ResolveWorkers      int
}

func clampOspfPushBatchLimit(v int) int {
	switch {
	case v < 1:
		return 1
	case v > 100000:
		return 100000
	default:
		return v
	}
}

func clampOspfPushIntervalSeconds(v int) int {
	switch {
	case v < 1:
		return 1
	case v > 3600:
		return 3600
	default:
		return v
	}
}

func clampOspfResolveWorkers(v int) int {
	switch {
	case v < 1:
		return 1
	case v > 128:
		return 128
	default:
		return v
	}
}

func readIntSettingWithDefault(key string, fallback int, clamp func(int) int) int {
	value := fallback
	var raw string
	err := db.QueryRow("SELECT value FROM settings WHERE key=?", key).Scan(&raw)
	switch {
	case err == nil:
		if parsed, parseErr := strconv.Atoi(strings.TrimSpace(raw)); parseErr == nil {
			value = parsed
		}
	case err != sql.ErrNoRows:
		log.Printf("[WARN] SELECT value FROM settings WHERE key=%q err: %v", key, err)
	}
	if clamp != nil {
		value = clamp(value)
	}
	if _, err := db.Exec("INSERT OR REPLACE INTO settings (key, value) VALUES (?, ?)", key, strconv.Itoa(value)); err != nil {
		log.Printf("[WARN] persist default setting %s failed: %v", key, err)
	}
	return value
}

func getOspfControllerSettings() ospfControllerSettings {
	return ospfControllerSettings{
		PushBatchLimit:      readIntSettingWithDefault("ospf_push_batch_limit", defaultOspfPushBatchLimit, clampOspfPushBatchLimit),
		PushIntervalSeconds: readIntSettingWithDefault("ospf_push_interval_seconds", defaultOspfPushIntervalSeconds, clampOspfPushIntervalSeconds),
		ResolveWorkers:      readIntSettingWithDefault("ospf_resolve_workers", defaultOspfResolveWorkers, clampOspfResolveWorkers),
	}
}

func cloneStringSliceMain(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	out := make([]string, len(in))
	copy(out, in)
	return out
}

func getDomainGeoIPMatchCache(key string) ([]string, bool) {
	domainGeoIPMatchCacheMu.Lock()
	defer domainGeoIPMatchCacheMu.Unlock()
	entry, ok := domainGeoIPMatchCache[key]
	if !ok {
		return nil, false
	}
	if time.Now().After(entry.expiresAt) {
		delete(domainGeoIPMatchCache, key)
		return nil, false
	}
	return cloneStringSliceMain(entry.tags), true
}

func setDomainGeoIPMatchCache(key string, tags []string) {
	domainGeoIPMatchCacheMu.Lock()
	defer domainGeoIPMatchCacheMu.Unlock()
	domainGeoIPMatchCache[key] = domainGeoIPMatchCacheEntry{tags: cloneStringSliceMain(tags), expiresAt: time.Now().Add(domainGeoIPMatchCacheTTL)}
	if len(domainGeoIPMatchCache) <= domainGeoIPMatchCacheMax {
		return
	}
	now := time.Now()
	for k, v := range domainGeoIPMatchCache {
		if now.After(v.expiresAt) {
			delete(domainGeoIPMatchCache, k)
		}
	}
	if len(domainGeoIPMatchCache) <= domainGeoIPMatchCacheMax {
		return
	}
	trim := len(domainGeoIPMatchCache) - domainGeoIPMatchCacheMax
	for k := range domainGeoIPMatchCache {
		delete(domainGeoIPMatchCache, k)
		trim--
		if trim <= 0 {
			break
		}
	}
}

var runVtyshConfigBatch = func(config string) (string, error) {
	f, err := os.CreateTemp("", "proxygw_vtysh_batch-*.conf")
	if err != nil {
		return "", err
	}
	tmpFile := f.Name()
	defer os.Remove(tmpFile)
	if _, err := f.WriteString(config); err != nil {
		f.Close()
		return "", err
	}
	if err := f.Close(); err != nil {
		return "", err
	}
	res := sysCmd.runCombinedOutput("vtysh", "-f", tmpFile)
	return string(res.Output), res.Err
}

var cronUpdateChan = make(chan struct{}, 1)

type cronScheduleSettings struct {
	Enabled      bool
	Time         string
	ScheduleType string
	Weekday      int
	Monthday     int
}

var (
	applyTimer *time.Timer
	applyMutex sync.Mutex
)

var pendingMosdnsApply bool
