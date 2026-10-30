package sharelink

import (
	"encoding/base64"
	"encoding/json"
	"testing"

	"github.com/zlylong/EdgeRouteGW/internal/model"
)

func TestVless(t *testing.T) {
	p, err := Parse("vless://11111111-2222-3333-4444-555555555555@sg.example.com:443?encryption=none&flow=xtls-rprx-vision&security=reality&sni=www.apple.com&fp=chrome&pbk=PBK&sid=ab12&type=tcp#SG%2001")
	if err != nil {
		t.Fatal(err)
	}
	if p.Node.Protocol != model.ProtocolVLESS || p.Node.Name != "SG 01" || p.Node.Address != "sg.example.com" || p.Node.Port != 443 || p.Credential != "11111111-2222-3333-4444-555555555555" {
		t.Fatalf("%+v", p)
	}
	var params map[string]any
	_ = json.Unmarshal([]byte(p.Node.Params), &params)
	if params["security"] != "reality" || params["pbk"] != "PBK" || params["flow"] != "xtls-rprx-vision" || params["type"] != "tcp" {
		t.Fatalf("params %v", params)
	}
}

func TestVlessWsFoldsStreamSettings(t *testing.T) {
	p, err := Parse("vless://uuid@h.example.com:8443?type=ws&path=%2Fws&host=cdn.example.com&security=tls&sni=cdn.example.com#ws")
	if err != nil {
		t.Fatal(err)
	}
	var params map[string]any
	_ = json.Unmarshal([]byte(p.Node.Params), &params)
	ss := params["streamSettings"].(map[string]any)
	if ss["network"] != "ws" || ss["security"] != "tls" || ss["wsSettings"].(map[string]any)["path"] != "/ws" || ss["tlsSettings"].(map[string]any)["serverName"] != "cdn.example.com" {
		t.Fatalf("ss %v", ss)
	}
	if _, leaked := params["sni"]; leaked {
		t.Fatal("shorthand must be folded when streamSettings is explicit")
	}
}

func TestVmess(t *testing.T) {
	payload := map[string]any{"v": "2", "ps": "HK", "add": "1.2.3.4", "port": "443", "id": "uuid", "aid": "0", "net": "ws", "path": "/v", "host": "h", "tls": "tls", "sni": "s"}
	b, _ := json.Marshal(payload)
	p, err := Parse("vmess://" + base64.StdEncoding.EncodeToString(b))
	if err != nil {
		t.Fatal(err)
	}
	if p.Node.Protocol != model.ProtocolVMess || p.Node.Port != 443 || p.Credential != "uuid" || p.Node.Name != "HK" {
		t.Fatalf("%+v", p)
	}
	var params map[string]any
	_ = json.Unmarshal([]byte(p.Node.Params), &params)
	ss := params["streamSettings"].(map[string]any)
	if ss["network"] != "ws" || ss["security"] != "tls" {
		t.Fatalf("ss %v", ss)
	}
}

func TestShadowsocks(t *testing.T) {
	ui := base64.RawURLEncoding.EncodeToString([]byte("chacha20-ietf-poly1305:secret"))
	p, err := Parse("ss://" + ui + "@5.6.7.8:8388#SS%20node")
	if err != nil {
		t.Fatal(err)
	}
	if p.Node.Protocol != model.ProtocolShadowsocks || p.Credential != "secret" || p.Node.Name != "SS node" || p.Node.Params != `{"method":"chacha20-ietf-poly1305"}` {
		t.Fatalf("%+v", p)
	}
	legacy := base64.StdEncoding.EncodeToString([]byte("aes-256-gcm:pw@host.example:443"))
	p, err = Parse("ss://" + legacy)
	if err != nil || p.Node.Address != "host.example" || p.Credential != "pw" {
		t.Fatalf("legacy: %v %+v", err, p)
	}
}

func TestTrojanAndWireGuard(t *testing.T) {
	p, err := Parse("trojan://pw@t.example.com:443?security=tls&sni=t.example.com#T")
	if err != nil || p.Node.Protocol != model.ProtocolTrojan || p.Credential != "pw" {
		t.Fatalf("%v %+v", err, p)
	}
	p, err = Parse("wireguard://PRIV@1.1.1.1:51820?publickey=PUB&address=10.0.0.2/32&mtu=1280&reserved=1,2,3#WG")
	if err != nil || p.Node.Protocol != model.ProtocolWireGuard || p.Credential != "PRIV" {
		t.Fatalf("%v %+v", err, p)
	}
	var params map[string]any
	_ = json.Unmarshal([]byte(p.Node.Params), &params)
	settings := params["settings"].(map[string]any)
	if settings["mtu"].(float64) != 1280 || settings["peers"].([]any)[0].(map[string]any)["publicKey"] != "PUB" {
		t.Fatalf("settings %v", settings)
	}
}

func TestHysteria2(t *testing.T) {
	p, err := Parse("hysteria2://letmein@example.com:123,5000-6000/?insecure=1&obfs=salamander&obfs-password=gawrgura&pinSHA256=deadbeef&sni=real.example.com#cool-server")
	if err != nil {
		t.Fatal(err)
	}
	if p.Node.Protocol != model.ProtocolHysteria || p.Node.Name != "cool-server" || p.Node.Address != "example.com" || p.Node.Port != 123 || p.Credential != "letmein" {
		t.Fatalf("%+v", p)
	}
	var params map[string]any
	_ = json.Unmarshal([]byte(p.Node.Params), &params)
	if params["sni"] != "real.example.com" || params["pin"] != "deadbeef" || params["obfs"] != "salamander" || params["obfs_password"] != "gawrgura" || params["hop"] != "123,5000-6000" {
		t.Fatalf("params %v", params)
	}
	p, err = Parse("hy2://user%3Apass@[2001:db8::1]/?sni=x.example.com")
	if err != nil || p.Node.Port != 443 || p.Node.Address != "2001:db8::1" || p.Credential != "user:pass" {
		t.Fatalf("defaults: %+v %v", p, err)
	}
	if _, err := Parse("hysteria2://pw@h.example.com:443/?insecure=1"); err == nil {
		t.Fatal("insecure without pin must be rejected")
	}
}

func TestRejects(t *testing.T) {
	for _, bad := range []string{"", "http://x", "vless://@host:443", "ss://not-base64!!", "vmess://%%%", "wireguard://k@h:1"} {
		if _, err := Parse(bad); err == nil {
			t.Fatalf("%q should be rejected", bad)
		}
	}
}
