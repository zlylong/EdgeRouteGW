package store

import (
	"context"
	"database/sql"
	"time"
)

// RemoteHost is a managed server the gateway can deploy a tunnel server on.
type RemoteHost struct {
	ID            string    `json:"id"`
	Name          string    `json:"name"`
	SSHHost       string    `json:"ssh_host"`
	SSHPort       int       `json:"ssh_port"`
	SSHUser       string    `json:"ssh_user"`
	SSHAuthKind   string    `json:"ssh_auth_kind"` // password | key
	CredentialEnc []byte    `json:"-"`
	HostKey       string    `json:"host_key"` // SHA256:... fingerprint, pinned on first contact
	Region        string    `json:"region"`
	Status        string    `json:"status"` // unknown | online | offline | deploying | failed
	Remark        string    `json:"remark"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
}

const hostCols = `id, name, ssh_host, ssh_port, ssh_user, ssh_auth_kind, ssh_credential_enc, ssh_host_key, region, status, remark, created_at, updated_at`

func scanHost(sc interface{ Scan(...any) error }) (RemoteHost, error) {
	var h RemoteHost
	var created, updated string
	if err := sc.Scan(&h.ID, &h.Name, &h.SSHHost, &h.SSHPort, &h.SSHUser, &h.SSHAuthKind, &h.CredentialEnc, &h.HostKey, &h.Region, &h.Status, &h.Remark, &created, &updated); err != nil {
		return h, err
	}
	h.CreatedAt, h.UpdatedAt = parseTime(created), parseTime(updated)
	return h, nil
}

// ListRemoteHosts returns all managed hosts.
func (s *Store) ListRemoteHosts(ctx context.Context) ([]RemoteHost, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+hostCols+` FROM remote_hosts ORDER BY created_at, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []RemoteHost{}
	for rows.Next() {
		h, err := scanHost(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, h)
	}
	return out, rows.Err()
}

// GetRemoteHost returns one host or ErrNotFound.
func (s *Store) GetRemoteHost(ctx context.Context, id string) (RemoteHost, error) {
	return scanHost(s.db.QueryRowContext(ctx, `SELECT `+hostCols+` FROM remote_hosts WHERE id = ?`, id))
}

// InsertRemoteHost adds a host (not a desired-state change: no generation).
func (s *Store) InsertRemoteHost(ctx context.Context, h RemoteHost) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO remote_hosts (id, name, ssh_host, ssh_port, ssh_user, ssh_auth_kind, ssh_credential_enc, ssh_host_key, region, status, remark)
		VALUES (?,?,?,?,?,?,?,?,?,?,?)`, h.ID, h.Name, h.SSHHost, h.SSHPort, h.SSHUser, h.SSHAuthKind, h.CredentialEnc, h.HostKey, h.Region, h.Status, h.Remark)
	return err
}

// UpdateRemoteHost rewrites editable fields; nil CredentialEnc keeps the
// stored credential.
func (s *Store) UpdateRemoteHost(ctx context.Context, h RemoteHost) error {
	if h.CredentialEnc == nil {
		return affectedOne(s.db.ExecContext(ctx, `UPDATE remote_hosts SET name=?, ssh_host=?, ssh_port=?, ssh_user=?, ssh_auth_kind=?, region=?, remark=?,
			updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE id=?`, h.Name, h.SSHHost, h.SSHPort, h.SSHUser, h.SSHAuthKind, h.Region, h.Remark, h.ID))
	}
	return affectedOne(s.db.ExecContext(ctx, `UPDATE remote_hosts SET name=?, ssh_host=?, ssh_port=?, ssh_user=?, ssh_auth_kind=?, ssh_credential_enc=?, region=?, remark=?,
		updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE id=?`, h.Name, h.SSHHost, h.SSHPort, h.SSHUser, h.SSHAuthKind, h.CredentialEnc, h.Region, h.Remark, h.ID))
}

// SetRemoteHostStatus records probe/deploy outcome.
func (s *Store) SetRemoteHostStatus(ctx context.Context, id, status string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE remote_hosts SET status=?, updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE id=?`, status, id)
	return err
}

// SetRemoteHostKey pins (or re-pins) the SSH host key fingerprint.
func (s *Store) SetRemoteHostKey(ctx context.Context, id, fingerprint string) error {
	return affectedOne(s.db.ExecContext(ctx, `UPDATE remote_hosts SET ssh_host_key=?, updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE id=?`, fingerprint, id))
}

// DeleteRemoteHost removes a host and (by cascade) its deploys.
func (s *Store) DeleteRemoteHost(ctx context.Context, id string) error {
	return affectedOne(s.db.ExecContext(ctx, `DELETE FROM remote_hosts WHERE id=?`, id))
}

// RemoteDeploy is one deployment attempt and the parameters it used.
type RemoteDeploy struct {
	ID         string     `json:"id"`
	HostID     string     `json:"host_id"`
	Kind       string     `json:"kind"` // wg | vless
	ParamsEnc  []byte     `json:"-"`
	State      string     `json:"state"` // queued | running | succeeded | failed | rolled_back
	NodeID     string     `json:"node_id,omitempty"`
	StartedAt  *time.Time `json:"started_at,omitempty"`
	FinishedAt *time.Time `json:"finished_at,omitempty"`
	Error      string     `json:"error,omitempty"`
	CreatedAt  time.Time  `json:"created_at"`
}

