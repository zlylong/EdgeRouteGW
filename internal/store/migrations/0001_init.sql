-- 0001_init: EdgeRouteGW 2.0 base schema. See docs/DESIGN-2.0.md section 3.3.
-- Conventions: ids are UUID text; timestamps are ISO-8601 UTC text except
-- where compared arithmetically (resolve_cache, traffic samples), which use
-- integer unix seconds; booleans are INTEGER 0/1; JSON is TEXT.

CREATE TABLE state_meta (
    id                      INTEGER PRIMARY KEY CHECK (id = 1),
    generation              INTEGER NOT NULL DEFAULT 0,
    last_applied_generation INTEGER NOT NULL DEFAULT 0,
    updated_at              TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now'))
);
INSERT INTO state_meta (id) VALUES (1);

CREATE TABLE settings (
    key        TEXT PRIMARY KEY,
    value      TEXT NOT NULL,
    updated_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now'))
);

CREATE TABLE auth_admin (
    id            INTEGER PRIMARY KEY CHECK (id = 1),
    password_hash TEXT    NOT NULL,
    bootstrap     INTEGER NOT NULL DEFAULT 1,
    updated_at    TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now'))
);

CREATE TABLE sessions (
    id           TEXT PRIMARY KEY,
    created_at   TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
    expires_at   TEXT NOT NULL,
    last_seen_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
    remote_ip    TEXT NOT NULL DEFAULT '',
    user_agent   TEXT NOT NULL DEFAULT ''
);
CREATE INDEX sessions_expires ON sessions (expires_at);

CREATE TABLE api_tokens (
    id           TEXT PRIMARY KEY,
    name         TEXT NOT NULL,
    token_hash   TEXT NOT NULL UNIQUE,
    scope        TEXT NOT NULL CHECK (scope IN ('read','write')),
    created_at   TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
    last_used_at TEXT,
    expires_at   TEXT
);

CREATE TABLE nodes (
    id             TEXT PRIMARY KEY,
    name           TEXT    NOT NULL,
    protocol       TEXT    NOT NULL CHECK (protocol IN ('vmess','vless','trojan','shadowsocks','socks','http','wireguard','custom')),
    address        TEXT    NOT NULL,
    port           INTEGER NOT NULL CHECK (port BETWEEN 1 AND 65535),
    credential_enc BLOB,
    params         TEXT    NOT NULL DEFAULT '{}',
    enabled        INTEGER NOT NULL DEFAULT 1,
    is_default     INTEGER NOT NULL DEFAULT 0,
    latency_ms     INTEGER,
    created_at     TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
    updated_at     TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now'))
);
CREATE UNIQUE INDEX nodes_single_default ON nodes (is_default) WHERE is_default = 1;

CREATE TABLE outbound_groups (
    id         TEXT PRIMARY KEY,
    name       TEXT NOT NULL UNIQUE,
    kind       TEXT NOT NULL CHECK (kind IN ('failover','loadbalance')),
    strategy   TEXT NOT NULL DEFAULT '{}',
    created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
    updated_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now'))
);

CREATE TABLE outbound_group_members (
    group_id TEXT    NOT NULL REFERENCES outbound_groups (id) ON DELETE CASCADE,
    node_id  TEXT    NOT NULL REFERENCES nodes (id) ON DELETE CASCADE,
    position INTEGER NOT NULL,
    PRIMARY KEY (group_id, node_id)
);
CREATE INDEX outbound_group_members_order ON outbound_group_members (group_id, position);

CREATE TABLE rule_sets (
    id         TEXT PRIMARY KEY,
    name       TEXT    NOT NULL,
    position   INTEGER NOT NULL,
    enabled    INTEGER NOT NULL DEFAULT 1,
    created_at TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
    updated_at TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now'))
);

