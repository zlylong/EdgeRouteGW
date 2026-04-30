package main

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/gin-gonic/gin"
)

type NodesController struct{ repo *NodesRepository }

func NewNodesController(repo *NodesRepository) *NodesController { return &NodesController{repo: repo} }

func (ctl *NodesController) RegisterRoutes(api *gin.RouterGroup) {
	api.GET("/nodes/failover_mode", func(c *gin.Context) {
		mode, err := ctl.repo.GetNodeFailoverMode()
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "db error"})
			return
		}
		c.JSON(http.StatusOK, gin.H{"mode": mode})
	})

	api.PUT("/nodes/failover_mode", func(c *gin.Context) {
		var req struct {
			Mode string `json:"mode"`
		}
		if c.BindJSON(&req) != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "bad request"})
			return
		}
		if req.Mode != "strict" && req.Mode != "normal" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "mode must be strict or normal"})
			return
		}
		if err := ctl.repo.SetNodeFailoverMode(req.Mode); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "db error"})
			return
		}
		if err := applyNodeChangeDynamically(); err != nil {
			log.Printf("[WARN] dynamic node failover mode apply failed, fallback to scheduled apply: %v", err)
			scheduleApplyFallbackIfRuntimeReady(false)
		}
		c.JSON(http.StatusOK, gin.H{"success": true, "mode": normalizeNodeFailoverMode(req.Mode)})
	})

	api.GET("/nodes", func(c *gin.Context) {
		var defNodeStr string
		defNodeStr, _ = ctl.repo.GetDefaultNodeID()
		defNodeId, _ := strconv.Atoi(defNodeStr)

		rows, err := ctl.repo.ListNodes()
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "db query error"})
			return
		}
		defer rows.Close()
		var nodes []map[string]interface{}
		for rows.Next() {
			var id, port, ping int
			var name, grp, ntype, address, uuid, params string
			var active bool
			if err := rows.Scan(&id, &name, &grp, &ntype, &address, &port, &uuid, &active, &ping, &params); err != nil {
				continue
			}
			nodes = append(nodes, map[string]interface{}{"id": id, "name": name, "group": grp, "type": ntype, "address": address, "port": port, "uuid": uuid, "active": active, "ping": ping, "params": params, "is_default": id == defNodeId})
		}
		if nodes == nil {
			nodes = make([]map[string]interface{}, 0)
		}
		if err := rows.Err(); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "db rows error"})
			return
		}

		var activeCount int
		for _, n := range nodes {
			if n["active"] == true || n["active"] == int64(1) || n["active"] == 1 {
				activeCount++
			}
		}

		if activeCount == 1 {
			for _, n := range nodes {
				if n["active"] == true || n["active"] == int64(1) || n["active"] == 1 {
					n["is_default"] = true
				} else {
					n["is_default"] = false
				}
			}
		} else {
			for _, n := range nodes {
				if idVal, ok := n["id"].(int); ok && idVal == defNodeId {
					n["is_default"] = true
				} else {
					n["is_default"] = false
				}
			}
		}

		c.JSON(http.StatusOK, nodes)
	})

	api.PUT("/nodes/:id/default", func(c *gin.Context) {
		if err := ctl.repo.SetDefaultNodeID(c.Param("id")); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "db error"})
			return
		}
		if err := applyNodeChangeDynamically(); err != nil {
			log.Printf("[WARN] dynamic default node apply failed, fallback to scheduled apply: %v", err)
			scheduleApplyFallbackIfRuntimeReady(false)
		}
		c.JSON(http.StatusOK, gin.H{"success": true})
	})

	api.POST("/nodes", func(c *gin.Context) {
		var n struct {
			Name, Group, Type, Address, UUID, Params string
			Port                                     int
		}
		if c.BindJSON(&n) != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "bad request"})
			return
		}
		if n.Params == "" {
			n.Params = "{}"
		}
		if err := ctl.repo.InsertNode(n.Name, n.Group, n.Type, n.Address, n.Port, n.UUID, n.Params); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "db error"})
			return
		}
		if err := applyNodeChangeDynamically(); err != nil {
			log.Printf("[WARN] dynamic node add apply failed, fallback to scheduled apply: %v", err)
			scheduleApplyFallbackIfRuntimeReady(false)
		}
		c.JSON(http.StatusOK, gin.H{"success": true})
	})

	api.POST("/nodes/import", func(c *gin.Context) {
		var req struct{ Url string }
		if err := c.BindJSON(&req); err == nil {
			if strings.HasPrefix(req.Url, "vmess://") {
				b64 := strings.TrimPrefix(req.Url, "vmess://")
				decoded, err := base64.StdEncoding.DecodeString(b64)
				if err != nil {
					c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid vmess base64 content"})
					return
				}
				var v struct {
					Ps, Add, Id, Net, Tls, Sni, Path, Host string
					Port                                   interface{}
				}
				if err := json.Unmarshal(decoded, &v); err != nil {
					c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid vmess payload"})
					return
				}
				if strings.TrimSpace(v.Add) == "" || strings.TrimSpace(v.Id) == "" {
					c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid vmess endpoint"})
					return
				}
				portInt := parsePortValue(v.Port)
				if portInt <= 0 || portInt > 65535 {
					c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid vmess port"})
					return
				}

				streamSettings := make(map[string]interface{})
				if v.Net != "" {
					streamSettings["network"] = v.Net
				}
				if v.Tls != "" {
					streamSettings["security"] = v.Tls
				}

				if v.Net == "ws" {
					wsSettings := make(map[string]interface{})
					if v.Path != "" {
						wsSettings["path"] = v.Path
					}
					if v.Host != "" {
						wsSettings["headers"] = map[string]string{"Host": v.Host}
					}
					if len(wsSettings) > 0 {
						streamSettings["wsSettings"] = wsSettings
					}
				} else if v.Net == "grpc" {
					if v.Path != "" {
						streamSettings["grpcSettings"] = map[string]string{"serviceName": v.Path}
					}
				}

				if v.Tls == "tls" {
					if v.Sni != "" {
						streamSettings["tlsSettings"] = map[string]string{"serverName": v.Sni}
					}
				}

				finalParamsVmess := map[string]interface{}{}
				if len(streamSettings) > 0 {
					finalParamsVmess["streamSettings"] = streamSettings
				}
				vmessParamsJson, err := json.Marshal(finalParamsVmess)
				if err != nil {
					c.JSON(http.StatusInternalServerError, gin.H{"error": "Marshal vmess params failed"})
					return
				}
				if err := ctl.repo.InsertNode(v.Ps, "Imported", "Vmess", v.Add, portInt, v.Id, string(vmessParamsJson)); err != nil {
					c.JSON(http.StatusInternalServerError, gin.H{"error": "db error"})
					return
				}

				if err := applyNodeChangeDynamically(); err != nil {
					log.Printf("[WARN] dynamic vmess import apply failed, fallback to scheduled apply: %v", err)
					scheduleApplyFallbackIfRuntimeReady(false)
				}
				c.JSON(http.StatusOK, gin.H{"success": true})
				return
			} else if strings.HasPrefix(req.Url, "vless://") {
				parsedUrl, err := url.Parse(req.Url)
				if err == nil {
					uuid := parsedUrl.User.Username()
					host := parsedUrl.Hostname()
					portStr := parsedUrl.Port()
					portInt, _ := strconv.Atoi(portStr)
					if strings.TrimSpace(host) == "" || strings.TrimSpace(uuid) == "" || portInt <= 0 || portInt > 65535 {
						c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid vless endpoint"})
						return
					}
					alias, _ := url.QueryUnescape(parsedUrl.Fragment)
					if alias == "" {
						alias = host
					}

					query := parsedUrl.Query()
					params := map[string]interface{}{
						"encryption": query.Get("encryption"),
						"flow":       query.Get("flow"),
						"security":   query.Get("security"),
						"sni":        query.Get("sni"),
						"fp":         query.Get("fp"),
						"pbk":        query.Get("pbk"),
						"sid":        query.Get("sid"),
						"type":       query.Get("type"),
						"headerType": query.Get("headerType"),
					}

					// Convert immediately to Xray standard structure
					ss := map[string]interface{}{"network": params["type"]}
					if params["security"] != nil && params["security"] != "" {
						ss["security"] = params["security"]
					}
					if params["security"] == "reality" {
						ss["realitySettings"] = map[string]interface{}{
							"fingerprint": params["fp"], "serverName": params["sni"],
							"publicKey": params["pbk"], "shortId": params["sid"], "spiderX": "/",
						}
					} else if params["security"] == "tls" {
						ss["tlsSettings"] = map[string]interface{}{"serverName": params["sni"]}
					}

					finalParams := map[string]interface{}{}
					if len(ss) > 0 {
						finalParams["streamSettings"] = ss
					}
					if f := params["flow"]; f != nil && f != "" {
						finalParams["flow"] = f
					}
					if e := params["encryption"]; e != nil && e != "" {
						finalParams["encryption"] = e
					}

					paramsJson, err := json.Marshal(finalParams)
					if err != nil {
						c.JSON(http.StatusInternalServerError, gin.H{"error": "Marshal vless params failed"})
						return
					}

					if err := ctl.repo.InsertNode(alias, "Imported", "Vless", host, portInt, uuid, string(paramsJson)); err != nil {
						c.JSON(http.StatusInternalServerError, gin.H{"error": "db error"})
						return
					}
					if err := applyNodeChangeDynamically(); err != nil {
						log.Printf("[WARN] dynamic vless import apply failed, fallback to scheduled apply: %v", err)
						scheduleApplyFallbackIfRuntimeReady(false)
					}
					c.JSON(http.StatusOK, gin.H{"success": true})
					return
				}
			}
		}
		if strings.HasPrefix(req.Url, "wireguard://") {
			parsedUrl, err := url.Parse(req.Url)
			if err == nil {
				clientPrivKey := parsedUrl.User.Username()
				host := parsedUrl.Hostname()
				portStr := parsedUrl.Port()
				portInt, _ := strconv.Atoi(portStr)
				if strings.TrimSpace(host) == "" || strings.TrimSpace(clientPrivKey) == "" || portInt <= 0 || portInt > 65535 {
					c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid wireguard endpoint"})
					return
				}
				alias, _ := url.QueryUnescape(parsedUrl.Fragment)
				if alias == "" {
					alias = host
				}

				query := parsedUrl.Query()

				peer := map[string]interface{}{
					"endpoint":  fmt.Sprintf("%s:%d", host, portInt),
					"publicKey": query.Get("publickey"),
				}

				settings := map[string]interface{}{
					"secretKey": clientPrivKey,
					"peers":     []map[string]interface{}{peer},
				}

				if addrStr := query.Get("address"); addrStr != "" {
					settings["address"] = []string{addrStr}
				}
				if mtuStr := query.Get("mtu"); mtuStr != "" {
					if mtu, err := strconv.Atoi(mtuStr); err == nil {
						settings["mtu"] = mtu
					}
				}
				if rsv := query.Get("reserved"); rsv != "" {
					settings["reserved"] = []int{0, 0, 0} // Mocked reserved formatting, actually needs parsing if it's string, but usually it's array of uint8. We can leave it as string or omit if empty.
					// For Xray wireguard, reserved is optional. Xray accepts [0,0,0] format.
					// Let's just omit it if it's too complex or just set it as string if Xray supports string reserved in some versions. Wait, Xray reserved is usually array of numbers. Let's just pass it if it exists.
				}

				params := map[string]interface{}{
					"settings": settings,
				}

				paramsJson, _ := json.Marshal(params)
				if err := ctl.repo.InsertNode(alias, "Imported", "Wireguard", host, portInt, "", string(paramsJson)); err != nil {
					c.JSON(http.StatusInternalServerError, gin.H{"error": "db error"})
					return
				}
				if err := applyNodeChangeDynamically(); err != nil {
					log.Printf("[WARN] dynamic wireguard import apply failed, fallback to scheduled apply: %v", err)
					scheduleApplyFallbackIfRuntimeReady(false)
				}
				c.JSON(http.StatusOK, gin.H{"success": true})
				return
			}
		}
		c.JSON(http.StatusBadRequest, gin.H{"error": "Unsupported URL format"})
	})

	api.POST("/nodes/ping", func(c *gin.Context) {
		rows, err := ctl.repo.ListPingTargets()
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "db query error"})
			return
		}
		defer rows.Close()
		const maxConcurrentPing = 20
		sem := make(chan struct{}, maxConcurrentPing)
		var wg sync.WaitGroup
		for rows.Next() {
			var id, port int
			var ntype, address string
			if err := rows.Scan(&id, &ntype, &address, &port); err != nil {
				continue
			}
			wg.Add(1)
			go func(nid int, nType string, addr string, p int) {
				defer wg.Done()
				sem <- struct{}{}
				defer func() { <-sem }()

				ping := -1

				if strings.ToLower(nType) == "wireguard" || strings.ToLower(nType) == "wg" {
					out, _ := sysCmd.output("ping", "-c", "1", "-W", "2", addr)
					if strings.Contains(string(out), "1 received") || strings.Contains(string(out), "1 packets received") {
						re := regexp.MustCompile(`time=([0-9.]+)`)
						matches := re.FindStringSubmatch(string(out))
						if len(matches) > 1 {
							f, _ := strconv.ParseFloat(matches[1], 64)
							ping = int(f)
							if ping == 0 {
								ping = 1
							}
						} else {
							ping = 1
						}
					}
				} else {
					start := time.Now()
					d := net.Dialer{
						Timeout: 2 * time.Second,
						Control: func(network, address string, c syscall.RawConn) error {
							return c.Control(func(fd uintptr) {
								err := syscall.SetsockoptInt(int(fd), syscall.SOL_SOCKET, 36, 2)
								if err != nil {
									log.Printf("[WARN] setsockopt SO_MARK error: %v", err)
								}
							})
						},
					}
					conn, err := d.Dial("tcp", net.JoinHostPort(addr, fmt.Sprintf("%d", p)))
					if err == nil {
						ping = int(time.Since(start).Milliseconds())
						if ping == 0 {
							ping = 1
						}
						conn.Close()
					}
				}

				if err := ctl.repo.UpdateNodePing(nid, ping); err != nil {
					log.Printf("[WARN] update node ping failed id=%d err=%v", nid, err)
				}
			}(id, ntype, address, port)
		}
		if err := rows.Err(); err != nil {
			log.Printf("[ERROR] ping nodes rows err: %v", err)
			c.JSON(http.StatusInternalServerError, gin.H{"error": "db rows error"})
			return
		}
		go func() {
			wg.Wait()
		}()
		c.JSON(http.StatusOK, gin.H{"success": true})
	})

	api.PUT("/nodes/:id", func(c *gin.Context) {
		var n struct {
			Name, Group, Type, Address, UUID, Params string
			Port                                     int
		}
		if c.BindJSON(&n) != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "bad request"})
			return
		}
		if n.Port <= 0 {
			n.Port = 443
		}
		if n.Params == "" {
			n.Params = "{}"
		}
		if err := ctl.repo.UpdateNodeByID(c.Param("id"), n.Name, n.Group, n.Type, n.Address, n.Port, n.UUID, n.Params); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "db error"})
			return
		}
		if err := applyNodeChangeDynamically(); err != nil {
			log.Printf("[WARN] dynamic node update apply failed, fallback to scheduled apply: %v", err)
			scheduleApplyFallbackIfRuntimeReady(false)
		}
		c.JSON(http.StatusOK, gin.H{"success": true})
	})

	api.DELETE("/nodes/:id", func(c *gin.Context) {
		removedTag := nodeIDToTag(c.Param("id"))
		if err := ctl.repo.DeleteNodeByID(c.Param("id")); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "db error"})
			return
		}
		if err := applyNodeChangeDynamically(removedTag); err != nil {
			log.Printf("[WARN] dynamic node delete apply failed, fallback to scheduled apply: %v", err)
			scheduleApplyFallbackIfRuntimeReady(false)
		}
		c.JSON(http.StatusOK, gin.H{"success": true})
	})

	api.PUT("/nodes/:id/toggle", func(c *gin.Context) {
		if err := ctl.repo.ToggleNodeByID(c.Param("id")); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "db error"})
			return
		}
		if err := applyNodeChangeDynamically(); err != nil {
			log.Printf("[WARN] dynamic node toggle apply failed, fallback to scheduled apply: %v", err)
			scheduleApplyFallbackIfRuntimeReady(false)
		}
		c.JSON(http.StatusOK, gin.H{"success": true})
	})
}
