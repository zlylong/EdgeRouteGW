-- migrate:foreign_keys=off
-- Hysteria 2 joins the node protocols. SQLite cannot alter a CHECK, so the
-- table is rebuilt; foreign keys are off for this file (see migrate.go) so
-- dropping the old table does not cascade into outbound_group_members or
-- null out remote_deploys.node_id. Column order matches 0001.
CREATE TABLE nodes_new (
    id             TEXT PRIMARY KEY,
    name           TEXT    NOT NULL,
    protocol       TEXT    NOT NULL CHECK (protocol IN ('vmess','vless','trojan','shadowsocks','socks','http','wireguard','custom','hysteria')),
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
INSERT INTO nodes_new (id, name, protocol, address, port, credential_enc, params, enabled, is_default, latency_ms, created_at, updated_at)
    SELECT id, name, protocol, address, port, credential_enc, params, enabled, is_default, latency_ms, created_at, updated_at FROM nodes;
DROP TABLE nodes;
ALTER TABLE nodes_new RENAME TO nodes;
CREATE UNIQUE INDEX nodes_single_default ON nodes (is_default) WHERE is_default = 1;