CREATE TABLE rules (
    id          TEXT PRIMARY KEY,
    set_id      TEXT    NOT NULL REFERENCES rule_sets (id) ON DELETE CASCADE,
    position    INTEGER NOT NULL,
    match_kind  TEXT    NOT NULL CHECK (match_kind IN ('domain','ip','geosite','geoip')),
    match_value TEXT    NOT NULL,
    action_kind TEXT    NOT NULL CHECK (action_kind IN ('direct','proxy','block','node','group')),
    action_ref  TEXT,
    enabled     INTEGER NOT NULL DEFAULT 1,
    remark      TEXT    NOT NULL DEFAULT '',
    created_at  TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
    updated_at  TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
    CHECK ((action_kind IN ('node','group')) = (action_ref IS NOT NULL))
);
CREATE INDEX rules_set_order ON rules (set_id, position);

CREATE TABLE device_policies (
    id         TEXT PRIMARY KEY,
    kind       TEXT NOT NULL CHECK (kind IN ('mac','ip','cidr')),
    value      TEXT NOT NULL,
    action     TEXT NOT NULL CHECK (action IN ('proxy','direct','reject')),
    remark     TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
    updated_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
    UNIQUE (kind, value)
);

CREATE TABLE bypass_destinations (
    id         TEXT PRIMARY KEY,
    cidr       TEXT NOT NULL UNIQUE,
    remark     TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now'))
);

CREATE TABLE dns_config (
    id               INTEGER PRIMARY KEY CHECK (id = 1),
    local_upstreams  TEXT    NOT NULL DEFAULT '["119.29.29.29","223.5.5.5"]',
    remote_upstreams TEXT    NOT NULL DEFAULT '["1.1.1.1","8.8.8.8"]',
    cache_size       INTEGER NOT NULL DEFAULT 10240,
    lazy_cache       INTEGER NOT NULL DEFAULT 1,
    lazy_cache_ttl   INTEGER NOT NULL DEFAULT 86400,
    log_level        TEXT    NOT NULL DEFAULT 'info' CHECK (log_level IN ('debug','info','warn','error')),
    updated_at       TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now'))
);
INSERT INTO dns_config (id) VALUES (1);

CREATE TABLE network_config (
    id                     INTEGER PRIMARY KEY CHECK (id = 1),
    mode                   TEXT    NOT NULL DEFAULT 'A' CHECK (mode IN ('A','B','C')),
    management_iface       TEXT    NOT NULL DEFAULT '',
    service_iface          TEXT    NOT NULL DEFAULT '',
    fakeip_pool            TEXT    NOT NULL DEFAULT '198.18.0.0/16',
    tproxy_port            INTEGER NOT NULL DEFAULT 12345,
    lan_default_policy     TEXT    NOT NULL DEFAULT 'proxy' CHECK (lan_default_policy IN ('proxy','direct')),
    ospf_push_batch        INTEGER NOT NULL DEFAULT 500,
    ospf_push_interval_s   INTEGER NOT NULL DEFAULT 2,
    ospf_resolve_workers   INTEGER NOT NULL DEFAULT 8,
    ospf_publish_allowlist TEXT    NOT NULL DEFAULT '[]',
    node_failover_mode     TEXT    NOT NULL DEFAULT 'normal' CHECK (node_failover_mode IN ('normal','strict')),
    updated_at             TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now'))
);
INSERT INTO network_config (id) VALUES (1);

CREATE TABLE remote_hosts (
    id                 TEXT PRIMARY KEY,
    name               TEXT    NOT NULL,
    ssh_host           TEXT    NOT NULL,
    ssh_port           INTEGER NOT NULL DEFAULT 22 CHECK (ssh_port BETWEEN 1 AND 65535),
    ssh_user           TEXT    NOT NULL,
    ssh_auth_kind      TEXT    NOT NULL CHECK (ssh_auth_kind IN ('password','key')),
    ssh_credential_enc BLOB,
    ssh_host_key       TEXT    NOT NULL DEFAULT '',
    region             TEXT    NOT NULL DEFAULT '',
    status             TEXT    NOT NULL DEFAULT 'unknown',
    remark             TEXT    NOT NULL DEFAULT '',
    created_at         TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
    updated_at         TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now'))
);

