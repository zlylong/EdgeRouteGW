package migrate1x

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zlylong/EdgeRouteGW/internal/secret"
	"github.com/zlylong/EdgeRouteGW/internal/store"
)

func v1Encrypt(t *testing.T, key []byte, plain string) string {
	block, _ := aes.NewCipher(key)
	gcm, _ := cipher.NewGCM(block)
	nonce := make([]byte, gcm.NonceSize())
	_, _ = rand.Read(nonce)
	return "ENC2:" + base64.StdEncoding.EncodeToString(append(nonce, gcm.Seal(nil, nonce, []byte(plain), nil)...))
}

// fixture builds a 1.x database shaped like the production one.
func fixture(t *testing.T, key []byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "proxygw.db")
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	stmts := []string{
		`CREATE TABLE nodes (id INTEGER PRIMARY KEY AUTOINCREMENT, name TEXT, grp TEXT, type TEXT, address TEXT, port INTEGER, uuid TEXT, active BOOLEAN DEFAULT 1, ping INTEGER DEFAULT 0, params TEXT DEFAULT '{}')`,
		`CREATE TABLE rules (id INTEGER PRIMARY KEY AUTOINCREMENT, type TEXT, value TEXT, policy TEXT, priority INTEGER NOT NULL DEFAULT 0, group_id TEXT NOT NULL DEFAULT '', group_name TEXT NOT NULL DEFAULT '')`,
		`CREATE TABLE settings (key TEXT PRIMARY KEY, value TEXT)`,
		`CREATE TABLE lan_acls (id INTEGER PRIMARY KEY AUTOINCREMENT, type TEXT, value TEXT, policy TEXT, remark TEXT)`,
		`CREATE TABLE protected_ips (id INTEGER PRIMARY KEY AUTOINCREMENT, value TEXT NOT NULL UNIQUE, remark TEXT)`,
		`CREATE TABLE remote_nodes (id INTEGER PRIMARY KEY AUTOINCREMENT, name TEXT, type TEXT, ssh_host TEXT, ssh_port INTEGER, ssh_user TEXT, ssh_auth_type TEXT, ssh_credential TEXT, ssh_host_key TEXT, region TEXT, status TEXT, remark TEXT)`,
		`CREATE TABLE remote_node_wg (node_id INTEGER PRIMARY KEY, server_priv TEXT, server_pub TEXT, client_priv TEXT, client_pub TEXT, endpoint TEXT, port INTEGER, tunnel_addr TEXT, client_addr TEXT)`,
		`CREATE TABLE remote_node_vless (node_id INTEGER PRIMARY KEY, uuid TEXT, reality_priv TEXT, reality_pub TEXT, short_id TEXT, server_name TEXT, dest TEXT, port INTEGER, share_link TEXT)`,
		`INSERT INTO nodes (id,name,grp,type,address,port,uuid,active,ping,params) VALUES
		 (6,'ProxyGW-SG','','Wireguard','13.228.66.113',32891,'',1,-1,'{"settings":{"address":["10.92.61.2/32"],"mtu":1420,"secretKey":"WGPRIV","peers":[{"endpoint":"13.228.66.113:32891","publicKey":"PUB"}]}}'),
		 (7,'CH-GZ','Imported','Vless','120.197.11.148',16938,'uuid-7',1,59,'{"encryption":"none","flow":"xtls-rprx-vision","streamSettings":{"network":"tcp","security":"reality","realitySettings":{"serverName":"www.apple.com","publicKey":"P","shortId":"s"}}}'),
		 (9,'CH-WH','Imported','Vless','58.19.38.60',24895,'uuid-9',0,158,'{}'),
		 (12,'SG01','Imported','Vless','13.228.66.113',57244,'uuid-12',1,75,'{"settings":{"vnext":[{"users":[{"id":"uuid-12"}]}]}}')`,
		`INSERT INTO rules (type,value,policy,priority,group_id,group_name) VALUES
		 ('geosite','category-ads-all','block',1,'',''),
		 ('ip','192.168.100.0/24','direct',2,'',''),
		 ('geosite','netflix','ha-6-12',3,'',''),
		 ('geoip','telegram','ha-6-12',4,'',''),
		 ('geosite','cn','ha-7-9',5,'',''),
		 ('domain','**.youtube.com','proxy',6,'g1','流媒体'),
		 ('domain','bad..value','proxy',7,'g1','流媒体'),
		 ('ip','1.1.1.1','proxy-7',8,'',''),
		 ('ip','2.2.2.2','proxy-99',9,'','')`,
		`INSERT INTO settings VALUES ('mode','A'),('default_node_id','6'),('lan_default_policy','proxy'),('node_failover_mode','normal'),('dns_local','223.5.5.5,119.29.29.29'),('dns_remote','172.20.1.11,10.100.10.10'),('dns_lazy','true'),('dns_cache_size','10240'),('dns_lazy_ttl','86400'),('dns_log_level','info'),('management_iface','eth0'),('service_iface','eth0'),('ospf_push_batch_limit','500'),('ospf_push_interval_seconds','10'),('ospf_resolve_workers','16'),('cron_enabled','true'),('cron_time','04:00'),('cron_schedule_type','weekly')`,
		`INSERT INTO lan_acls (type,value,policy,remark) VALUES ('mac','AA:BB:CC:DD:EE:01','direct','tv'),('ip','192.168.1.0/24','proxy','')`,
		`INSERT INTO protected_ips (value) VALUES ('1.32.219.132/32'),('120.197.11.148/32')`,
		`INSERT INTO remote_nodes (id,name,type,ssh_host,ssh_port,ssh_user,ssh_auth_type,ssh_credential,ssh_host_key,region,status,remark) VALUES
		 (16,'SG01','vless','13.228.66.113',22,'admin','key','` + v1Encrypt(t, key, "-----KEY-----") + `','SHA256:abc','','Online',''),
		 (17,'SGWG01','wg','13.228.66.113',22,'admin','key','` + v1Encrypt(t, key, "-----KEY-----") + `','SHA256:abc','','Online','')`,
		`INSERT INTO remote_node_wg VALUES (17,'SP','SPUB','CP','CPUB','13.228.66.113:32891',32891,'10.92.61.1/24','10.92.61.2/32')`,
		`INSERT INTO remote_node_vless VALUES (16,'uuid-12','RPRIV','RPUB','142c8654','www.apple.com','www.apple.com:443',57244,'')`,
	}
	for _, s := range stmts {
		if _, err := db.Exec(s); err != nil {
			t.Fatalf("%v\n%s", err, s)
		}
	}
	return path
}

