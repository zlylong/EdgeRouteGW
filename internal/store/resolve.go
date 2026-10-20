package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"time"
)

// ResolvedEntry is one cached DNS answer.
type ResolvedEntry struct {
	Domain     string    `json:"domain"`
	Group      string    `json:"group"` // resolver group: "local" or "remote"
	IPs        []string  `json:"ips"`
	TTL        int       `json:"ttl"`
	ResolvedAt time.Time `json:"resolved_at"`
	ExpiresAt  time.Time `json:"expires_at"`
	FailCount  int       `json:"fail_count"`
	LastError  string    `json:"last_error,omitempty"`
}

// Expired reports whether the entry is past its TTL.
func (e ResolvedEntry) Expired(now time.Time) bool { return !e.ExpiresAt.After(now) }

// ListResolveCache returns every cached answer.
func (s *Store) ListResolveCache(ctx context.Context) ([]ResolvedEntry, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT domain, resolver_group, ips, ttl, resolved_at, expires_at, fail_count, last_error FROM resolve_cache`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ResolvedEntry{}
	for rows.Next() {
		var e ResolvedEntry
		var ips string
		var resolved, expires int64
		if err := rows.Scan(&e.Domain, &e.Group, &ips, &e.TTL, &resolved, &expires, &e.FailCount, &e.LastError); err != nil {
			return nil, err
		}
		_ = json.Unmarshal([]byte(ips), &e.IPs)
		if e.IPs == nil {
			e.IPs = []string{}
		}
		e.ResolvedAt, e.ExpiresAt = time.Unix(resolved, 0).UTC(), time.Unix(expires, 0).UTC()
		out = append(out, e)
	}
	return out, rows.Err()
}

// UpsertResolveCache stores an answer (or a failure, keeping the old IPs).
func (s *Store) UpsertResolveCache(ctx context.Context, e ResolvedEntry) error {
	ips, _ := json.Marshal(nonNil(e.IPs))
	_, err := s.db.ExecContext(ctx, `INSERT INTO resolve_cache (domain, resolver_group, ips, ttl, resolved_at, expires_at, fail_count, last_error)
		VALUES (?,?,?,?,?,?,?,?)
		ON CONFLICT(domain, resolver_group) DO UPDATE SET ips=excluded.ips, ttl=excluded.ttl, resolved_at=excluded.resolved_at,
		expires_at=excluded.expires_at, fail_count=excluded.fail_count, last_error=excluded.last_error`,
		e.Domain, e.Group, string(ips), e.TTL, e.ResolvedAt.Unix(), e.ExpiresAt.Unix(), e.FailCount, e.LastError)
	return err
}

// PruneResolveCache drops entries for domains not in keep.
func (s *Store) PruneResolveCache(ctx context.Context, keep map[string]bool) (int64, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT domain FROM resolve_cache`)
	if err != nil {
		return 0, err
	}
	var drop []string
	for rows.Next() {
		var d string
		if rows.Scan(&d) == nil && !keep[d] {
			drop = append(drop, d)
		}
	}
	rows.Close()
	var n int64
	for _, d := range drop {
		res, err := s.db.ExecContext(ctx, `DELETE FROM resolve_cache WHERE domain=?`, d)
		if err != nil {
			return n, err
		}
		k, _ := res.RowsAffected()
		n += k
	}
	return n, nil
}

// ClearResolveCache empties the cache (DNS upstream change, operator reset).
func (s *Store) ClearResolveCache(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM resolve_cache`)
	return err
}

// PublishedRoute is one row of routes_published: what the route engine
// decided for a prefix and what the kernel currently holds.
type PublishedRoute struct {
	CIDR      string    `json:"cidr"`
	Source    string    `json:"source"`
	RuleID    string    `json:"rule_id,omitempty"`
	State     string    `json:"state"`
	Detail    string    `json:"detail,omitempty"`
	FirstSeen time.Time `json:"first_seen"`
	LastSeen  time.Time `json:"last_seen"`
}

// ReplacePublishedRoutes rewrites the table to match the given set in one
// transaction, preserving first_seen for prefixes that were already there.
func (s *Store) ReplacePublishedRoutes(ctx context.Context, routes []PublishedRoute) error {
	return s.WithTx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `CREATE TEMP TABLE IF NOT EXISTS _keep (cidr TEXT PRIMARY KEY)`); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM _keep`); err != nil {
			return err
		}
		for _, r := range routes {
			var rule any
			if r.RuleID != "" {
				rule = r.RuleID
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO routes_published (cidr, source, rule_id, state, detail)
				VALUES (?,?,?,?,?)
				ON CONFLICT(cidr) DO UPDATE SET source=excluded.source, rule_id=excluded.rule_id, state=excluded.state,
				detail=excluded.detail, last_seen=strftime('%Y-%m-%dT%H:%M:%fZ','now')`,
				r.CIDR, r.Source, rule, r.State, r.Detail); err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO _keep (cidr) VALUES (?)`, r.CIDR); err != nil {
				return err
			}
		}
		_, err := tx.ExecContext(ctx, `DELETE FROM routes_published WHERE cidr NOT IN (SELECT cidr FROM _keep)`)
		return err
	})
}

// RouteFilter narrows ListPublishedRoutes.
type RouteFilter struct {
	State string
	Query string
	Limit int
}

// ListPublishedRoutes returns rows newest-first.
func (s *Store) ListPublishedRoutes(ctx context.Context, f RouteFilter) ([]PublishedRoute, error) {
	if f.Limit <= 0 || f.Limit > 5000 {
		f.Limit = 500
	}
	q := `SELECT cidr, source, COALESCE(rule_id,''), state, detail, first_seen, last_seen FROM routes_published WHERE 1=1`
	args := []any{}
	if f.State != "" {
		q += ` AND state = ?`
		args = append(args, f.State)
	}
	if f.Query != "" {
		q += ` AND (cidr LIKE ? OR detail LIKE ?)`
		args = append(args, "%"+f.Query+"%", "%"+f.Query+"%")
	}
	q += ` ORDER BY last_seen DESC, cidr LIMIT ?`
	args = append(args, f.Limit)
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []PublishedRoute{}
	for rows.Next() {
		var r PublishedRoute
		var first, last string
		if err := rows.Scan(&r.CIDR, &r.Source, &r.RuleID, &r.State, &r.Detail, &first, &last); err != nil {
			return nil, err
		}
		r.FirstSeen, r.LastSeen = parseTime(first), parseTime(last)
		out = append(out, r)
	}
	return out, rows.Err()
}

// RouteCounts returns the number of rows per state.
func (s *Store) RouteCounts(ctx context.Context) (map[string]int, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT state, COUNT(*) FROM routes_published GROUP BY state`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var st string
		var n int
		if err := rows.Scan(&st, &n); err != nil {
			return nil, err
		}
		out[st] = n
	}
	return out, rows.Err()
}

// ClearPublishedRoutes empties the table (operator reset; the next
// reconcile pass republishes from scratch).
func (s *Store) ClearPublishedRoutes(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM routes_published`)
	return err
}

// ListRemoteHostAddrs returns the SSH hosts of managed remote hosts; they are
// protected endpoints the route engine must never publish.
func (s *Store) ListRemoteHostAddrs(ctx context.Context) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT ssh_host FROM remote_hosts`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var h string
		if rows.Scan(&h) == nil && h != "" {
			out = append(out, h)
		}
	}
	return out, rows.Err()
}