CREATE TABLE remote_deploys (
    id          TEXT PRIMARY KEY,
    host_id     TEXT NOT NULL REFERENCES remote_hosts (id) ON DELETE CASCADE,
    kind        TEXT NOT NULL CHECK (kind IN ('wg','vless')),
    params_enc  BLOB,
    state       TEXT NOT NULL CHECK (state IN ('queued','running','succeeded','failed','rolled_back')),
    node_id     TEXT REFERENCES nodes (id) ON DELETE SET NULL,
    started_at  TEXT,
    finished_at TEXT,
    error       TEXT NOT NULL DEFAULT '',
    created_at  TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now'))
);
CREATE INDEX remote_deploys_host ON remote_deploys (host_id, created_at);

CREATE TABLE remote_deploy_logs (
    id        INTEGER PRIMARY KEY AUTOINCREMENT,
    deploy_id TEXT NOT NULL REFERENCES remote_deploys (id) ON DELETE CASCADE,
    ts        TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
    stream    TEXT NOT NULL CHECK (stream IN ('stdout','stderr','system')),
    line      TEXT NOT NULL
);
CREATE INDEX remote_deploy_logs_deploy ON remote_deploy_logs (deploy_id, id);

CREATE TABLE routes_published (
    cidr       TEXT PRIMARY KEY,
    source     TEXT NOT NULL CHECK (source IN ('rule','geo','domain','fakeip')),
    rule_id    TEXT,
    first_seen TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
    last_seen  TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
    state      TEXT NOT NULL CHECK (state IN ('pending','published','failed','protected')),
    detail     TEXT NOT NULL DEFAULT ''
);
CREATE INDEX routes_published_state ON routes_published (state);

CREATE TABLE resolve_cache (
    domain         TEXT    NOT NULL,
    resolver_group TEXT    NOT NULL,
    ips            TEXT    NOT NULL,
    ttl            INTEGER NOT NULL,
    resolved_at    INTEGER NOT NULL,
    expires_at     INTEGER NOT NULL,
    fail_count     INTEGER NOT NULL DEFAULT 0,
    last_error     TEXT    NOT NULL DEFAULT '',
    PRIMARY KEY (domain, resolver_group)
);
CREATE INDEX resolve_cache_expires ON resolve_cache (expires_at);

CREATE TABLE geo_cache (
    kind        TEXT NOT NULL CHECK (kind IN ('geosite','geoip')),
    tag         TEXT NOT NULL,
    geodata_ver TEXT NOT NULL,
    payload     TEXT NOT NULL,
    updated_at  TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
    PRIMARY KEY (kind, tag, geodata_ver)
);

CREATE TABLE events (
    id       INTEGER PRIMARY KEY AUTOINCREMENT,
    ts       TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
    level    TEXT NOT NULL CHECK (level IN ('debug','info','warn','error')),
    module   TEXT NOT NULL,
    type     TEXT NOT NULL,
    message  TEXT NOT NULL,
    trace_id TEXT NOT NULL DEFAULT '',
    actor    TEXT NOT NULL DEFAULT '',
    details  TEXT NOT NULL DEFAULT '{}'
);
CREATE INDEX events_ts ON events (ts);
CREATE INDEX events_module_level ON events (module, level);

CREATE TABLE apply_log (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    generation  INTEGER NOT NULL,
    component   TEXT    NOT NULL,
    action      TEXT    NOT NULL,
    ok          INTEGER NOT NULL,
    duration_ms INTEGER NOT NULL DEFAULT 0,
    error       TEXT    NOT NULL DEFAULT '',
    ts          TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now'))
);
CREATE INDEX apply_log_generation ON apply_log (generation);

CREATE TABLE changesets (
    id          TEXT PRIMARY KEY,
    generation  INTEGER NOT NULL UNIQUE,
    actor       TEXT    NOT NULL DEFAULT '',
    summary     TEXT    NOT NULL,
    diff        TEXT    NOT NULL DEFAULT '{}',
    reverted_by TEXT,
    created_at  TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now'))
);

CREATE TABLE traffic_samples (
    ts         INTEGER PRIMARY KEY,
    up_bytes   INTEGER NOT NULL,
    down_bytes INTEGER NOT NULL
);

CREATE TABLE node_traffic_samples (
    ts         INTEGER NOT NULL,
    node_id    TEXT    NOT NULL,
    up_bytes   INTEGER NOT NULL,
    down_bytes INTEGER NOT NULL,
    PRIMARY KEY (ts, node_id)
);
