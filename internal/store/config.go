package store

import (
	"context"
	"database/sql"
	"encoding/json"

	"github.com/zlylong/EdgeRouteGW/internal/model"
)

// UpdateNetworkConfig replaces the single network_config row.
func UpdateNetworkConfig(ctx context.Context, tx *sql.Tx, c model.NetworkConfig) error {
	allow, err := json.Marshal(nonNil(c.OSPFPublishAllow))
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `UPDATE network_config SET mode=?, management_iface=?, service_iface=?, fakeip_pool=?, tproxy_port=?,
		lan_default_policy=?, ospf_push_batch=?, ospf_push_interval_s=?, ospf_resolve_workers=?, ospf_publish_allowlist=?, node_failover_mode=?,
		updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE id=1`,
		string(c.Mode), c.ManagementIface, c.ServiceIface, c.FakeIPPool, c.TProxyPort, c.LANDefaultPolicy,
		c.OSPFPushBatch, c.OSPFPushIntervalSec, c.OSPFResolveWorkers, string(allow), c.NodeFailoverMode)
	return err
}

// UpdateDNSConfig replaces the single dns_config row.
func UpdateDNSConfig(ctx context.Context, tx *sql.Tx, c model.DNSConfig) error {
	local, err := json.Marshal(nonNil(c.LocalUpstreams))
	if err != nil {
		return err
	}
	remote, err := json.Marshal(nonNil(c.RemoteUpstreams))
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `UPDATE dns_config SET local_upstreams=?, remote_upstreams=?, cache_size=?, lazy_cache=?, lazy_cache_ttl=?, log_level=?,
		updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE id=1`,
		string(local), string(remote), c.CacheSize, b2i(c.LazyCache), c.LazyCacheTTL, c.LogLevel)
	return err
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}
