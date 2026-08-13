package main

import (
	"encoding/json"
	"fmt"
	"github.com/gin-gonic/gin"
	"log"
	"net/http"
	"net/url"
	"proxygw/remote_deploy"
	"regexp"
	"runtime/debug"
	"strings"
)

type RemoteNodeReq struct {
	Name          string `json:"name"`
	Type          string `json:"type"` // "wg" or "vless"
	SSHHost       string `json:"ssh_host"`
	SSHPort       int    `json:"ssh_port"`
	SSHUser       string `json:"ssh_user"`
	SSHHostKey    string `json:"ssh_host_key"`
	SSHAuthType   string `json:"ssh_auth_type"`
	SSHCredential string `json:"ssh_credential"`
	Region        string `json:"region"`
	Remark        string `json:"remark"`
}

type remoteSSHClient interface {
	RunCommand(cmd string) (string, string, error)
	Close() error
}

var remoteConnect = func(host string, port int, user string, authType string, credential string, expectedHostKey string) (remoteSSHClient, error) {
	return remote_deploy.Connect(host, port, user, authType, credential, expectedHostKey)
}

var startDeployRoutine = func(id int64, req RemoteNodeReq, isUpdate bool, params map[string]interface{}) {
	go doDeployRoutineWrapper(id, req, isUpdate, params)
}

func shellSingleQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'"
}

func wrapRemoteCommandWithSudo(req RemoteNodeReq, cmd string, withPassword bool) string {
	if strings.EqualFold(strings.TrimSpace(req.SSHUser), "root") || strings.TrimSpace(req.SSHUser) == "" {
		return cmd
	}
	quotedCmd := shellSingleQuote(cmd)
	if withPassword && req.SSHAuthType == "password" && strings.TrimSpace(req.SSHCredential) != "" {
		quotedPassword := shellSingleQuote(req.SSHCredential)
		return fmt.Sprintf("printf '%s\\n' %s | sudo -S -p '' bash -lc %s", "%s", quotedPassword, quotedCmd)
	}
	return fmt.Sprintf("sudo -n bash -lc %s", quotedCmd)
}

func runRemoteCommand(sshClient remoteSSHClient, req RemoteNodeReq, cmd string) (string, string, error) {
	primary := wrapRemoteCommandWithSudo(req, cmd, false)
	stdout, stderr, err := sshClient.RunCommand(primary)
	if err == nil || strings.EqualFold(strings.TrimSpace(req.SSHUser), "root") || req.SSHAuthType != "password" || strings.TrimSpace(req.SSHCredential) == "" {
		return stdout, stderr, err
	}
	joined := strings.ToLower(stdout + "\n" + stderr + "\n" + err.Error())
	if !strings.Contains(joined, "password is required") && !strings.Contains(joined, "a terminal is required") && !strings.Contains(joined, "no tty present") {
		return stdout, stderr, err
	}
	fallback := wrapRemoteCommandWithSudo(req, cmd, true)
	return sshClient.RunCommand(fallback)
}

type RemoteNodesController struct{}

func NewRemoteNodesController() *RemoteNodesController { return &RemoteNodesController{} }

func (ctl *RemoteNodesController) RegisterRoutes(authed *gin.RouterGroup) {
	NewRemoteNodesRepository().EnsureRemoteNodeHistoryTable()

	authed.GET("/remote_nodes", getRemoteNodes)
	authed.GET("/remote_nodes/:id", getRemoteNodeDetails)
	authed.POST("/remote_nodes", createAndDeployRemoteNode)
	authed.DELETE("/remote_nodes/:id", deleteRemoteNode)
	authed.POST("/remote_nodes/:id/check", checkRemoteNode)
	authed.POST("/remote_nodes/:id/hostkey", updateRemoteNodeHostKey)

	// Advanced Features
	authed.POST("/remote_nodes/batch", batchDeployRemoteNodes)
	authed.POST("/remote_nodes/:id/regenerate", regenerateRemoteNodeParams)
	authed.GET("/remote_nodes/:id/history", getRemoteNodeHistory)
	authed.POST("/remote_nodes/:id/rollback", rollbackRemoteNode)
}

func getRemoteNodes(c *gin.Context) {
	nodes, err := NewRemoteNodesRepository().ListRemoteNodes()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, nodes)
}

