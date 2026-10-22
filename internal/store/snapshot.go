package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
)

// DesiredSnapshot is the raw desired state captured after a mutation so a
// generation can be restored later. Credentials are kept in their
// encrypted form (this never leaves the database).
type DesiredSnapshot struct {
	Nodes          []snapNode     `json:"nodes"`
	Groups         []snapGroup    `json:"groups"`
	RuleSets       []snapRuleSet  `json:"rule_sets"`
	Rules          []snapRule     `json:"rules"`
	DevicePolicies []snapDevice   `json:"device_policies"`
	Bypass         []snapBypass   `json:"bypass"`
	Network        map[string]any `json:"network"`
	DNS            map[string]any `json:"dns"`
}

type snapNode struct {
	ID, Name, Protocol, Address, Params string
	Port                                int
	CredentialEnc                       []byte
	Enabled, IsDefault                  bool
	LatencyMs                           *int
}
type snapGroup struct {
	ID, Name, Kind, Strategy string
	Members                  []string
}
type snapRuleSet struct {
	ID, Name string
	Position int
	Enabled  bool
}
type snapRule struct {
	ID, SetID, MatchKind, MatchValue, ActionKind, ActionRef, Remark string
	Position                                                        int
	Enabled                                                         bool
}
type snapDevice struct{ ID, Kind, Value, Action, Remark string }
type snapBypass struct{ ID, CIDR, Remark string }

// captureDesired reads every desired-state table inside tx.
func captureDesired(ctx context.Context, tx *sql.Tx) (*DesiredSnapshot, error) {
	s := &DesiredSnapshot{}
	rows, err := tx.QueryContext(ctx, `SELECT id, name, protocol, address, port, credential_enc, params, enabled, is_default, latency_ms FROM nodes ORDER BY created_at, id`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var n snapNode
		var enabled, def int
		var lat sql.NullInt64
		if err := rows.Scan(&n.ID, &n.Name, &n.Protocol, &n.Address, &n.Port, &n.CredentialEnc, &n.Params, &enabled, &def, &lat); err != nil {
			rows.Close()
			return nil, err
		}
		n.Enabled, n.IsDefault = enabled != 0, def != 0
		if lat.Valid {
			v := int(lat.Int64)
			n.LatencyMs = &v
		}
		s.Nodes = append(s.Nodes, n)
	}
	rows.Close()
	rows, err = tx.QueryContext(ctx, `SELECT g.id, g.name, g.kind, g.strategy, COALESCE((SELECT json_group_array(node_id) FROM (SELECT node_id FROM outbound_group_members m WHERE m.group_id=g.id ORDER BY position)), '[]') FROM outbound_groups g ORDER BY created_at, id`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var g snapGroup
		var members string
		if err := rows.Scan(&g.ID, &g.Name, &g.Kind, &g.Strategy, &members); err != nil {
			rows.Close()
			return nil, err
		}
		_ = json.Unmarshal([]byte(members), &g.Members)
		s.Groups = append(s.Groups, g)
	}
	rows.Close()
	rows, err = tx.QueryContext(ctx, `SELECT id, name, position, enabled FROM rule_sets ORDER BY position, created_at`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var r snapRuleSet
		var enabled int
		if err := rows.Scan(&r.ID, &r.Name, &r.Position, &enabled); err != nil {
			rows.Close()
			return nil, err
		}
		r.Enabled = enabled != 0
		s.RuleSets = append(s.RuleSets, r)
	}
	rows.Close()
	rows, err = tx.QueryContext(ctx, `SELECT id, set_id, position, match_kind, match_value, action_kind, COALESCE(action_ref,''), enabled, remark FROM rules ORDER BY set_id, position`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var r snapRule
		var enabled int
		if err := rows.Scan(&r.ID, &r.SetID, &r.Position, &r.MatchKind, &r.MatchValue, &r.ActionKind, &r.ActionRef, &enabled, &r.Remark); err != nil {
			rows.Close()
			return nil, err
		}
		r.Enabled = enabled != 0
		s.Rules = append(s.Rules, r)
	}
	rows.Close()
	rows, err = tx.QueryContext(ctx, `SELECT id, kind, value, action, remark FROM device_policies ORDER BY created_at, id`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var d snapDevice
		if err := rows.Scan(&d.ID, &d.Kind, &d.Value, &d.Action, &d.Remark); err != nil {
			rows.Close()
			return nil, err
		}
		s.DevicePolicies = append(s.DevicePolicies, d)
	}
	rows.Close()
	rows, err = tx.QueryContext(ctx, `SELECT id, cidr, remark FROM bypass_destinations ORDER BY created_at, id`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var b snapBypass
		if err := rows.Scan(&b.ID, &b.CIDR, &b.Remark); err != nil {
			rows.Close()
			return nil, err
		}
		s.Bypass = append(s.Bypass, b)
	}
	rows.Close()
	if s.Network, err = rowAsMap(ctx, tx, `SELECT mode, management_iface, service_iface, fakeip_pool, tproxy_port, lan_default_policy, ospf_push_batch, ospf_push_interval_s, ospf_resolve_workers, ospf_publish_allowlist, node_failover_mode, sniff_override FROM network_config WHERE id=1`); err != nil {
		return nil, err
	}
	if s.DNS, err = rowAsMap(ctx, tx, `SELECT local_upstreams, remote_upstreams, cache_size, lazy_cache, lazy_cache_ttl, log_level, hijack, block_dot, tap FROM dns_config WHERE id=1`); err != nil {
		return nil, err
	}
	return s, nil
}

