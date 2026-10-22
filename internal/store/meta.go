package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/zlylong/EdgeRouteGW/internal/model"
)

// Meta returns the generation counters.
func (s *Store) Meta(ctx context.Context) (model.Meta, error) {
	var m model.Meta
	var updated string
	err := s.db.QueryRowContext(ctx,
		`SELECT generation, last_applied_generation, updated_at FROM state_meta WHERE id = 1`,
	).Scan(&m.Generation, &m.LastAppliedGeneration, &updated)
	if err != nil {
		return m, fmt.Errorf("read state_meta: %w", err)
	}
	m.UpdatedAt = parseTime(updated)
	return m, nil
}

// BumpGeneration increments the desired-state generation inside tx and
// returns the new value. Every write that changes desired state calls this in
// the same transaction so the reconciler never sees a half-applied change.
func BumpGeneration(ctx context.Context, tx *sql.Tx) (int64, error) {
	var gen int64
	err := tx.QueryRowContext(ctx, `UPDATE state_meta
		SET generation = generation + 1, updated_at = strftime('%Y-%m-%dT%H:%M:%fZ','now')
		WHERE id = 1 RETURNING generation`).Scan(&gen)
	if err != nil {
		return 0, fmt.Errorf("bump generation: %w", err)
	}
	return gen, nil
}

// SetLastApplied records that the reconciler finished applying gen.
func (s *Store) SetLastApplied(ctx context.Context, gen int64) error {
	_, err := s.db.ExecContext(ctx, `UPDATE state_meta
		SET last_applied_generation = ?, updated_at = strftime('%Y-%m-%dT%H:%M:%fZ','now')
		WHERE id = 1`, gen)
	return err
}

// NetworkConfig returns the single-row network configuration.
func (s *Store) NetworkConfig(ctx context.Context) (model.NetworkConfig, error) {
	var c model.NetworkConfig
	var mode, allow, updated string
	var sniff int
	err := s.db.QueryRowContext(ctx, `SELECT mode, management_iface, service_iface, fakeip_pool,
		tproxy_port, lan_default_policy, ospf_push_batch, ospf_push_interval_s, ospf_resolve_workers,
		ospf_publish_allowlist, node_failover_mode, sniff_override, updated_at FROM network_config WHERE id = 1`,
	).Scan(&mode, &c.ManagementIface, &c.ServiceIface, &c.FakeIPPool, &c.TProxyPort, &c.LANDefaultPolicy,
		&c.OSPFPushBatch, &c.OSPFPushIntervalSec, &c.OSPFResolveWorkers, &allow, &c.NodeFailoverMode, &sniff, &updated)
	if err != nil {
		return c, fmt.Errorf("read network_config: %w", err)
	}
	c.SniffOverride = sniff != 0
	c.Mode = model.Mode(mode)
	c.UpdatedAt = parseTime(updated)
	if err := json.Unmarshal([]byte(allow), &c.OSPFPublishAllow); err != nil {
		return c, fmt.Errorf("decode ospf_publish_allowlist: %w", err)
	}
	if c.OSPFPublishAllow == nil {
		c.OSPFPublishAllow = []string{}
	}
	return c, nil
}

// DNSConfig returns the single-row DNS configuration.
func (s *Store) DNSConfig(ctx context.Context) (model.DNSConfig, error) {
	var c model.DNSConfig
	var local, remote, updated string
	var lazy, hijack, blockDoT, tap int
	err := s.db.QueryRowContext(ctx, `SELECT local_upstreams, remote_upstreams, cache_size, lazy_cache,
		lazy_cache_ttl, log_level, hijack, block_dot, tap, updated_at FROM dns_config WHERE id = 1`,
	).Scan(&local, &remote, &c.CacheSize, &lazy, &c.LazyCacheTTL, &c.LogLevel, &hijack, &blockDoT, &tap, &updated)
	if err != nil {
		return c, fmt.Errorf("read dns_config: %w", err)
	}
	c.LazyCache, c.Hijack, c.BlockDoT, c.Tap = lazy != 0, hijack != 0, blockDoT != 0, tap != 0
	c.UpdatedAt = parseTime(updated)
	if err := json.Unmarshal([]byte(local), &c.LocalUpstreams); err != nil {
		return c, fmt.Errorf("decode local_upstreams: %w", err)
	}
	if err := json.Unmarshal([]byte(remote), &c.RemoteUpstreams); err != nil {
		return c, fmt.Errorf("decode remote_upstreams: %w", err)
	}
	return c, nil
}

// parseTime accepts the ISO-8601 forms SQLite's strftime produces. A value
// that does not parse yields the zero time rather than an error: timestamps
// are informational and must never make a read fail.
func parseTime(s string) time.Time {
	for _, layout := range []string{"2006-01-02T15:04:05.000Z", time.RFC3339Nano, time.RFC3339, "2006-01-02 15:04:05"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UTC()
		}
	}
	return time.Time{}
}