const deployCols = `id, host_id, kind, params_enc, state, COALESCE(node_id,''), started_at, finished_at, error, created_at`

func scanDeploy(sc interface{ Scan(...any) error }) (RemoteDeploy, error) {
	var d RemoteDeploy
	var started, finished sql.NullString
	var created string
	if err := sc.Scan(&d.ID, &d.HostID, &d.Kind, &d.ParamsEnc, &d.State, &d.NodeID, &started, &finished, &d.Error, &created); err != nil {
		return d, err
	}
	if started.Valid {
		t := parseTime(started.String)
		d.StartedAt = &t
	}
	if finished.Valid {
		t := parseTime(finished.String)
		d.FinishedAt = &t
	}
	d.CreatedAt = parseTime(created)
	return d, nil
}

// InsertRemoteDeploy queues a deploy.
func (s *Store) InsertRemoteDeploy(ctx context.Context, d RemoteDeploy) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO remote_deploys (id, host_id, kind, params_enc, state) VALUES (?,?,?,?,?)`, d.ID, d.HostID, d.Kind, d.ParamsEnc, d.State)
	return err
}

// SetRemoteDeployState advances the state machine.
func (s *Store) SetRemoteDeployState(ctx context.Context, id, state, errMsg string) error {
	switch state {
	case "running":
		_, err := s.db.ExecContext(ctx, `UPDATE remote_deploys SET state=?, started_at=strftime('%Y-%m-%dT%H:%M:%fZ','now'), error='' WHERE id=?`, state, id)
		return err
	case "succeeded", "failed", "rolled_back":
		_, err := s.db.ExecContext(ctx, `UPDATE remote_deploys SET state=?, finished_at=strftime('%Y-%m-%dT%H:%M:%fZ','now'), error=? WHERE id=?`, state, errMsg, id)
		return err
	}
	_, err := s.db.ExecContext(ctx, `UPDATE remote_deploys SET state=?, error=? WHERE id=?`, state, errMsg, id)
	return err
}

// LinkDeployNode records the node created for a deploy.
func (s *Store) LinkDeployNode(ctx context.Context, deployID, nodeID string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE remote_deploys SET node_id=? WHERE id=?`, nodeID, deployID)
	return err
}

// GetRemoteDeploy returns one deploy.
func (s *Store) GetRemoteDeploy(ctx context.Context, id string) (RemoteDeploy, error) {
	return scanDeploy(s.db.QueryRowContext(ctx, `SELECT `+deployCols+` FROM remote_deploys WHERE id=?`, id))
}

// ListRemoteDeploys returns deploys newest first, optionally for one host.
func (s *Store) ListRemoteDeploys(ctx context.Context, hostID string, limit int) ([]RemoteDeploy, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	q := `SELECT ` + deployCols + ` FROM remote_deploys`
	args := []any{}
	if hostID != "" {
		q += ` WHERE host_id=?`
		args = append(args, hostID)
	}
	q += ` ORDER BY created_at DESC LIMIT ?`
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []RemoteDeploy{}
	for rows.Next() {
		d, err := scanDeploy(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// DeployLogLine is one line of deploy output.
type DeployLogLine struct {
	ID     int64     `json:"id"`
	At     time.Time `json:"at"`
	Stream string    `json:"stream"` // stdout | stderr | system
	Line   string    `json:"line"`
}

// AppendDeployLog stores a line.
func (s *Store) AppendDeployLog(ctx context.Context, deployID, stream, line string) (int64, error) {
	res, err := s.db.ExecContext(ctx, `INSERT INTO remote_deploy_logs (deploy_id, stream, line) VALUES (?,?,?)`, deployID, stream, line)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// ListDeployLogs returns lines in order, after afterID.
func (s *Store) ListDeployLogs(ctx context.Context, deployID string, afterID int64, limit int) ([]DeployLogLine, error) {
	if limit <= 0 || limit > 5000 {
		limit = 2000
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id, ts, stream, line FROM remote_deploy_logs WHERE deploy_id=? AND id>? ORDER BY id LIMIT ?`, deployID, afterID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []DeployLogLine{}
	for rows.Next() {
		var l DeployLogLine
		var ts string
		if err := rows.Scan(&l.ID, &ts, &l.Stream, &l.Line); err != nil {
			return nil, err
		}
		l.At = parseTime(ts)
		out = append(out, l)
	}
	return out, rows.Err()
}

// GetSetting / SetSetting are the remaining true key-value settings
// (schedules, UI preferences, component metadata).
func (s *Store) GetSetting(ctx context.Context, key string) (string, error) {
	var v string
	err := s.db.QueryRowContext(ctx, `SELECT value FROM settings WHERE key=?`, key).Scan(&v)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return v, err
}

// SetSetting upserts a key.
func (s *Store) SetSetting(ctx context.Context, key, value string) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO settings (key, value) VALUES (?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value, updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now')`, key, value)
	return err
}
