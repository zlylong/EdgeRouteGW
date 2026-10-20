package store

import (
	"context"
	"database/sql"
	"time"
)

// AddTrafficSample stores one minute of totals and per-outbound bytes.
func (s *Store) AddTrafficSample(ctx context.Context, ts int64, up, down int64, perTag map[string][2]int64) error {
	return s.WithTx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `INSERT INTO traffic_samples (ts, up_bytes, down_bytes) VALUES (?,?,?)
			ON CONFLICT(ts) DO UPDATE SET up_bytes=up_bytes+excluded.up_bytes, down_bytes=down_bytes+excluded.down_bytes`, ts, up, down); err != nil {
			return err
		}
		for tag, v := range perTag {
			nodeID := tag
			if len(tag) > 5 && tag[:5] == "node-" {
				nodeID = tag[5:]
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO node_traffic_samples (ts, node_id, up_bytes, down_bytes) VALUES (?,?,?,?)
				ON CONFLICT(ts, node_id) DO UPDATE SET up_bytes=up_bytes+excluded.up_bytes, down_bytes=down_bytes+excluded.down_bytes`, ts, nodeID, v[0], v[1]); err != nil {
				return err
			}
		}
		return nil
	})
}

// TrafficPoint is one bucket of a series.
type TrafficPoint struct {
	TS   int64 `json:"ts"`
	Up   int64 `json:"up"`
	Down int64 `json:"down"`
}

// TrafficSeries returns totals bucketed by bucketSec since from.
func (s *Store) TrafficSeries(ctx context.Context, from time.Time, bucketSec int64) ([]TrafficPoint, error) {
	if bucketSec <= 0 {
		bucketSec = 60
	}
	rows, err := s.db.QueryContext(ctx, `SELECT (ts / ?) * ?, SUM(up_bytes), SUM(down_bytes) FROM traffic_samples WHERE ts >= ? GROUP BY 1 ORDER BY 1`, bucketSec, bucketSec, from.Unix())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []TrafficPoint{}
	for rows.Next() {
		var p TrafficPoint
		if err := rows.Scan(&p.TS, &p.Up, &p.Down); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// NodeTraffic is a per-node total.
type NodeTraffic struct {
	NodeID string `json:"node_id"`
	Up     int64  `json:"up"`
	Down   int64  `json:"down"`
}

// TrafficByNode returns per-node totals since from, largest first.
func (s *Store) TrafficByNode(ctx context.Context, from time.Time) ([]NodeTraffic, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT node_id, SUM(up_bytes), SUM(down_bytes) FROM node_traffic_samples WHERE ts >= ? GROUP BY node_id ORDER BY SUM(up_bytes)+SUM(down_bytes) DESC`, from.Unix())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []NodeTraffic{}
	for rows.Next() {
		var n NodeTraffic
		if err := rows.Scan(&n.NodeID, &n.Up, &n.Down); err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

// TrafficTotals returns the sum since from.
func (s *Store) TrafficTotals(ctx context.Context, from time.Time) (up, down int64, err error) {
	err = s.db.QueryRowContext(ctx, `SELECT COALESCE(SUM(up_bytes),0), COALESCE(SUM(down_bytes),0) FROM traffic_samples WHERE ts >= ?`, from.Unix()).Scan(&up, &down)
	return
}

// PruneTraffic drops samples older than keep.
func (s *Store) PruneTraffic(ctx context.Context, keep time.Duration) error {
	cut := time.Now().Add(-keep).Unix()
	if _, err := s.db.ExecContext(ctx, `DELETE FROM traffic_samples WHERE ts < ?`, cut); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx, `DELETE FROM node_traffic_samples WHERE ts < ?`, cut)
	return err
}