func getRemoteNodeDetails(c *gin.Context) {
	id := c.Param("id")
	repo := NewRemoteNodesRepository()
	node := make(map[string]interface{})

	basic, err := repo.GetRemoteNodeBasic(id)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Node not found"})
		return
	}

	node["id"] = id
	node["name"] = basic.Name
	node["type"] = basic.Type
	node["ssh_host"] = basic.Host
	node["ssh_port"] = basic.Port
	node["region"] = basic.Region
	node["status"] = basic.Status
	node["remark"] = basic.Remark

	if basic.Type == "wg" {
		wg, err := repo.GetRemoteNodeWGParams(id)
		if err == nil {
			node["wg"] = map[string]interface{}{
				"server_pub": wg.ServerPub, "client_pub": wg.ClientPub,
				"endpoint": wg.Endpoint, "port": wg.Port, "tunnel_addr": wg.TunnelAddr, "client_addr": wg.ClientAddr,
				"share_link": remote_deploy.GenerateWireGuardShareLink(wg.ClientPriv, basic.Host, wg.Port, wg.ServerPub, wg.ClientAddr, "", "EdgeRouteGW-"+basic.Host, 1420),
			}
		}
	} else if basic.Type == "vless" {
		v, err := repo.GetRemoteNodeVLESSParams(id)
		if err == nil {
			// The provisioning share link intentionally embeds the VLESS UUID (and
			// the WireGuard client private key in the WG branch above) so the link can
			// be imported directly into a client device. Because the secret is
			// deliberately carried by the share link, do not also return a standalone
			// secret field that merely mimics redaction while the value is already
			// present in the link. Only the exportable share link and public metadata
			// are surfaced, consistent with the WireGuard branch.
			node["vless"] = map[string]interface{}{
				"reality_pub": v.RealityPub, "short_id": v.ShortID,
				"server_name": v.ServerName, "dest": v.Dest, "port": v.Port, "share_link": v.ShareLink,
			}
		}
	}

	c.JSON(http.StatusOK, node)
}

func logAction(nodeId int64, action, status, logText string) {
	NewRemoteNodesRepository().InsertNodeLog(nodeId, action, status, logText)
}

var hostKeyFingerprintRe = regexp.MustCompile(`SHA256:[A-Za-z0-9+/=_-]+`)

// sshHostKeyFingerprintRe validates an explicit, user-supplied SSH host key
// fingerprint (as logged by ssh / ssh-keygen, e.g. "SHA256:abc...xyz=").
var sshHostKeyFingerprintRe = regexp.MustCompile(`^SHA256:[A-Za-z0-9+/=_-]{43,44}$`)

func isValidSSHHostKeyFingerprint(s string) bool {
	return sshHostKeyFingerprintRe.MatchString(strings.TrimSpace(s))
}