func TestImportProductionShape(t *testing.T) {
	ctx := context.Background()
	oldKey := bytes.Repeat([]byte{3}, 32)
	src := fixture(t, oldKey)
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "new.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	_, _ = st.Migrate(ctx)
	kr, _ := secret.FromBytes(bytes.Repeat([]byte{7}, 32))

	dry, err := Import(ctx, st, kr, Options{SourceDB: src, OldKey: oldKey, DryRun: true})
	if err != nil {
		t.Fatal(err)
	}
	if n, _ := st.ListNodes(ctx); len(n) != 0 {
		t.Fatal("dry run wrote nodes")
	}
	rep, err := Import(ctx, st, kr, Options{SourceDB: src, OldKey: oldKey})
	if err != nil {
		t.Fatalf("import: %v (report %+v)", err, rep)
	}
	if rep.Nodes != 4 || rep.Groups != 2 || rep.RuleSets != 2 || rep.Rules != 8 || rep.DevicePolicies != 2 || rep.Bypass != 2 || rep.Hosts != 2 || rep.Deploys != 2 || rep.Mode != "A" || dry.Rules != rep.Rules {
		t.Fatalf("report %+v", rep)
	}
	joined := strings.Join(rep.Warnings, "\n")
	for _, want := range []string{"bad..value", "node 99 no longer exists", "weekly"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("missing warning %q in %v", want, rep.Warnings)
		}
	}

	nodes, _ := st.ListNodes(ctx)
	byName := map[string]store.NodeRow{}
	for _, n := range nodes {
		byName[n.Name] = n
	}
	wg := byName["ProxyGW-SG"]
	if wg.Protocol != "wireguard" || !wg.IsDefault || wg.LatencyMs != nil || strings.Contains(wg.Params, "secretKey") || len(wg.CredentialEnc) == 0 {
		t.Fatalf("wg node %+v", wg.Node)
	}
	if cred, _ := kr.Decrypt(wg.CredentialEnc); cred != "WGPRIV" {
		t.Fatalf("wg credential %q", cred)
	}
	gz := byName["CH-GZ"]
	if gz.Protocol != "vless" || *gz.LatencyMs != 59 || !strings.Contains(gz.Params, "realitySettings") {
		t.Fatalf("vless node %+v", gz.Node)
	}
	if byName["CH-WH"].Enabled {
		t.Fatal("disabled node imported as enabled")
	}
	if strings.Contains(byName["SG01"].Params, "uuid-12") {
		t.Fatal("embedded credential copy not stripped from params")
	}

	groups, _ := st.ListGroups(ctx)
	if len(groups) != 2 || groups[0].Kind != "failover" || len(groups[0].MemberIDs) != 2 || !strings.HasPrefix(groups[0].Name, "HA ") {
		t.Fatalf("groups %+v", groups)
	}
	sets, _ := st.ListRuleSets(ctx)
	if sets[0].Name != "默认" || sets[1].Name != "流媒体" {
		t.Fatalf("sets %+v", sets)
	}
	rules, _ := st.ListRules(ctx, "")
	kinds := []string{}
	for _, r := range rules {
		kinds = append(kinds, string(r.MatchKind)+":"+r.MatchValue+"->"+string(r.ActionKind))
	}
	// Imported rules keep their order and precedence; the seeded built-in
	// direct rules follow them inside the built-in (first) set.
	want := "geosite:category-ads-all->block ip:192.168.100.0/24->direct geosite:netflix->group geoip:telegram->group geosite:cn->group ip:1.1.1.1/32->node ip:2.2.2.2/32->proxy geoip:private->direct geosite:cn->direct geoip:cn->direct domain:**.youtube.com->proxy"
	if got := strings.Join(kinds, " "); got != want {
		t.Fatalf("rules\n got %s\nwant %s", got, want)
	}
	if rules[5].ActionRef != gz.ID {
		t.Fatal("proxy-7 should point at CH-GZ")
	}
	net, _ := st.NetworkConfig(ctx)
	if net.ServiceIface != "eth0" || net.OSPFPushIntervalSec != 10 || net.OSPFResolveWorkers != 16 {
		t.Fatalf("network %+v", net)
	}
	dns, _ := st.DNSConfig(ctx)
	if strings.Join(dns.RemoteUpstreams, ",") != "172.20.0.11,10.100.10.10" && strings.Join(dns.RemoteUpstreams, ",") != "172.20.1.11,10.100.10.10" {
		t.Fatalf("dns %+v", dns)
	}
	if v, _ := st.GetSetting(ctx, "geodata.auto_update"); v != "daily 04:00" {
		t.Fatalf("schedule %q", v)
	}
	devs, _ := st.ListDevicePolicies(ctx)
	if len(devs) != 2 || devs[0].Value != "aa:bb:cc:dd:ee:01" || devs[1].Kind != "cidr" {
		t.Fatalf("devices %+v", devs)
	}
	hosts, _ := st.ListRemoteHosts(ctx)
	if len(hosts) != 2 || hosts[0].HostKey != "SHA256:abc" {
		t.Fatalf("hosts %+v", hosts)
	}
	if cred, _ := kr.Decrypt(hosts[0].CredentialEnc); cred != "-----KEY-----" {
		t.Fatalf("host credential %q", cred)
	}
	deploys, _ := st.ListRemoteDeploys(ctx, "", 10)
	linked := 0
	for _, d := range deploys {
		if d.State != "succeeded" {
			t.Fatalf("deploy %+v", d)
		}
		if d.NodeID != "" {
			linked++
		}
	}
	if linked != 2 {
		t.Fatalf("deploys should link to the nodes 1.x created: %+v", deploys)
	}
	// Second import without --replace is refused; without the key hosts lose credentials.
	if _, err := Import(ctx, st, kr, Options{SourceDB: src}); err == nil {
		t.Fatal("import into a populated store must be refused")
	}
	rep, err = Import(ctx, st, kr, Options{SourceDB: src, Replace: true})
	if err != nil || rep.HostsNoCred != 2 {
		t.Fatalf("replace import %v %+v", err, rep)
	}
}
