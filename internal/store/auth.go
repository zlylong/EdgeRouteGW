package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// AdminCredential is the single admin account.
type AdminCredential struct {
	PasswordHash string
	Bootstrap    bool // still using the generated initial password
	UpdatedAt    time.Time
}

// ErrNoAdmin is returned before the first bootstrap.
var ErrNoAdmin = errors.New("admin credential not initialised")

// Admin returns the admin credential row.
func (s *Store) Admin(ctx context.Context) (AdminCredential, error) {
	var a AdminCredential
	var boot int
	var updated string
	err := s.db.QueryRowContext(ctx, `SELECT password_hash, bootstrap, updated_at FROM auth_admin WHERE id = 1`).Scan(&a.PasswordHash, &boot, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return a, ErrNoAdmin
	}
	if err != nil {
		return a, err
	}
	a.Bootstrap = boot != 0
	a.UpdatedAt = parseTime(updated)
	return a, nil
}

// SetAdminPassword inserts or replaces the admin hash. It is not a desired-
// state change, so it does not bump the generation.
func (s *Store) SetAdminPassword(ctx context.Context, hash string, bootstrap bool) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO auth_admin (id, password_hash, bootstrap) VALUES (1, ?, ?)
		ON CONFLICT(id) DO UPDATE SET password_hash=excluded.password_hash, bootstrap=excluded.bootstrap,
		updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now')`, hash, b2i(bootstrap))
	return err
}

// Session is a browser login.
type Session struct {
	ID         string    `json:"id"`
	CreatedAt  time.Time `json:"created_at"`
	ExpiresAt  time.Time `json:"expires_at"`
	LastSeenAt time.Time `json:"last_seen_at"`
	RemoteIP   string    `json:"remote_ip"`
	UserAgent  string    `json:"user_agent"`
}

// CreateSession stores a new session.
func (s *Store) CreateSession(ctx context.Context, sess Session) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO sessions (id, expires_at, remote_ip, user_agent) VALUES (?,?,?,?)`,
		sess.ID, sess.ExpiresAt.UTC().Format(isoMillis), sess.RemoteIP, sess.UserAgent)
	return err
}

const isoMillis = "2006-01-02T15:04:05.000Z"

// GetSession returns a session or ErrNotFound (also for expired ones).
func (s *Store) GetSession(ctx context.Context, id string) (Session, error) {
	var sess Session
	var created, expires, seen string
	err := s.db.QueryRowContext(ctx, `SELECT id, created_at, expires_at, last_seen_at, remote_ip, user_agent FROM sessions WHERE id = ?`, id).
		Scan(&sess.ID, &created, &expires, &seen, &sess.RemoteIP, &sess.UserAgent)
	if err != nil {
		return sess, err
	}
	sess.CreatedAt, sess.ExpiresAt, sess.LastSeenAt = parseTime(created), parseTime(expires), parseTime(seen)
	if !sess.ExpiresAt.After(time.Now()) {
		return sess, ErrNotFound
	}
	return sess, nil
}

// TouchSession records activity and extends expiry.
func (s *Store) TouchSession(ctx context.Context, id string, expires time.Time) error {
	_, err := s.db.ExecContext(ctx, `UPDATE sessions SET last_seen_at=strftime('%Y-%m-%dT%H:%M:%fZ','now'), expires_at=? WHERE id=?`,
		expires.UTC().Format(isoMillis), id)
	return err
}

// DeleteSession removes one session.
func (s *Store) DeleteSession(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE id=?`, id)
	return err
}

// DeleteOtherSessions removes every session but keep.
func (s *Store) DeleteOtherSessions(ctx context.Context, keep string) (int64, error) {
	res, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE id <> ?`, keep)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// ListSessions returns live sessions, most recent first.
func (s *Store) ListSessions(ctx context.Context) ([]Session, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, created_at, expires_at, last_seen_at, remote_ip, user_agent FROM sessions
		WHERE expires_at > strftime('%Y-%m-%dT%H:%M:%fZ','now') ORDER BY last_seen_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Session{}
	for rows.Next() {
		var sess Session
		var created, expires, seen string
		if err := rows.Scan(&sess.ID, &created, &expires, &seen, &sess.RemoteIP, &sess.UserAgent); err != nil {
			return nil, err
		}
		sess.CreatedAt, sess.ExpiresAt, sess.LastSeenAt = parseTime(created), parseTime(expires), parseTime(seen)
		out = append(out, sess)
	}
	return out, rows.Err()
}

// PruneSessions deletes expired sessions.
func (s *Store) PruneSessions(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE expires_at <= strftime('%Y-%m-%dT%H:%M:%fZ','now')`)
	return err
}

// APIToken is a long-lived credential for automation.
type APIToken struct {
	ID         string     `json:"id"`
	Name       string     `json:"name"`
	Scope      string     `json:"scope"`
	CreatedAt  time.Time  `json:"created_at"`
	LastUsedAt *time.Time `json:"last_used_at,omitempty"`
	ExpiresAt  *time.Time `json:"expires_at,omitempty"`
}

// CreateAPIToken stores a token's hash.
func (s *Store) CreateAPIToken(ctx context.Context, t APIToken, hash string) error {
	var exp any
	if t.ExpiresAt != nil {
		exp = t.ExpiresAt.UTC().Format(isoMillis)
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO api_tokens (id, name, token_hash, scope, expires_at) VALUES (?,?,?,?,?)`, t.ID, t.Name, hash, t.Scope, exp)
	return err
}

// APITokenByHash looks a token up for authentication.
func (s *Store) APITokenByHash(ctx context.Context, hash string) (APIToken, error) {
	var t APIToken
	var created string
	var used, exp sql.NullString
	err := s.db.QueryRowContext(ctx, `SELECT id, name, scope, created_at, last_used_at, expires_at FROM api_tokens WHERE token_hash = ?`, hash).
		Scan(&t.ID, &t.Name, &t.Scope, &created, &used, &exp)
	if err != nil {
		return t, err
	}
	t.CreatedAt = parseTime(created)
	if used.Valid {
		v := parseTime(used.String)
		t.LastUsedAt = &v
	}
	if exp.Valid {
		v := parseTime(exp.String)
		t.ExpiresAt = &v
		if !v.After(time.Now()) {
			return t, ErrNotFound
		}
	}
	return t, nil
}

// TouchAPIToken records usage.
func (s *Store) TouchAPIToken(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE api_tokens SET last_used_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE id=?`, id)
	return err
}

// ListAPITokens returns tokens (never the hash).
func (s *Store) ListAPITokens(ctx context.Context) ([]APIToken, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, name, scope, created_at, last_used_at, expires_at FROM api_tokens ORDER BY created_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []APIToken{}
	for rows.Next() {
		var t APIToken
		var created string
		var used, exp sql.NullString
		if err := rows.Scan(&t.ID, &t.Name, &t.Scope, &created, &used, &exp); err != nil {
			return nil, err
		}
		t.CreatedAt = parseTime(created)
		if used.Valid {
			v := parseTime(used.String)
			t.LastUsedAt = &v
		}
		if exp.Valid {
			v := parseTime(exp.String)
			t.ExpiresAt = &v
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// DeleteAllAPITokens revokes every token (password change).
func (s *Store) DeleteAllAPITokens(ctx context.Context) (int64, error) {
	res, err := s.db.ExecContext(ctx, `DELETE FROM api_tokens`)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return n, nil
}

// DeleteAPIToken revokes a token.
func (s *Store) DeleteAPIToken(ctx context.Context, id string) error {
	return affectedOne(s.db.ExecContext(ctx, `DELETE FROM api_tokens WHERE id=?`, id))
}