func rowAsMap(ctx context.Context, tx *sql.Tx, q string) (map[string]any, error) {
	rows, err := tx.QueryContext(ctx, q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	cols, _ := rows.Columns()
	if !rows.Next() {
		return nil, fmt.Errorf("no row")
	}
	vals := make([]any, len(cols))
	ptrs := make([]any, len(cols))
	for i := range vals {
		ptrs[i] = &vals[i]
	}
	if err := rows.Scan(ptrs...); err != nil {
		return nil, err
	}
	m := map[string]any{}
	for i, c := range cols {
		m[c] = vals[i]
	}
	return m, nil
}

// restoreDesired replaces every desired-state table with the snapshot.
func restoreDesired(ctx context.Context, tx *sql.Tx, s *DesiredSnapshot) error {
	for _, t := range []string{"rules", "rule_sets", "outbound_group_members", "outbound_groups", "nodes", "device_policies", "bypass_destinations"} {
		if _, err := tx.ExecContext(ctx, "DELETE FROM "+t); err != nil {
			return err
		}
	}
	for _, n := range s.Nodes {
		var lat sql.NullInt64
		if n.LatencyMs != nil {
			lat = sql.NullInt64{Int64: int64(*n.LatencyMs), Valid: true}
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO nodes (id, name, protocol, address, port, credential_enc, params, enabled, is_default, latency_ms) VALUES (?,?,?,?,?,?,?,?,?,?)`,
			n.ID, n.Name, n.Protocol, n.Address, n.Port, n.CredentialEnc, n.Params, b2i(n.Enabled), b2i(n.IsDefault), lat); err != nil {
			return fmt.Errorf("node %s: %w", n.Name, err)
		}
	}
	for _, g := range s.Groups {
		if _, err := tx.ExecContext(ctx, `INSERT INTO outbound_groups (id, name, kind, strategy) VALUES (?,?,?,?)`, g.ID, g.Name, g.Kind, g.Strategy); err != nil {
			return err
		}
		for i, m := range g.Members {
			if _, err := tx.ExecContext(ctx, `INSERT INTO outbound_group_members (group_id, node_id, position) VALUES (?,?,?)`, g.ID, m, i); err != nil {
				return err
			}
		}
	}
	for _, r := range s.RuleSets {
		if _, err := tx.ExecContext(ctx, `INSERT INTO rule_sets (id, name, position, enabled) VALUES (?,?,?,?)`, r.ID, r.Name, r.Position, b2i(r.Enabled)); err != nil {
			return err
		}
	}
	for _, r := range s.Rules {
		var ref any
		if r.ActionRef != "" {
			ref = r.ActionRef
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO rules (id, set_id, position, match_kind, match_value, action_kind, action_ref, enabled, remark) VALUES (?,?,?,?,?,?,?,?,?)`,
			r.ID, r.SetID, r.Position, r.MatchKind, r.MatchValue, r.ActionKind, ref, b2i(r.Enabled), r.Remark); err != nil {
			return err
		}
	}
	for _, d := range s.DevicePolicies {
		if _, err := tx.ExecContext(ctx, `INSERT INTO device_policies (id, kind, value, action, remark) VALUES (?,?,?,?,?)`, d.ID, d.Kind, d.Value, d.Action, d.Remark); err != nil {
			return err
		}
	}
	for _, b := range s.Bypass {
		if _, err := tx.ExecContext(ctx, `INSERT INTO bypass_destinations (id, cidr, remark) VALUES (?,?,?)`, b.ID, b.CIDR, b.Remark); err != nil {
			return err
		}
	}
	n := s.Network
	if _, err := tx.ExecContext(ctx, `UPDATE network_config SET mode=?, management_iface=?, service_iface=?, fakeip_pool=?, tproxy_port=?, lan_default_policy=?, ospf_push_batch=?, ospf_push_interval_s=?, ospf_resolve_workers=?, ospf_publish_allowlist=?, node_failover_mode=?, updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE id=1`,
		n["mode"], n["management_iface"], n["service_iface"], n["fakeip_pool"], n["tproxy_port"], n["lan_default_policy"], n["ospf_push_batch"], n["ospf_push_interval_s"], n["ospf_resolve_workers"], n["ospf_publish_allowlist"], n["node_failover_mode"]); err != nil {
		return err
	}
	d := s.DNS
	hijack, blockDoT := d["hijack"], d["block_dot"]
	if hijack == nil { // snapshots taken before 0004
		hijack = 1
	}
	if blockDoT == nil {
		blockDoT = 1
	}
	_, err := tx.ExecContext(ctx, `UPDATE dns_config SET local_upstreams=?, remote_upstreams=?, cache_size=?, lazy_cache=?, lazy_cache_ttl=?, log_level=?, hijack=?, block_dot=?, updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE id=1`,
		d["local_upstreams"], d["remote_upstreams"], d["cache_size"], d["lazy_cache"], d["lazy_cache_ttl"], d["log_level"], hijack, blockDoT)
	return err
}

// RevertTo restores the desired state recorded after changeset id as a new
// generation. It returns the new generation and the generation restored.
func (s *Store) RevertTo(ctx context.Context, changesetID, actor string) (newGen, restored int64, err error) {
	var diff string
	if err := s.db.QueryRowContext(ctx, `SELECT generation, diff FROM changesets WHERE id=?`, changesetID).Scan(&restored, &diff); err != nil {
		return 0, 0, err
	}
	var snap DesiredSnapshot
	if err := json.Unmarshal([]byte(diff), &snap); err != nil || snap.Network == nil {
		return 0, restored, fmt.Errorf("changeset %d has no restorable snapshot", restored)
	}
	newGen, err = s.Mutate(ctx, Mutation{Actor: actor, Summary: fmt.Sprintf("revert to generation %d", restored)}, func(tx *sql.Tx) error {
		return restoreDesired(ctx, tx, &snap)
	})
	if err == nil {
		_, _ = s.db.ExecContext(ctx, `UPDATE changesets SET reverted_by=? WHERE generation > ? AND generation < ?`, changesetID, restored, newGen)
	}
	return newGen, restored, err
}

// SnapshotRetention is how many recent changesets keep a restorable
// snapshot; older ones keep their summary only.
const SnapshotRetention = 200

func pruneSnapshots(ctx context.Context, tx *sql.Tx) {
	_, _ = tx.ExecContext(ctx, `UPDATE changesets SET diff='{}' WHERE diff <> '{}' AND generation <= (SELECT MAX(generation) FROM changesets) - ?`, SnapshotRetention)
}