// updateRemoteNodeHostKey provides an explicit, user-confirmed recovery path
// for a legitimately rotated host key: instead of silently auto-trusting, the
// operator verifies the new fingerprint out-of-band and submits it here.
func updateRemoteNodeHostKey(c *gin.Context) {
	id := c.Param("id")
	var req struct {
		SSHHostKey string `json:"ssh_host_key"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "bad request"})
		return
	}
	fp := strings.TrimSpace(req.SSHHostKey)
	if !isValidSSHHostKeyFingerprint(fp) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid SSH host key fingerprint (expected SHA256:<base64>)"})
		return
	}
	if _, err := NewRemoteNodesRepository().FetchNodeReq(id); err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Node not found"})
		return
	}
	if err := NewRemoteNodesRepository().SetRemoteNodeHostKey(id, fp); err != nil {
		log.Printf("[ERR] SetRemoteNodeHostKey: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to update host key"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "ssh_host_key": fp})
}

func extractFingerprintFromSSHError(err error) string {
	if err == nil {
		return ""
	}
	return hostKeyFingerprintRe.FindString(err.Error())
}

func connectWithAutoHostKey(id int64, req *RemoteNodeReq) (remoteSSHClient, error) {
	sshClient, err := remoteConnect(req.SSHHost, req.SSHPort, req.SSHUser, req.SSHAuthType, req.SSHCredential, req.SSHHostKey)
	if err == nil {
		return sshClient, nil
	}

	fp := extractFingerprintFromSSHError(err)
	if fp == "" || fp == req.SSHHostKey {
		return nil, err
	}

	// Trust-on-first-use (TOFU) is only acceptable while provisioning a node that
	// has no pinned host key yet. If a host key is already stored and the server
	// now presents a different one, silently trusting it would let a MITM
	// substitute its own key and have it accepted. Treat a rotation of an
	// already-pinned key as a hard failure and require an explicit update
	// (e.g. re-create the node) instead of auto-trusting.
	if strings.TrimSpace(req.SSHHostKey) != "" {
		return nil, fmt.Errorf("refusing to auto-trust rotated SSH host key for node %d (stored %s, presented %s): update the pinned host key explicitly to proceed", id, req.SSHHostKey, fp)
	}

	// The first routine to reach this point with an empty stored key pins the
	// fingerprint. The update is conditional on the row still having an empty
	// key so a concurrent deploy cannot overwrite an already-pinned key with a
	// different fingerprint (which would reintroduce silent rotation).
	pinned, uerr := NewRemoteNodesRepository().PinInitialHostKey(id, fp)
	if uerr != nil {
		return nil, fmt.Errorf("%v; auto-update host key failed: %v", err, uerr)
	}
	if !pinned {
		return nil, fmt.Errorf("host key for node %d was pinned concurrently by another operation (stored key changed while deploying); refusing to overwrite, retry the deployment", id)
	}
	logAction(id, "deploy", "running", fmt.Sprintf("Pinned initial SSH host fingerprint to %s and retrying deployment", fp))
	req.SSHHostKey = fp

	sshClient, err = remoteConnect(req.SSHHost, req.SSHPort, req.SSHUser, req.SSHAuthType, req.SSHCredential, req.SSHHostKey)
	if err != nil {
		return nil, err
	}
	return sshClient, nil
}

var deploySemaphore = make(chan struct{}, 3)

func doDeployRoutineWrapper(id int64, req RemoteNodeReq, isUpdate bool, params map[string]interface{}) {
	deploySemaphore <- struct{}{}
	defer func() { <-deploySemaphore }()
	defer func() {
		// A panic inside a background deploy must not take down the whole gateway
		// backend; mark the node failed and surface the stack for diagnosis.
		if r := recover(); r != nil {
			NewRemoteNodesRepository().SetRemoteNodeStatus(id, "Failed")
			logAction(id, "deploy", "failed", fmt.Sprintf("deployment panicked: %v\n%s", r, debug.Stack()))
		}
	}()
	doDeployRoutine(id, req, isUpdate, params)
}

func doDeployRoutine(id int64, req RemoteNodeReq, isUpdate bool, params map[string]interface{}) {
	logAction(id, "deploy", "running", "Connecting via SSH...")

	sshClient, err := connectWithAutoHostKey(id, &req)
	if err != nil {
		NewRemoteNodesRepository().SetRemoteNodeStatus(id, "Failed")
		logAction(id, "deploy", "failed", err.Error())
		return
	}
	defer sshClient.Close()

	logAction(id, "deploy", "running", "Connected successfully, generating parameters...")

	var script string

	if req.Type == "wg" {
		var sPriv, sPub, cPriv, cPub, tunnel, clientIP string
		var port int

		if params == nil {
			port, _ = remote_deploy.GenerateUniquePort(db, 10000, 60000)
			sPriv, sPub, _ = remote_deploy.GenerateWireGuardKeys()
			cPriv, cPub, _ = remote_deploy.GenerateWireGuardKeys()
			tunnel, clientIP, _ = remote_deploy.GenerateUniqueWGTunnel(db)
		} else {
			if p, ok := params["port"].(float64); ok {
				port = int(p)
			}
			if p, ok := params["server_priv"].(string); ok {
				sPriv = p
			}
			if p, ok := params["server_pub"].(string); ok {
				sPub = p
			}
			if p, ok := params["client_priv"].(string); ok {
				cPriv = p
			}
			if p, ok := params["client_pub"].(string); ok {
				cPub = p
			}
			if p, ok := params["tunnel_addr"].(string); ok {
				tunnel = p
			}
			if p, ok := params["client_addr"].(string); ok {
				clientIP = p
			}
		}

		endpoint := fmt.Sprintf("%s:%d", req.SSHHost, port)

		if err := NewRemoteNodesRepository().UpsertRemoteNodeWGParams(id, sPriv, sPub, cPriv, cPub, endpoint, port, tunnel, clientIP); err != nil {
			NewRemoteNodesRepository().SetRemoteNodeStatus(id, "Failed")
			logAction(id, "deploy", "failed", fmt.Sprintf("Failed to persist WireGuard params: %v", err))
			return
		}

		script = remote_deploy.GenerateWGInstallScript(port, sPriv, cPub, tunnel)

	} else if req.Type == "vless" {
		var rPriv, rPub, uuid, shortId, dest, serverName, shareLink string
		var port int

		if params == nil {
			port, _ = remote_deploy.GenerateUniquePort(db, 10000, 60000)
			rPriv, rPub, _ = remote_deploy.GenerateXrayRealityKeys()
			uuid = remote_deploy.GenerateUUID()
			shortId, _ = remote_deploy.GenerateShortId()
			dest = "www.microsoft.com:443"
			serverName = "www.microsoft.com"
		} else {
			if p, ok := params["port"].(float64); ok {
				port = int(p)
			}
			if p, ok := params["reality_priv"].(string); ok {
				rPriv = p
			}
			if p, ok := params["reality_pub"].(string); ok {
				rPub = p
			}
			if p, ok := params["uuid"].(string); ok {
				uuid = p
			}
			if p, ok := params["short_id"].(string); ok {
				shortId = p
			}
			if p, ok := params["dest"].(string); ok {
				dest = p
			}
			if p, ok := params["server_name"].(string); ok {
				serverName = p
			}
		}

		shareLink = fmt.Sprintf("vless://%s@%s:%d?security=reality&sni=%s&fp=chrome&pbk=%s&sid=%s&type=tcp&flow=xtls-rprx-vision&encryption=none#%s",
			uuid, req.SSHHost, port, serverName, rPub, shortId, url.QueryEscape(req.Name))

		if err := NewRemoteNodesRepository().UpsertRemoteNodeVLESSParams(id, uuid, rPriv, rPub, shortId, serverName, dest, port, shareLink); err != nil {
			NewRemoteNodesRepository().SetRemoteNodeStatus(id, "Failed")
			logAction(id, "deploy", "failed", fmt.Sprintf("Failed to persist VLESS params: %v", err))
			return
		}

		script = remote_deploy.GenerateVlessRealityInstallScript(port, uuid, rPriv, shortId, serverName, dest)
	}

	logAction(id, "deploy", "running", "Executing installation script on remote host...")
	stdout, stderr, err := runRemoteCommand(sshClient, req, script)

	if err != nil {
		NewRemoteNodesRepository().SetRemoteNodeStatus(id, "Failed")
		failureLog := fmt.Sprintf("Deployment failed: %v", err)
		if strings.TrimSpace(stdout) != "" {
			failureLog += "\nstdout:\n" + strings.TrimSpace(stdout)
		}
		if strings.TrimSpace(stderr) != "" {
			failureLog += "\nstderr:\n" + strings.TrimSpace(stderr)
		}
		logAction(id, "deploy", "failed", failureLog)
		return
	}

	NewRemoteNodesRepository().SetRemoteNodeStatus(id, "Online")
	logAction(id, "deploy", "success", "Deployment successful. (Detailed output redacted for security)")
}

func createAndDeployRemoteNode(c *gin.Context) {
	var req RemoteNodeReq
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	nodeId, err := NewRemoteNodesRepository().InsertRemoteNodeDeploying(req)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to insert node"})
		return
	}
	startDeployRoutine(nodeId, req, false, nil)
	c.JSON(http.StatusOK, gin.H{"success": true, "message": "Deployment started", "id": nodeId})
}

func batchDeployRemoteNodes(c *gin.Context) {
	var reqs []RemoteNodeReq
	if err := c.ShouldBindJSON(&reqs); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	for _, req := range reqs {
		nodeId, err := NewRemoteNodesRepository().InsertRemoteNodeDeploying(req)
		if err == nil {
			startDeployRoutine(nodeId, req, false, nil)
		}
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "message": fmt.Sprintf("Batch deployment started for %d nodes", len(reqs))})
}

func deleteRemoteNode(c *gin.Context) {
	id := c.Param("id")

	req, err := fetchNodeReq(id)
	if err == nil {
		goSafe(func() {
			client, err := remoteConnect(req.SSHHost, req.SSHPort, req.SSHUser, req.SSHAuthType, req.SSHCredential, req.SSHHostKey)
			if err == nil {
				defer client.Close()
				if req.Type == "wg" {
					runRemoteCommand(client, req, "systemctl stop wg-quick@wg0; systemctl disable wg-quick@wg0; rm -f /etc/wireguard/wg0.conf")
				} else if req.Type == "vless" {
					runRemoteCommand(client, req, "systemctl stop xray; systemctl disable xray; rm -f /etc/systemd/system/xray.service; rm -rf /usr/local/etc/xray; rm -f /usr/local/bin/xray")
				}
			}
		})
	}

	err = NewRemoteNodesRepository().DeleteRemoteNodeCascade(id)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to delete"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true})
}

func checkRemoteNode(c *gin.Context) {
	id := c.Param("id")
	info, err := NewRemoteNodesRepository().GetRemoteNodeCheckInfo(id)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Node not found"})
		return
	}

	client, err := remoteConnect(info.Host, info.Port, info.User, info.AuthType, info.Credential, info.HostKey)
	if err != nil {
		NewRemoteNodesRepository().SetRemoteNodeStatus(id, "Offline")
		logAction(0, "check", "failed", fmt.Sprintf("Node %s SSH check failed: %v", id, err))
		c.JSON(http.StatusOK, gin.H{"success": false, "status": "Offline", "reason": err.Error()})
		return
	}
	defer client.Close()

	cmd := "systemctl is-active xray"
	if info.Type == "wg" {
		cmd = "systemctl is-active wg-quick@wg0"
	}

	checkReq := RemoteNodeReq{SSHUser: info.User, SSHAuthType: info.AuthType, SSHCredential: info.Credential}
	out, _, err := runRemoteCommand(client, checkReq, cmd)
	status := "Online"
	if err != nil || out == "" {
		status = "Offline"
	}

	NewRemoteNodesRepository().SetRemoteNodeStatus(id, status)
	c.JSON(http.StatusOK, gin.H{"success": true, "status": status})
}

func fetchNodeReq(id string) (RemoteNodeReq, error) {
	return NewRemoteNodesRepository().FetchNodeReq(id)
}

func regenerateRemoteNodeParams(c *gin.Context) {
	id := c.Param("id")
	req, err := fetchNodeReq(id)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Node not found"})
		return
	}

	// Archive old params
	var oldParams map[string]interface{} = make(map[string]interface{})
	if req.Type == "wg" {
		wg, err := NewRemoteNodesRepository().GetRegenerateWGParams(id)
		if err == nil {
			oldParams = map[string]interface{}{
				"server_priv": wg.ServerPriv,
				"share_link":  remote_deploy.GenerateWireGuardShareLink(wg.ClientPriv, req.SSHHost, wg.Port, wg.ServerPub, wg.ClientAddr, "", "EdgeRouteGW-"+req.SSHHost, 1420),
				"server_pub":  wg.ServerPub,
				"client_priv": wg.ClientPriv,
				"client_pub":  wg.ClientPub,
				"endpoint":    wg.Endpoint,
				"port":        wg.Port,
				"tunnel_addr": wg.TunnelAddr,
				"client_addr": wg.ClientAddr,
			}
		}
	} else {
		v, err := NewRemoteNodesRepository().GetRegenerateVLESSParams(id)
		if err == nil {
			oldParams = map[string]interface{}{"uuid": v.UUID, "reality_priv": v.RealityPriv, "reality_pub": v.RealityPub, "short_id": v.ShortID, "server_name": v.ServerName, "dest": v.Dest, "port": v.Port, "share_link": v.ShareLink}
		}
	}

	paramsJSON, _ := json.Marshal(oldParams)
	NewRemoteNodesRepository().InsertRemoteNodeHistory(id, req.Type, string(paramsJSON))

	NewRemoteNodesRepository().SetRemoteNodeStatus(id, "Deploying")

	var intId int64
	fmt.Sscanf(id, "%d", &intId)

	startDeployRoutine(intId, req, true, nil)
	c.JSON(http.StatusOK, gin.H{"success": true, "message": "Regeneration started"})
}

func getRemoteNodeHistory(c *gin.Context) {
	id := c.Param("id")
	history, err := NewRemoteNodesRepository().ListRemoteNodeHistory(id)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, history)
}

func rollbackRemoteNode(c *gin.Context) {
	id := c.Param("id")
	var reqBody struct {
		HistoryId int `json:"history_id"`
	}
	if err := c.ShouldBindJSON(&reqBody); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	req, err := fetchNodeReq(id)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Node not found"})
		return
	}

	pjson, err := NewRemoteNodesRepository().GetHistoryParams(reqBody.HistoryId, id)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "History record not found"})
		return
	}

	var oldParams map[string]interface{}
	json.Unmarshal([]byte(pjson), &oldParams)

	NewRemoteNodesRepository().SetRemoteNodeStatus(id, "Deploying")

	var intId int64
	fmt.Sscanf(id, "%d", &intId)

	startDeployRoutine(intId, req, true, oldParams)
	c.JSON(http.StatusOK, gin.H{"success": true, "message": "Rollback started"})
}
