package store

import (
	"context"
	"fmt"
	"os"
	"time"
)

// HistoryUsage is what the history tables and the database files take up.
type HistoryUsage struct {
	DBBytes            int64 `json:"db_bytes"`
	WALBytes           int64 `json:"wal_bytes"`
	Events             int64 `json:"events"`
	ApplyLog           int64 `json:"apply_log"`
	Changesets         int64 `json:"changesets"`
	DeployLogs         int64 `json:"deploy_logs"`
	TrafficSamples     int64 `json:"traffic_samples"`
	NodeTrafficSamples int64 `json:"node_traffic_samples"`
}

// HistoryUsage counts the rows of every table that only ever grows and
// reports the size of the database files.
func (s *Store) HistoryUsage(ctx context.Context) (HistoryUsage, error) {
	var u HistoryUsage
	for _, c := range []struct {
		table string
		dst   *int64
	}{
		{"events", &u.Events}, {"apply_log", &u.ApplyLog}, {"changesets", &u.Changesets},
		{"remote_deploy_logs", &u.DeployLogs}, {"traffic_samples", &u.TrafficSamples}, {"node_traffic_samples", &u.NodeTrafficSamples},
	} {
		if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM `+c.table).Scan(c.dst); err != nil {
			return u, fmt.Errorf("count %s: %w", c.table, err)
		}
	}
	if s.path != "" {
		if st, err := os.Stat(s.path); err == nil {
			u.DBBytes = st.Size()
		}
		if st, err := os.Stat(s.path + "-wal"); err == nil {
			u.WALBytes = st.Size()
		}
	}
	return u, nil
}

// PruneReport counts what PruneHistory removed.
type PruneReport struct {
	Events     int64 `json:"events"`
	ApplyLog   int64 `json:"apply_log"`
	Changesets int64 `json:"changesets"`
	Deploys    int64 `json:"deploys"`
	DeployLogs int64 `json:"deploy_logs"`
	Traffic    int64 `json:"traffic"`
}

// Total is the number of rows removed.
func (r PruneReport) Total() int64 {
	return r.Events + r.ApplyLog + r.Changesets + r.Deploys + r.DeployLogs + r.Traffic
}

// tsFormat is how the schema defaults write timestamps; comparing strings in
// this format orders correctly.
const tsFormat = "2006-01-02T15:04:05.000Z"

// PruneHistory deletes records older than historyKeep (events, apply log,
// revision rows past the last SnapshotRetention, failed or rolled-back
// deploys, deploy output) and traffic samples older than trafficKeep.
// Succeeded deploy rows stay: the host probe reads them to know what is
// installed where.
func (s *Store) PruneHistory(ctx context.Context, historyKeep, trafficKeep time.Duration) (PruneReport, error) {
	var rep PruneReport
	now := time.Now()
	cut := now.Add(-historyKeep).UTC().Format(tsFormat)
	for _, st := range []struct {
		q    string
		args []any
		dst  *int64
	}{
		{`DELETE FROM events WHERE ts < ?`, []any{cut}, &rep.Events},
		{`DELETE FROM apply_log WHERE ts < ?`, []any{cut}, &rep.ApplyLog},
		{`DELETE FROM changesets WHERE created_at < ? AND generation <= (SELECT COALESCE(MAX(generation),0) FROM changesets) - ?`, []any{cut, SnapshotRetention}, &rep.Changesets},
		{`DELETE FROM remote_deploy_logs WHERE ts < ?`, []any{cut}, &rep.DeployLogs},
		{`DELETE FROM remote_deploys WHERE created_at < ? AND state IN ('failed','rolled_back')`, []any{cut}, &rep.Deploys},
	} {
		res, err := s.db.ExecContext(ctx, st.q, st.args...)
		if err != nil {
			return rep, err
		}
		n, _ := res.RowsAffected()
		*st.dst += n
	}
	tcut := now.Add(-trafficKeep).Unix()
	for _, q := range []string{`DELETE FROM traffic_samples WHERE ts < ?`, `DELETE FROM node_traffic_samples WHERE ts < ?`} {
		res, err := s.db.ExecContext(ctx, q, tcut)
		if err != nil {
			return rep, err
		}
		n, _ := res.RowsAffected()
		rep.Traffic += n
	}
	return rep, nil
}

// Compact rebuilds the database file so pages freed by pruning return to
// the filesystem, then folds the WAL back in. It takes a write lock for the
// duration, which is well under a second for a database of a few MB.
func (s *Store) Compact(ctx context.Context) error {
	if _, err := s.db.ExecContext(ctx, `VACUUM`); err != nil {
		return fmt.Errorf("vacuum: %w", err)
	}
	// VACUUM writes the rebuilt pages through the WAL; fold them back and
	// truncate it so the -wal file does not keep the old size.
	var busy, logFrames, ckpt int
	if err := s.db.QueryRowContext(ctx, `PRAGMA wal_checkpoint(TRUNCATE)`).Scan(&busy, &logFrames, &ckpt); err != nil {
		return fmt.Errorf("checkpoint: %w", err)
	}
	return nil
}
