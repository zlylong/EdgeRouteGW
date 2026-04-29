package main

import "database/sql"

type RemoteNodesRepository struct {
	db *sql.DB
}

func NewRemoteNodesRepository() *RemoteNodesRepository {
	return &RemoteNodesRepository{db: db}
}

func (r *RemoteNodesRepository) ListRemoteNodes() ([]map[string]interface{}, error) {
	rows, err := r.db.Query("SELECT id, name, type, ssh_host, region, status, remark, created_at FROM remote_nodes ORDER BY id DESC")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var nodes []map[string]interface{}
	for rows.Next() {
		var id int
		var name, ntype, host, region, status, remark, createdAt string
		if err := rows.Scan(&id, &name, &ntype, &host, &region, &status, &remark, &createdAt); err != nil {
			continue
		}
		nodes = append(nodes, map[string]interface{}{
			"id": id, "name": name, "type": ntype, "ssh_host": host,
			"region": region, "status": status, "remark": remark, "created_at": createdAt,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if nodes == nil {
		nodes = []map[string]interface{}{}
	}
	return nodes, nil
}

func (r *RemoteNodesRepository) FetchNodeReq(id string) (RemoteNodeReq, error) {
	var req RemoteNodeReq
	err := r.db.QueryRow("SELECT name, type, ssh_host, ssh_port, ssh_user, ssh_auth_type, ssh_credential, ssh_host_key, region, remark FROM remote_nodes WHERE id = ?", id).
		Scan(&req.Name, &req.Type, &req.SSHHost, &req.SSHPort, &req.SSHUser, &req.SSHAuthType, &req.SSHCredential, &req.SSHHostKey, &req.Region, &req.Remark)
	req.SSHCredential = DecryptAES(req.SSHCredential)
	return req, err
}

func (r *RemoteNodesRepository) ListRemoteNodeHistory(id string) ([]map[string]interface{}, error) {
	rows, err := r.db.Query("SELECT id, params, created_at FROM remote_node_history WHERE node_id = ? ORDER BY id DESC", id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var history []map[string]interface{}
	for rows.Next() {
		var hid int
		var pjson, cat string
		if err := rows.Scan(&hid, &pjson, &cat); err == nil {
			history = append(history, map[string]interface{}{"id": hid, "params": pjson, "created_at": cat})
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if history == nil {
		history = []map[string]interface{}{}
	}
	return history, nil
}

func (r *RemoteNodesRepository) GetHistoryParams(historyID int, nodeID string) (string, error) {
	var pjson string
	err := r.db.QueryRow("SELECT params FROM remote_node_history WHERE id = ? AND node_id = ?", historyID, nodeID).Scan(&pjson)
	return pjson, err
}

func (r *RemoteNodesRepository) InsertNodeLog(nodeID int64, action, status, logText string) {
	_, _ = r.db.Exec("INSERT INTO remote_node_logs (node_id, action, status, log_text) VALUES (?, ?, ?, ?)", nodeID, action, status, logText)
}

func (r *RemoteNodesRepository) SetRemoteNodeStatus(id interface{}, status string) {
	_, _ = r.db.Exec("UPDATE remote_nodes SET status = ? WHERE id = ?", status, id)
}

func (r *RemoteNodesRepository) InsertRemoteNodeHistory(nodeID string, nodeType, paramsJSON string) {
	_, _ = r.db.Exec("INSERT INTO remote_node_history (node_id, type, params) VALUES (?, ?, ?)", nodeID, nodeType, paramsJSON)
}

func (r *RemoteNodesRepository) InsertRemoteNodeDeploying(req RemoteNodeReq) (int64, error) {
	res, err := r.db.Exec("INSERT INTO remote_nodes (name, type, ssh_host, ssh_port, ssh_user, ssh_auth_type, ssh_credential, ssh_host_key, region, status, remark) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, 'Deploying', ?)",
		req.Name, req.Type, req.SSHHost, req.SSHPort, req.SSHUser, req.SSHAuthType, EncryptAES(req.SSHCredential), req.SSHHostKey, req.Region, req.Remark)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func (r *RemoteNodesRepository) DeleteRemoteNodeCascade(id string) error {
	_, _ = r.db.Exec("DELETE FROM remote_node_wg WHERE node_id = ?", id)
	_, _ = r.db.Exec("DELETE FROM remote_node_vless WHERE node_id = ?", id)
	_, _ = r.db.Exec("DELETE FROM remote_node_logs WHERE node_id = ?", id)
	_, _ = r.db.Exec("DELETE FROM remote_node_history WHERE node_id = ?", id)
	_, err := r.db.Exec("DELETE FROM remote_nodes WHERE id = ?", id)
	return err
}
