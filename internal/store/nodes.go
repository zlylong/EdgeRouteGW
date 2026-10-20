package store

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/zlylong/EdgeRouteGW/internal/model"
)

// NodeRow is a node with its encrypted credential; the store never decrypts.
type NodeRow struct {
	model.Node
	CredentialEnc []byte
}

const nodeCols = `id, name, protocol, address, port, credential_enc, params, enabled, is_default, latency_ms, created_at, updated_at`

func scanNode(sc interface{ Scan(...any) error }) (NodeRow, error) {
	var n NodeRow
	var proto, created, updated string
	var enabled, isDefault int
	var latency sql.NullInt64
	if err := sc.Scan(&n.ID, &n.Name, &proto, &n.Address, &n.Port, &n.CredentialEnc, &n.Params, &enabled, &isDefault, &latency, &created, &updated); err != nil {
		return n, err
	}
	n.Protocol = model.Protocol(proto)
	n.Enabled = enabled != 0
	n.IsDefault = isDefault != 0
	if latency.Valid {
		v := int(latency.Int64)
		n.LatencyMs = &v
	}
	n.CreatedAt = parseTime(created)
	n.UpdatedAt = parseTime(updated)
	return n, nil
}

// ListNodes returns every node ordered by creation.
func (s *Store) ListNodes(ctx context.Context) ([]NodeRow, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+nodeCols+` FROM nodes ORDER BY created_at, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []NodeRow{}
	for rows.Next() {
		n, err := scanNode(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

// GetNode returns one node or ErrNotFound.
func (s *Store) GetNode(ctx context.Context, id string) (NodeRow, error) {
	return scanNode(s.db.QueryRowContext(ctx, `SELECT `+nodeCols+` FROM nodes WHERE id = ?`, id))
}

// InsertNode adds a node inside tx.
func InsertNode(ctx context.Context, tx *sql.Tx, n NodeRow) error {
	var lat sql.NullInt64
	if n.LatencyMs != nil {
		lat = sql.NullInt64{Int64: int64(*n.LatencyMs), Valid: true}
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO nodes (id, name, protocol, address, port, credential_enc, params, enabled, is_default, latency_ms)
		VALUES (?,?,?,?,?,?,?,?,?,?)`,
		n.ID, n.Name, string(n.Protocol), n.Address, n.Port, n.CredentialEnc, n.Params, b2i(n.Enabled), b2i(n.IsDefault), lat)
	return err
}

// UpdateNode rewrites a node's editable fields. A nil CredentialEnc keeps the
// stored credential (the API never echoes it, so "unchanged" must be
// expressible); an empty non-nil slice clears it.
func UpdateNode(ctx context.Context, tx *sql.Tx, n NodeRow) error {
	if n.CredentialEnc == nil {
		return affectedOne(tx.ExecContext(ctx, `UPDATE nodes SET name=?, protocol=?, address=?, port=?, params=?, enabled=?,
			updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE id=?`,
			n.Name, string(n.Protocol), n.Address, n.Port, n.Params, b2i(n.Enabled), n.ID))
	}
	var cred []byte
	if len(n.CredentialEnc) > 0 {
		cred = n.CredentialEnc
	}
	return affectedOne(tx.ExecContext(ctx, `UPDATE nodes SET name=?, protocol=?, address=?, port=?, credential_enc=?, params=?, enabled=?,
		updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE id=?`,
		n.Name, string(n.Protocol), n.Address, n.Port, cred, n.Params, b2i(n.Enabled), n.ID))
}

// SetNodeEnabled toggles a node.
func SetNodeEnabled(ctx context.Context, tx *sql.Tx, id string, enabled bool) error {
	return affectedOne(tx.ExecContext(ctx, `UPDATE nodes SET enabled=?, updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE id=?`, b2i(enabled), id))
}

