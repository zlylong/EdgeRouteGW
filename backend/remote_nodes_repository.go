package main

import "database/sql"

type RemoteNodeBasic struct {
	Name   string
	Type   string
	Host   string
	Port   int
	Region string
	Status string
	Remark string
}

type RemoteNodeWGParams struct {
	ServerPriv string
	ServerPub  string
	ClientPriv string
	ClientPub  string
	Endpoint   string
	Port       int
	TunnelAddr string
	ClientAddr string
}

type RemoteNodeVLESSParams struct {
	UUID        string
	RealityPriv string
	RealityPub  string
	ShortID     string
	ServerName  string
	Dest        string
	Port        int
	ShareLink   string
}

type RemoteNodeCheckInfo struct {
	Host       string
	Port       int
	User       string
	AuthType   string
	Credential string
	HostKey    string
	Type       string
}

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

func (r *RemoteNodesRepository) GetRemoteNodeBasic(id string) (RemoteNodeBasic, error) {
	var n RemoteNodeBasic
	err := r.db.QueryRow("SELECT name, type, ssh_host, ssh_port, region, status, remark FROM remote_nodes WHERE id = ?", id).
		Scan(&n.Name, &n.Type, &n.Host, &n.Port, &n.Region, &n.Status, &n.Remark)
	return n, err
}

func (r *RemoteNodesRepository) GetRemoteNodeWGParams(id string) (RemoteNodeWGParams, error) {
	var p RemoteNodeWGParams
	err := r.db.QueryRow("SELECT server_priv, server_pub, client_priv, client_pub, endpoint, port, tunnel_addr, client_addr FROM remote_node_wg WHERE node_id = ?", id).
		Scan(&p.ServerPriv, &p.ServerPub, &p.ClientPriv, &p.ClientPub, &p.Endpoint, &p.Port, &p.TunnelAddr, &p.ClientAddr)
	return p, err
}

func (r *RemoteNodesRepository) GetRemoteNodeVLESSParams(id string) (RemoteNodeVLESSParams, error) {
	var p RemoteNodeVLESSParams
	err := r.db.QueryRow("SELECT uuid, reality_priv, reality_pub, short_id, server_name, dest, port, share_link FROM remote_node_vless WHERE node_id = ?", id).
		Scan(&p.UUID, &p.RealityPriv, &p.RealityPub, &p.ShortID, &p.ServerName, &p.Dest, &p.Port, &p.ShareLink)
	return p, err
}

func (r *RemoteNodesRepository) UpdateRemoteNodeHostKey(id int64, hostKey string) error {
	_, err := r.db.Exec("UPDATE remote_nodes SET ssh_host_key = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?", hostKey, id)
	return err
}

func (r *RemoteNodesRepository) GetRemoteNodeCheckInfo(id string) (RemoteNodeCheckInfo, error) {
	var info RemoteNodeCheckInfo
	err := r.db.QueryRow("SELECT ssh_host, ssh_port, ssh_user, ssh_auth_type, ssh_credential, ssh_host_key, type FROM remote_nodes WHERE id = ?", id).
		Scan(&info.Host, &info.Port, &info.User, &info.AuthType, &info.Credential, &info.HostKey, &info.Type)
	if err == nil {
		info.Credential = DecryptAES(info.Credential)
	}
	return info, err
}

func (r *RemoteNodesRepository) GetRegenerateWGParams(id string) (RemoteNodeWGParams, error) {
	return r.GetRemoteNodeWGParams(id)
}

func (r *RemoteNodesRepository) GetRegenerateVLESSParams(id string) (RemoteNodeVLESSParams, error) {
	return r.GetRemoteNodeVLESSParams(id)
}

func (r *RemoteNodesRepository) EnsureRemoteNodeHistoryTable() {
	_, _ = r.db.Exec("CREATE TABLE IF NOT EXISTS remote_node_history (id INTEGER PRIMARY KEY AUTOINCREMENT, node_id INTEGER, type TEXT, params TEXT, created_at DATETIME DEFAULT CURRENT_TIMESTAMP, FOREIGN KEY(node_id) REFERENCES remote_nodes(id) ON DELETE CASCADE);")
}

func (r *RemoteNodesRepository) UpsertRemoteNodeWGParams(id int64, serverPriv, serverPub, clientPriv, clientPub, endpoint string, port int, tunnelAddr, clientAddr string) error {
	_, err := r.db.Exec(`INSERT INTO remote_node_wg (node_id, server_priv, server_pub, client_priv, client_pub, endpoint, port, tunnel_addr, client_addr)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(node_id) DO UPDATE SET
			server_priv=excluded.server_priv,
			server_pub=excluded.server_pub,
			client_priv=excluded.client_priv,
			client_pub=excluded.client_pub,
			endpoint=excluded.endpoint,
			port=excluded.port,
			tunnel_addr=excluded.tunnel_addr,
			client_addr=excluded.client_addr`,
		id, serverPriv, serverPub, clientPriv, clientPub, endpoint, port, tunnelAddr, clientAddr)
	return err
}

func (r *RemoteNodesRepository) UpsertRemoteNodeVLESSParams(id int64, uuid, realityPriv, realityPub, shortID, serverName, dest string, port int, shareLink string) error {
	_, err := r.db.Exec(`INSERT INTO remote_node_vless (node_id, uuid, reality_priv, reality_pub, short_id, server_name, dest, port, share_link)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(node_id) DO UPDATE SET
			uuid=excluded.uuid,
			reality_priv=excluded.reality_priv,
			reality_pub=excluded.reality_pub,
			short_id=excluded.short_id,
			server_name=excluded.server_name,
			dest=excluded.dest,
			port=excluded.port,
			share_link=excluded.share_link`,
		id, uuid, realityPriv, realityPub, shortID, serverName, dest, port, shareLink)
	return err
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
