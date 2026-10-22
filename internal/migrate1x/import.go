// Package migrate1x imports a 1.x (proxygw) SQLite database into the 2.0
// store: nodes, HA pairs as outbound groups, rules, device policies, bypass
// destinations, DNS/network settings and managed hosts with their deploy
// parameters. It runs as one desired-state mutation so the reconciler sees
// the whole 1.x configuration at once.
package migrate1x

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/google/uuid"
	_ "modernc.org/sqlite"

	"github.com/zlylong/EdgeRouteGW/internal/model"
	"github.com/zlylong/EdgeRouteGW/internal/netinfo"
	"github.com/zlylong/EdgeRouteGW/internal/rulespec"
	"github.com/zlylong/EdgeRouteGW/internal/secret"
	"github.com/zlylong/EdgeRouteGW/internal/store"
)

// Options control an import.
type Options struct {
	SourceDB string
	// OldKey is the 32-byte 1.x aes.key; without it SSH credentials of
	// managed hosts are not imported (the hosts are, with a warning).
	OldKey []byte
	DryRun bool
	// Replace allows importing into a store that already has nodes or rules.
	Replace bool
}

// Report summarises what was (or would be) imported.
type Report struct {
	Nodes          int      `json:"nodes"`
	Groups         int      `json:"groups"`
	Rules          int      `json:"rules"`
	RuleSets       int      `json:"rule_sets"`
	DevicePolicies int      `json:"device_policies"`
	Bypass         int      `json:"bypass"`
	Hosts          int      `json:"hosts"`
	HostsNoCred    int      `json:"hosts_without_credential"`
	Deploys        int      `json:"deploys"`
	Mode           string   `json:"mode"`
	Warnings       []string `json:"warnings"`
	Generation     int64    `json:"generation,omitempty"`
}

func (r *Report) warn(format string, args ...any) {
	r.Warnings = append(r.Warnings, fmt.Sprintf(format, args...))
}

type v1Node struct {
	ID       int64
	Name     string
	Type     string
	Address  string
	Port     int
	UUID     string
	Active   bool
	Ping     int
	Params   string
	newID    string
	cred     string
	params   string
	protocol model.Protocol
}

