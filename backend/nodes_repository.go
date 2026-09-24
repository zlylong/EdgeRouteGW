package main

import "errors"

import "database/sql"

type NodesRepository struct{}

func NewNodesRepository() *NodesRepository { return &NodesRepository{} }

func (r *NodesRepository) GetDefaultNodeID() (string, error) {
	var value string
	err := getDB().QueryRow("SELECT value FROM settings WHERE key='default_node_id'").Scan(&value)
	return value, err
}

func (r *NodesRepository) GetNodeFailoverMode() (string, error) {
	var value string
	err := getDB().QueryRow("SELECT value FROM settings WHERE key='node_failover_mode'").Scan(&value)
	if err != nil {
		if err == sql.ErrNoRows {
			return "normal", nil
		}
		return "", err
	}
	return normalizeNodeFailoverMode(value), nil
}

func (r *NodesRepository) ListNodes() (*sql.Rows, error) {
	return getDB().Query("SELECT id, name, grp, type, address, port, uuid, active, ping, COALESCE(params, '{}') FROM nodes")
}

func (r *NodesRepository) SetDefaultNodeID(id string) error {
	_, err := getDB().Exec("INSERT OR REPLACE INTO settings (key, value) VALUES ('default_node_id', ?)", id)
	return err
}

func (r *NodesRepository) SetNodeFailoverMode(mode string) error {
	_, err := getDB().Exec("INSERT OR REPLACE INTO settings (key, value) VALUES ('node_failover_mode', ?)", normalizeNodeFailoverMode(mode))
	return err
}

func normalizeNodeFailoverMode(mode string) string {
	if mode == "strict" {
		return "strict"
	}
	return "normal"
}

func (r *NodesRepository) InsertNode(name, groupName, nodeType, address string, port int, uuid, params string) error {
	_, err := getDB().Exec("INSERT INTO nodes (name, grp, type, address, port, uuid, params, active) VALUES (?, ?, ?, ?, ?, ?, ?, 1)", name, groupName, nodeType, address, port, uuid, params)
	return err
}

func (r *NodesRepository) ListPingTargets() (*sql.Rows, error) {
	return getDB().Query("SELECT id, type, address, port FROM nodes")
}

func (r *NodesRepository) UpdateNodePing(id int, ping int) error {
	_, err := getDB().Exec("UPDATE nodes SET ping=? WHERE id=?", ping, id)
	return err
}

// errNotFound is returned by repository writes that matched no row so the
// controller can answer 404 instead of a misleading 200.
var errNotFound = errors.New("not found")

func execExpectingRow(query string, args ...interface{}) error {
	res, err := getDB().Exec(query, args...)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return errNotFound
	}
	return nil
}

func (r *NodesRepository) NodeExists(id string) (bool, error) {
	var n int
	if err := getDB().QueryRow("SELECT COUNT(*) FROM nodes WHERE id=?", id).Scan(&n); err != nil {
		return false, err
	}
	return n > 0, nil
}

func (r *NodesRepository) UpdateNodeByID(id, name, groupName, nodeType, address string, port int, uuid, params string) error {
	return execExpectingRow("UPDATE nodes SET name=?, grp=?, type=?, address=?, port=?, uuid=?, params=? WHERE id=?", name, groupName, nodeType, address, port, uuid, params, id)
}

func (r *NodesRepository) DeleteNodeByID(id string) error {
	return execExpectingRow("DELETE FROM nodes WHERE id=?", id)
}

func (r *NodesRepository) ToggleNodeByID(id string) error {
	return execExpectingRow("UPDATE nodes SET active = NOT active WHERE id=?", id)
}
