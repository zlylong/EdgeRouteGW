package store

import (
	"context"
	"database/sql"

	"github.com/zlylong/EdgeRouteGW/internal/model"
)

// ListDevicePolicies returns per-device overrides in creation order.
func (s *Store) ListDevicePolicies(ctx context.Context) ([]model.DevicePolicy, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, kind, value, action, remark, created_at, updated_at FROM device_policies ORDER BY created_at, rowid`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.DevicePolicy{}
	for rows.Next() {
		var d model.DevicePolicy
		var kind, action, created, updated string
		if err := rows.Scan(&d.ID, &kind, &d.Value, &action, &d.Remark, &created, &updated); err != nil {
			return nil, err
		}
		d.Kind, d.Action = model.DevicePolicyKind(kind), model.DeviceAction(action)
		d.CreatedAt, d.UpdatedAt = parseTime(created), parseTime(updated)
		out = append(out, d)
	}
	return out, rows.Err()
}

// InsertDevicePolicy adds an override.
func InsertDevicePolicy(ctx context.Context, tx *sql.Tx, d model.DevicePolicy) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO device_policies (id, kind, value, action, remark) VALUES (?,?,?,?,?)`,
		d.ID, string(d.Kind), d.Value, string(d.Action), d.Remark)
	return err
}

// UpdateDevicePolicy edits an override.
func UpdateDevicePolicy(ctx context.Context, tx *sql.Tx, d model.DevicePolicy) error {
	return affectedOne(tx.ExecContext(ctx, `UPDATE device_policies SET kind=?, value=?, action=?, remark=?, updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE id=?`,
		string(d.Kind), d.Value, string(d.Action), d.Remark, d.ID))
}

// DeleteDevicePolicy removes an override.
func DeleteDevicePolicy(ctx context.Context, tx *sql.Tx, id string) error {
	return affectedOne(tx.ExecContext(ctx, `DELETE FROM device_policies WHERE id=?`, id))
}

// ListBypassDestinations returns Mode A bypass CIDRs.
func (s *Store) ListBypassDestinations(ctx context.Context) ([]model.BypassDestination, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, cidr, remark, created_at FROM bypass_destinations ORDER BY created_at, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.BypassDestination{}
	for rows.Next() {
		var b model.BypassDestination
		var created string
		if err := rows.Scan(&b.ID, &b.CIDR, &b.Remark, &created); err != nil {
			return nil, err
		}
		b.CreatedAt = parseTime(created)
		out = append(out, b)
	}
	return out, rows.Err()
}

// InsertBypassDestination adds a bypass CIDR.
func InsertBypassDestination(ctx context.Context, tx *sql.Tx, b model.BypassDestination) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO bypass_destinations (id, cidr, remark) VALUES (?,?,?)`, b.ID, b.CIDR, b.Remark)
	return err
}

// UpdateBypassRemark changes the remark of a bypass CIDR. Remarks never
// reach a rendered artifact, so this is not a desired-state change.
func (s *Store) UpdateBypassRemark(ctx context.Context, id, remark string) error {
	return affectedOne(s.db.ExecContext(ctx, `UPDATE bypass_destinations SET remark=? WHERE id=?`, remark, id))
}

// DeleteBypassDestination removes a bypass CIDR.
func DeleteBypassDestination(ctx context.Context, tx *sql.Tx, id string) error {
	return affectedOne(tx.ExecContext(ctx, `DELETE FROM bypass_destinations WHERE id=?`, id))
}