// Import performs the migration.
func Import(ctx context.Context, st *store.Store, kr *secret.Keyring, opts Options) (*Report, error) {
	rep := &Report{Warnings: []string{}}
	src, err := sql.Open("sqlite", "file:"+opts.SourceDB+"?mode=ro&_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, err
	}
	defer src.Close()
	if err := src.PingContext(ctx); err != nil {
		return nil, fmt.Errorf("open 1.x database: %w", err)
	}
	if err := requireTables(ctx, src, "nodes", "rules", "settings"); err != nil {
		return nil, err
	}

	if !opts.Replace {
		has, err := st.HasUserData(ctx)
		if err != nil {
			return nil, err
		}
		if has {
			return nil, errors.New("target already has nodes or rules; use --replace to import anyway")
		}
	}

	settings := map[string]string{}
	if rows, err := src.QueryContext(ctx, `SELECT key, value FROM settings`); err == nil {
		for rows.Next() {
			var k, v string
			if rows.Scan(&k, &v) == nil {
				settings[k] = v
			}
		}
		rows.Close()
	}

	// --- nodes ---
	var nodes []*v1Node
	byID := map[int64]*v1Node{}
	rows, err := src.QueryContext(ctx, `SELECT id, name, type, address, port, COALESCE(uuid,''), active, COALESCE(ping,0), COALESCE(params,'{}') FROM nodes ORDER BY id`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		n := &v1Node{}
		if err := rows.Scan(&n.ID, &n.Name, &n.Type, &n.Address, &n.Port, &n.UUID, &n.Active, &n.Ping, &n.Params); err != nil {
			rows.Close()
			return nil, err
		}
		n.newID = uuid.NewString()
		n.protocol, n.cred, n.params = convertNode(n, rep)
		nodes = append(nodes, n)
		byID[n.ID] = n
	}
	rows.Close()
	defaultNode, _ := strconv.ParseInt(strings.TrimSpace(settings["default_node_id"]), 10, 64)

	// --- rules and HA groups ---
	type v1Rule struct {
		ID                  int64
		Type, Value, Policy string
		GroupID, GroupName  string
		Priority            int
	}
	var v1Rules []v1Rule
	rows, err = src.QueryContext(ctx, `SELECT id, type, value, policy, COALESCE(group_id,''), COALESCE(group_name,''), COALESCE(priority,0) FROM rules ORDER BY priority, id`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var r v1Rule
		if err := rows.Scan(&r.ID, &r.Type, &r.Value, &r.Policy, &r.GroupID, &r.GroupName, &r.Priority); err != nil {
			rows.Close()
			return nil, err
		}
		v1Rules = append(v1Rules, r)
	}
	rows.Close()

	groups := map[string]model.OutboundGroup{} // policy -> group
	var groupOrder []string
	var setOrder []string
	sets := map[string]model.RuleSet{}
	var newRules []model.Rule
	// Ungrouped 1.x rules land in the built-in default set, which exists
	// after migration (or is recreated below when --replace wiped it).
	builtinID, _ := st.BuiltinRuleSet(ctx)
	if builtinID == "" {
		builtinID = uuid.NewString()
	}
	for _, r := range v1Rules {
		setKey := r.GroupID
		name := r.GroupName
		if name == "" {
			name = store.BuiltinRuleSetName
		}
		if _, ok := sets[setKey]; !ok {
			id := uuid.NewString()
			if name == store.BuiltinRuleSetName && r.GroupID == "" {
				id = builtinID
			}
			sets[setKey] = model.RuleSet{ID: id, Name: name, Position: len(setOrder), Enabled: true, Builtin: id == builtinID}
			setOrder = append(setOrder, setKey)
		}
		nr := model.Rule{ID: uuid.NewString(), SetID: sets[setKey].ID, Enabled: true}
		switch strings.ToLower(r.Type) {
		case "domain":
			p, err := rulespec.ParseDomain(r.Value)
			if err != nil {
				rep.warn("rule %d skipped: %v", r.ID, err)
				continue
			}
			nr.MatchKind, nr.MatchValue = model.MatchDomain, p.Raw
		case "ip":
			v, err := rulespec.NormalizeIPOrCIDR(r.Value)
			if err != nil {
				rep.warn("rule %d skipped: %v", r.ID, err)
				continue
			}
			nr.MatchKind, nr.MatchValue = model.MatchIP, v
		case "geosite":
			t, err := rulespec.NormalizeGeoTag(r.Value)
			if err != nil {
				rep.warn("rule %d skipped: %v", r.ID, err)
				continue
			}
			nr.MatchKind, nr.MatchValue = model.MatchGeosite, t
		case "geoip", "geolocation":
			t, err := rulespec.NormalizeGeoTag(r.Value)
			if err != nil {
				rep.warn("rule %d skipped: %v", r.ID, err)
				continue
			}
			nr.MatchKind, nr.MatchValue = model.MatchGeoIP, t
		default:
			rep.warn("rule %d skipped: unknown type %q", r.ID, r.Type)
			continue
		}
		pol := strings.ToLower(strings.TrimSpace(r.Policy))
		switch {
		case pol == "direct":
			nr.ActionKind = model.ActionDirect
		case pol == "block":
			nr.ActionKind = model.ActionBlock
		case pol == "proxy" || pol == "":
			nr.ActionKind = model.ActionProxy
		case strings.HasPrefix(pol, "proxy-"):
			id, _ := strconv.ParseInt(strings.TrimPrefix(pol, "proxy-"), 10, 64)
			if n, ok := byID[id]; ok {
				nr.ActionKind, nr.ActionRef = model.ActionNode, n.newID
			} else {
				rep.warn("rule %d: node %d no longer exists; using default outbound", r.ID, id)
				nr.ActionKind = model.ActionProxy
			}
		case strings.HasPrefix(pol, "ha-"):
			parts := strings.Split(strings.TrimPrefix(pol, "ha-"), "-")
			if len(parts) != 2 {
				rep.warn("rule %d: malformed HA policy %q; using default outbound", r.ID, pol)
				nr.ActionKind = model.ActionProxy
				break
			}
			a, _ := strconv.ParseInt(parts[0], 10, 64)
			b, _ := strconv.ParseInt(parts[1], 10, 64)
			var members []string
			for _, id := range []int64{a, b} {
				if n, ok := byID[id]; ok {
					members = append(members, n.newID)
				} else {
					rep.warn("rule %d: HA member node %d no longer exists", r.ID, id)
				}
			}
			if len(members) == 0 {
				nr.ActionKind = model.ActionProxy
				break
			}
			g, ok := groups[pol]
			if !ok {
				names := []string{}
				for _, id := range []int64{a, b} {
					if n, ok := byID[id]; ok {
						names = append(names, n.Name)
					}
				}
				g = model.OutboundGroup{ID: uuid.NewString(), Name: "HA " + strings.Join(names, " → "), Kind: model.GroupFailover, MemberIDs: members, Strategy: "{}"}
				groups[pol] = g
				groupOrder = append(groupOrder, pol)
			}
			nr.ActionKind, nr.ActionRef = model.ActionGroup, g.ID
		default:
			rep.warn("rule %d: unknown policy %q; using default outbound", r.ID, pol)
			nr.ActionKind = model.ActionProxy
		}
		nr.Position = len(newRules)
		newRules = append(newRules, nr)
	}

	// --- device policies, bypass ---
	var devices []model.DevicePolicy
	if ok, _ := hasTable(ctx, src, "lan_acls"); ok {
		rows, err := src.QueryContext(ctx, `SELECT type, value, policy, COALESCE(remark,'') FROM lan_acls ORDER BY id`)
		if err == nil {
			for rows.Next() {
				var t, v, p, remark string
				if rows.Scan(&t, &v, &p, &remark) != nil {
					continue
				}
				d := model.DevicePolicy{ID: uuid.NewString(), Action: model.DeviceAction(p), Remark: remark}
				switch t {
				case "mac":
					mac, err := rulespec.NormalizeMAC(v)
					if err != nil {
						rep.warn("device policy %q skipped: %v", v, err)
						continue
					}
					d.Kind, d.Value = model.DeviceMAC, mac
				default:
					ip, err := rulespec.NormalizeIPOrCIDR(v)
					if err != nil {
						rep.warn("device policy %q skipped: %v", v, err)
						continue
					}
					d.Kind, d.Value = model.DeviceIP, ip
					if !strings.HasSuffix(ip, "/32") && !strings.HasSuffix(ip, "/128") {
						d.Kind = model.DeviceCIDR
					}
				}
				if d.Action != model.DeviceProxy && d.Action != model.DeviceDirect && d.Action != model.DeviceReject {
					rep.warn("device policy %q skipped: unknown action %q", v, p)
					continue
				}
				devices = append(devices, d)
			}
			rows.Close()
		}
	}
	var bypass []model.BypassDestination
	if ok, _ := hasTable(ctx, src, "protected_ips"); ok {
		rows, err := src.QueryContext(ctx, `SELECT value, COALESCE(remark,'') FROM protected_ips ORDER BY id`)
		if err == nil {
			for rows.Next() {
				var v, remark string
				if rows.Scan(&v, &remark) != nil {
					continue
				}
				c, err := rulespec.NormalizeIPOrCIDR(v)
				if err != nil || rulespec.IsIPv6Prefix(c) {
					rep.warn("protected ip %q skipped", v)
					continue
				}
				bypass = append(bypass, model.BypassDestination{ID: uuid.NewString(), CIDR: c, Remark: remark})
			}
			rows.Close()
		}
	}

	// --- settings -> network / dns ---
	net, err := st.NetworkConfig(ctx)
	if err != nil {
		return nil, err
	}
	if m := model.Mode(strings.ToUpper(strings.TrimSpace(settings["mode"]))); m.Valid() {
		net.Mode = m
	}
	rep.Mode = string(net.Mode)
	net.ManagementIface = strings.TrimSpace(settings["management_iface"])
	net.ServiceIface = strings.TrimSpace(settings["service_iface"])
	if net.ServiceIface == "" {
		// 1.x derived the OSPF network from the default-route interface.
		if di, err := netinfo.DefaultIface(); err == nil {
			net.ServiceIface = di
		}
	}
	if v := strings.ToLower(settings["lan_default_policy"]); v == "proxy" || v == "direct" {
		net.LANDefaultPolicy = v
	}
	if v := strings.ToLower(settings["node_failover_mode"]); v == "normal" || v == "strict" {
		net.NodeFailoverMode = v
	}
	if v, err := strconv.Atoi(settings["ospf_push_batch_limit"]); err == nil && v > 0 {
		net.OSPFPushBatch = v
	}
	if v, err := strconv.Atoi(settings["ospf_push_interval_seconds"]); err == nil && v > 0 {
		net.OSPFPushIntervalSec = v
	}
	if v, err := strconv.Atoi(settings["ospf_resolve_workers"]); err == nil && v > 0 {
		net.OSPFResolveWorkers = v
	}
	net.OSPFPublishAllow = []string{}
	for _, p := range strings.Split(settings["ospf_publish_allowlist"], ",") {
		if c, err := rulespec.NormalizeIPOrCIDR(strings.TrimSpace(p)); err == nil {
			net.OSPFPublishAllow = append(net.OSPFPublishAllow, c)
		}
	}
	dns, err := st.DNSConfig(ctx)
	if err != nil {
		return nil, err
	}
	if v := splitCSV(settings["dns_local"]); len(v) > 0 {
		dns.LocalUpstreams = v
	}
	if v := splitCSV(settings["dns_remote"]); len(v) > 0 {
		dns.RemoteUpstreams = v
	}
	dns.LazyCache = settings["dns_lazy"] != "false"
	dns.Hijack, dns.BlockDoT = true, true
	if v, err := strconv.Atoi(settings["dns_cache_size"]); err == nil && v > 0 {
		dns.CacheSize = v
	}
	if v, err := strconv.Atoi(settings["dns_lazy_ttl"]); err == nil && v > 0 {
		dns.LazyCacheTTL = v
	}
	if v := strings.ToLower(settings["dns_log_level"]); v == "debug" || v == "info" || v == "warn" || v == "error" {
		dns.LogLevel = v
	}
	geodataSchedule := "off"
	if settings["cron_enabled"] == "true" {
		if t := strings.TrimSpace(settings["cron_time"]); len(t) == 5 {
			geodataSchedule = "daily " + t
			if st := settings["cron_schedule_type"]; st != "" && st != "daily" {
				rep.warn("geodata update was %s in 1.x; 2.0 schedules daily at %s", st, t)
			}
		}
	}

	// --- managed hosts ---
	type v1Host struct {
		ID                                 int64
		Name, Type, Host, User, Auth, Cred string
		Port                               int
		HostKey, Region, Remark            string
	}
	var hosts []v1Host
	if ok, _ := hasTable(ctx, src, "remote_nodes"); ok {
		rows, err := src.QueryContext(ctx, `SELECT id, name, COALESCE(type,''), ssh_host, COALESCE(ssh_port,22), COALESCE(ssh_user,'root'), COALESCE(ssh_auth_type,'password'), COALESCE(ssh_credential,''), COALESCE(ssh_host_key,''), COALESCE(region,''), COALESCE(remark,'') FROM remote_nodes ORDER BY id`)
		if err == nil {
			for rows.Next() {
				var h v1Host
				if rows.Scan(&h.ID, &h.Name, &h.Type, &h.Host, &h.Port, &h.User, &h.Auth, &h.Cred, &h.HostKey, &h.Region, &h.Remark) == nil {
					hosts = append(hosts, h)
				}
			}
			rows.Close()
		}
	}
	type deployParams struct {
		kind   string
		params map[string]any
		port   int
	}
	hostDeploys := map[int64]deployParams{}
	if ok, _ := hasTable(ctx, src, "remote_node_wg"); ok {
		rows, err := src.QueryContext(ctx, `SELECT node_id, server_priv, server_pub, client_priv, client_pub, port, tunnel_addr, client_addr FROM remote_node_wg`)
		if err == nil {
			for rows.Next() {
				var id int64
				var sp, spub, cp, cpub, tun, caddr string
				var port int
				if rows.Scan(&id, &sp, &spub, &cp, &cpub, &port, &tun, &caddr) == nil {
					hostDeploys[id] = deployParams{kind: "wg", port: port, params: map[string]any{"port": port, "server_priv": sp, "server_pub": spub, "client_priv": cp, "client_pub": cpub, "tunnel_cidr": tun, "client_addr": caddr, "mtu": 1420}}
				}
			}
			rows.Close()
		}
	}
	if ok, _ := hasTable(ctx, src, "remote_node_vless"); ok {
		rows, err := src.QueryContext(ctx, `SELECT node_id, uuid, reality_priv, reality_pub, short_id, server_name, dest, port FROM remote_node_vless`)
		if err == nil {
			for rows.Next() {
				var id int64
				var u, priv, pub, sid, sn, dest string
				var port int
				if rows.Scan(&id, &u, &priv, &pub, &sid, &sn, &dest, &port) == nil {
					hostDeploys[id] = deployParams{kind: "vless", port: port, params: map[string]any{"port": port, "uuid": u, "reality_priv": priv, "reality_pub": pub, "short_id": sid, "server_name": sn, "dest": dest}}
				}
			}
			rows.Close()
		}
	}

	rep.Nodes, rep.Groups, rep.Rules, rep.RuleSets = len(nodes), len(groups), len(newRules), len(sets)
	rep.DevicePolicies, rep.Bypass, rep.Hosts, rep.Deploys = len(devices), len(bypass), len(hosts), len(hostDeploys)
	if opts.DryRun {
		return rep, nil
	}

	gen, err := st.Mutate(ctx, store.Mutation{Actor: "import-v1", Summary: fmt.Sprintf("import 1.x database (%d nodes, %d rules)", len(nodes), len(newRules))}, func(tx *sql.Tx) error {
		if opts.Replace {
			for _, t := range []string{"rules", "rule_sets", "outbound_group_members", "outbound_groups", "nodes", "device_policies", "bypass_destinations"} {
				if _, err := tx.ExecContext(ctx, "DELETE FROM "+t); err != nil {
					return err
				}
			}
		}
		for _, n := range nodes {
			enc, err := kr.Encrypt(n.cred)
			if err != nil {
				return err
			}
			var lat *int
			if n.Ping > 0 {
				v := n.Ping
				lat = &v
			}
			row := store.NodeRow{Node: model.Node{ID: n.newID, Name: n.Name, Protocol: n.protocol, Address: n.Address, Port: n.Port, Params: n.params, Enabled: n.Active, IsDefault: n.ID == defaultNode && n.Active, LatencyMs: lat}, CredentialEnc: enc}
			if err := store.InsertNode(ctx, tx, row); err != nil {
				return fmt.Errorf("node %s: %w", n.Name, err)
			}
		}
		for _, k := range groupOrder {
			if err := store.UpsertGroup(ctx, tx, groups[k]); err != nil {
				return err
			}
		}
		if opts.Replace {
			if err := store.InsertBuiltinRuleSet(ctx, tx, builtinID); err != nil {
				return err
			}
		}
		for _, k := range setOrder {
			s := sets[k]
			if s.Builtin {
				var n int
				if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM rule_sets WHERE id=?`, s.ID).Scan(&n); err != nil {
					return err
				}
				if n > 0 {
					continue
				}
				if err := store.InsertBuiltinRuleSet(ctx, tx, s.ID); err != nil {
					return err
				}
				continue
			}
			if err := store.InsertRuleSet(ctx, tx, s.ID, s.Name); err != nil {
				return err
			}
		}
		// Rules seeded in the built-in set must not shadow imported ones (a
		// 1.x "geosite:cn -> HA group" must beat the seeded "geosite:cn ->
		// direct"), so imported rules go first and the seeded ones follow.
		var seededIDs []string
		if rows, err := tx.QueryContext(ctx, `SELECT id FROM rules WHERE set_id=? ORDER BY position, created_at`, builtinID); err == nil {
			for rows.Next() {
				var id string
				if err := rows.Scan(&id); err == nil {
					seededIDs = append(seededIDs, id)
				}
			}
			rows.Close()
		}
		var importedBuiltin []string
		for _, r := range newRules {
			if err := store.InsertRule(ctx, tx, r); err != nil {
				return fmt.Errorf("rule %s: %w", r.MatchValue, err)
			}
			if r.SetID == builtinID {
				importedBuiltin = append(importedBuiltin, r.ID)
			}
		}
		if err := store.SeedBuiltinRules(ctx, tx, builtinID); err != nil {
			return err
		}
		if rows, err := tx.QueryContext(ctx, `SELECT id FROM rules WHERE set_id=? ORDER BY position, created_at`, builtinID); err == nil {
			var all []string
			for rows.Next() {
				var id string
				if err := rows.Scan(&id); err == nil {
					all = append(all, id)
				}
			}
			rows.Close()
			isImported := map[string]bool{}
			for _, id := range importedBuiltin {
				isImported[id] = true
			}
			order := append([]string{}, importedBuiltin...)
			for _, id := range all {
				if !isImported[id] {
					order = append(order, id)
				}
			}
			if len(order) > 0 {
				if err := store.ReorderRules(ctx, tx, builtinID, order); err != nil {
					return fmt.Errorf("order built-in rules: %w", err)
				}
			}
		}
		for _, d := range devices {
			if err := store.InsertDevicePolicy(ctx, tx, d); err != nil {
				rep.warn("device policy %s not imported: %v", d.Value, err)
			}
		}
		for _, b := range bypass {
			if err := store.InsertBypassDestination(ctx, tx, b); err != nil {
				rep.warn("bypass %s not imported: %v", b.CIDR, err)
			}
		}
		if err := store.UpdateNetworkConfig(ctx, tx, net); err != nil {
			return err
		}
		return store.UpdateDNSConfig(ctx, tx, dns)
	})
	if err != nil {
		return rep, err
	}
	rep.Generation = gen
	_ = st.SetSetting(ctx, "geodata.auto_update", geodataSchedule)

	// Hosts are not desired state (no generation); import after.
	for _, h := range hosts {
		cred := ""
		if h.Cred != "" {
			if opts.OldKey == nil {
				rep.HostsNoCred++
				rep.warn("host %s: SSH credential not imported (no --old-key); re-enter it", h.Name)
			} else if plain, ok := decryptV1(h.Cred, opts.OldKey); ok {
				cred = plain
			} else {
				rep.HostsNoCred++
				rep.warn("host %s: SSH credential could not be decrypted with the given key; re-enter it", h.Name)
			}
		}
		enc, err := kr.Encrypt(cred)
		if err != nil {
			return rep, err
		}
		rh := store.RemoteHost{ID: uuid.NewString(), Name: h.Name, SSHHost: h.Host, SSHPort: h.Port, SSHUser: h.User, SSHAuthKind: h.Auth, CredentialEnc: enc, HostKey: h.HostKey, Region: h.Region, Status: "unknown", Remark: h.Remark}
		if rh.SSHAuthKind != "password" && rh.SSHAuthKind != "key" {
			rh.SSHAuthKind = "password"
		}
		if err := st.InsertRemoteHost(ctx, rh); err != nil {
			rep.warn("host %s not imported: %v", h.Name, err)
			continue
		}
		if dp, ok := hostDeploys[h.ID]; ok {
			raw, _ := json.Marshal(dp.params)
			penc, err := kr.Encrypt(string(raw))
			if err != nil {
				return rep, err
			}
			d := store.RemoteDeploy{ID: uuid.NewString(), HostID: rh.ID, Kind: dp.kind, ParamsEnc: penc, State: "succeeded"}
			if err := st.InsertRemoteDeploy(ctx, d); err == nil {
				_ = st.SetRemoteDeployState(ctx, d.ID, "succeeded", "")
				// Link the node that 1.x created for this deployment (same
				// address and port) so re-deploys keep updating it.
				for _, n := range nodes {
					if n.Address == h.Host && n.Port == dp.port {
						_ = st.LinkDeployNode(ctx, d.ID, n.newID)
						break
					}
				}
				_, _ = st.AppendDeployLog(ctx, d.ID, "system", "imported from 1.x remote_node "+strconv.FormatInt(h.ID, 10))
			}
		}
	}
	return rep, nil
}

// convertNode maps a 1.x node row to protocol, credential and 2.0 params.
func convertNode(n *v1Node, rep *Report) (model.Protocol, string, string) {
	proto := model.Protocol(strings.ToLower(strings.TrimSpace(n.Type)))
	if proto == "ss" {
		proto = model.ProtocolShadowsocks
	}
	params := map[string]any{}
	if strings.TrimSpace(n.Params) != "" {
		if err := json.Unmarshal([]byte(n.Params), &params); err != nil {
			rep.warn("node %s: params are not valid JSON (%v); kept empty", n.Name, err)
			params = map[string]any{}
		}
	}
	cred := n.UUID
	switch proto {
	case model.ProtocolVMess, model.ProtocolVLESS, model.ProtocolTrojan, model.ProtocolShadowsocks, model.ProtocolSocks, model.ProtocolHTTP:
	case model.ProtocolWireGuard:
		// 1.x kept the client private key inside params.settings.secretKey.
		if s, ok := params["settings"].(map[string]any); ok {
			if sk, ok := s["secretKey"].(string); ok && sk != "" {
				cred = sk
				delete(s, "secretKey")
			}
		}
		if cred == "" {
			rep.warn("node %s: WireGuard private key not found; the node needs its credential re-entered", n.Name)
		}
	case model.ProtocolCustom:
	default:
		rep.warn("node %s: unknown protocol %q imported as custom", n.Name, n.Type)
		proto = model.ProtocolCustom
	}
	// 1.x stored the credential both in the uuid column and sometimes inside
	// params.settings; the renderer injects it from the credential, so strip
	// embedded copies.
	if s, ok := params["settings"].(map[string]any); ok {
		for _, key := range []string{"vnext", "servers"} {
			if list, ok := s[key].([]any); ok {
				for _, item := range list {
					if m, ok := item.(map[string]any); ok {
						delete(m, "password")
						if users, ok := m["users"].([]any); ok {
							for _, u := range users {
								if um, ok := u.(map[string]any); ok {
									delete(um, "id")
								}
							}
						}
					}
				}
			}
		}
	}
	b, _ := json.Marshal(params)
	return proto, cred, string(b)
}

func splitCSV(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if v, err := rulespec.NormalizeUpstream(strings.TrimSpace(p)); err == nil {
			out = append(out, v)
		}
	}
	return out
}

func requireTables(ctx context.Context, db *sql.DB, names ...string) error {
	for _, n := range names {
		ok, err := hasTable(ctx, db, n)
		if err != nil {
			return err
		}
		if !ok {
			return fmt.Errorf("not a 1.x database: table %s missing", n)
		}
	}
	return nil
}

func hasTable(ctx context.Context, db *sql.DB, name string) (bool, error) {
	var n int
	err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name=?`, name).Scan(&n)
	return n > 0, err
}

// decryptV1 opens a 1.x "ENC2:" AES-GCM credential with the old aes.key.
// The pre-1.7.27 "ENC:" CFB format is not supported; those installs
// re-encrypted every row on first boot after upgrading.
func decryptV1(enc string, key []byte) (string, bool) {
	payload, ok := strings.CutPrefix(enc, "ENC2:")
	if !ok {
		return "", false
	}
	data, err := base64.StdEncoding.DecodeString(payload)
	if err != nil {
		return "", false
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", false
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil || len(data) < gcm.NonceSize() {
		return "", false
	}
	plain, err := gcm.Open(nil, data[:gcm.NonceSize()], data[gcm.NonceSize():], nil)
	if err != nil {
		return "", false
	}
	return string(plain), true
}

// SortedWarnings returns warnings sorted for stable output.
func (r *Report) SortedWarnings() []string {
	out := append([]string(nil), r.Warnings...)
	sort.Strings(out)
	return out
}