// SetDefaultNode makes id the single default node (clearing any other).
func SetDefaultNode(ctx context.Context, tx *sql.Tx, id string) error {
	var exists int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM nodes WHERE id=?`, id).Scan(&exists); err != nil {
		return err
	}
	if exists == 0 {
		return ErrNotFound
	}
	if _, err := tx.ExecContext(ctx, `UPDATE nodes SET is_default=0 WHERE is_default=1`); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, `UPDATE nodes SET is_default=1, updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE id=?`, id)
	return err
}

// DeleteNode removes a node. Group memberships cascade; rules that pointed
// at it are retargeted to the default outbound so routing never references a
// missing tag (the 1.x dangling-HA failure mode).
func DeleteNode(ctx context.Context, tx *sql.Tx, id string) (retargeted int64, err error) {
	res, err := tx.ExecContext(ctx, `UPDATE rules SET action_kind='proxy', action_ref=NULL,
		updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE action_kind='node' AND action_ref=?`, id)
	if err != nil {
		return 0, err
	}
	retargeted, _ = res.RowsAffected()
	return retargeted, affectedOne(tx.ExecContext(ctx, `DELETE FROM nodes WHERE id=?`, id))
}

// UpdateNodeLatency stores a probe result (nil = unreachable). It is not a
// desired-state change, so it does not bump the generation.
func (s *Store) UpdateNodeLatency(ctx context.Context, id string, ms *int) error {
	var v sql.NullInt64
	if ms != nil {
		v = sql.NullInt64{Int64: int64(*ms), Valid: true}
	}
	_, err := s.db.ExecContext(ctx, `UPDATE nodes SET latency_ms=? WHERE id=?`, v, id)
	return err
}

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}

// --- outbound groups ---

// ListGroups returns all groups with ordered members.
func (s *Store) ListGroups(ctx context.Context) ([]model.OutboundGroup, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, name, kind, strategy, created_at, updated_at FROM outbound_groups ORDER BY created_at, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.OutboundGroup{}
	idx := map[string]int{}
	for rows.Next() {
		var g model.OutboundGroup
		var kind, created, updated string
		if err := rows.Scan(&g.ID, &g.Name, &kind, &g.Strategy, &created, &updated); err != nil {
			return nil, err
		}
		g.Kind = model.GroupKind(kind)
		g.MemberIDs = []string{}
		g.CreatedAt, g.UpdatedAt = parseTime(created), parseTime(updated)
		idx[g.ID] = len(out)
		out = append(out, g)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	mrows, err := s.db.QueryContext(ctx, `SELECT group_id, node_id FROM outbound_group_members ORDER BY group_id, position`)
	if err != nil {
		return nil, err
	}
	defer mrows.Close()
	for mrows.Next() {
		var gid, nid string
		if err := mrows.Scan(&gid, &nid); err != nil {
			return nil, err
		}
		if i, ok := idx[gid]; ok {
			out[i].MemberIDs = append(out[i].MemberIDs, nid)
		}
	}
	return out, mrows.Err()
}

// GetGroup returns one group or ErrNotFound.
func (s *Store) GetGroup(ctx context.Context, id string) (model.OutboundGroup, error) {
	all, err := s.ListGroups(ctx)
	if err != nil {
		return model.OutboundGroup{}, err
	}
	for _, g := range all {
		if g.ID == id {
			return g, nil
		}
	}
	return model.OutboundGroup{}, ErrNotFound
}

// UpsertGroup inserts or fully replaces a group and its member list.
func UpsertGroup(ctx context.Context, tx *sql.Tx, g model.OutboundGroup) error {
	if g.Strategy == "" {
		g.Strategy = "{}"
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO outbound_groups (id, name, kind, strategy) VALUES (?,?,?,?)
		ON CONFLICT(id) DO UPDATE SET name=excluded.name, kind=excluded.kind, strategy=excluded.strategy,
		updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now')`, g.ID, g.Name, string(g.Kind), g.Strategy); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM outbound_group_members WHERE group_id=?`, g.ID); err != nil {
		return err
	}
	for i, nid := range g.MemberIDs {
		if _, err := tx.ExecContext(ctx, `INSERT INTO outbound_group_members (group_id, node_id, position) VALUES (?,?,?)`, g.ID, nid, i); err != nil {
			return fmt.Errorf("member %s: %w", nid, err)
		}
	}
	return nil
}

// DeleteGroup removes a group; rules targeting it fall back to the default
// outbound.
func DeleteGroup(ctx context.Context, tx *sql.Tx, id string) (retargeted int64, err error) {
	res, err := tx.ExecContext(ctx, `UPDATE rules SET action_kind='proxy', action_ref=NULL,
		updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE action_kind='group' AND action_ref=?`, id)
	if err != nil {
		return 0, err
	}
	retargeted, _ = res.RowsAffected()
	return retargeted, affectedOne(tx.ExecContext(ctx, `DELETE FROM outbound_groups WHERE id=?`, id))
}
