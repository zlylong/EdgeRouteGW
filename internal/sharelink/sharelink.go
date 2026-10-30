// Package sharelink parses proxy share links (vmess://, vless://, trojan://,
// ss://, wireguard://, hysteria2://) into a Node plus its credential. The output params use
// the shorthand keys the renderer understands, so a parsed link renders the
// same way as a node entered by hand.
package sharelink

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"github.com/zlylong/EdgeRouteGW/internal/model"
)

// Parsed is the result of Parse.
type Parsed struct {
	Node       model.Node // ID empty; Params set; Enabled true
	Credential string
}

// Parse decodes a share link.
func Parse(link string) (Parsed, error) {
	link = strings.TrimSpace(link)
	scheme, rest, ok := strings.Cut(link, "://")
	if !ok {
		return Parsed{}, fmt.Errorf("not a share link")
	}
	switch strings.ToLower(scheme) {
	case "vmess":
		return parseVmess(rest)
	case "vless":
		return parseURLStyle(link, model.ProtocolVLESS)
	case "trojan":
		return parseURLStyle(link, model.ProtocolTrojan)
	case "ss":
		return parseShadowsocks(link)
	case "wireguard", "wg":
		return parseWireGuard(link)
	case "hysteria2", "hy2":
		return parseHysteria2(link)
	default:
		return Parsed{}, fmt.Errorf("unsupported share link scheme %q", scheme)
	}
}

func parseVmess(b64 string) (Parsed, error) {
	raw, err := decodeB64(b64)
	if err != nil {
		return Parsed{}, fmt.Errorf("vmess: invalid base64")
	}
	var v struct {
		Ps, Add, ID, Net, Tls, Sni, Path, Host, Scy, Type, Fp string
		Port                                                  any
		Aid                                                   any
	}
	if err := json.Unmarshal(raw, &v); err != nil {
		return Parsed{}, fmt.Errorf("vmess: invalid payload")
	}
	port := anyPort(v.Port)
	if v.Add == "" || v.ID == "" || port == 0 {
		return Parsed{}, fmt.Errorf("vmess: missing address, id or port")
	}
	params := map[string]any{}
	ss := map[string]any{}
	if v.Net != "" {
		ss["network"] = v.Net
	}
	switch v.Net {
	case "ws":
		ws := map[string]any{}
		if v.Path != "" {
			ws["path"] = v.Path
		}
		if v.Host != "" {
			ws["headers"] = map[string]string{"Host": v.Host}
		}
		if len(ws) > 0 {
			ss["wsSettings"] = ws
		}
	case "grpc":
		if v.Path != "" {
			ss["grpcSettings"] = map[string]any{"serviceName": v.Path}
		}
	case "h2":
		h2 := map[string]any{}
		if v.Path != "" {
			h2["path"] = v.Path
		}
		if v.Host != "" {
			h2["host"] = []string{v.Host}
		}
		if len(h2) > 0 {
			ss["httpSettings"] = h2
		}
	}
	if v.Tls == "tls" {
		ss["security"] = "tls"
		tls := map[string]any{}
		if v.Sni != "" {
			tls["serverName"] = v.Sni
		}
		if v.Fp != "" {
			tls["fingerprint"] = v.Fp
		}
		if len(tls) > 0 {
			ss["tlsSettings"] = tls
		}
	}
	if len(ss) > 0 {
		params["streamSettings"] = ss
	}
	if v.Scy != "" && v.Scy != "auto" {
		params["security"] = v.Scy
	}
	name := v.Ps
	if name == "" {
		name = v.Add
	}
	return Parsed{Node: model.Node{Name: name, Protocol: model.ProtocolVMess, Address: v.Add, Port: port, Params: mustJSON(params), Enabled: true}, Credential: v.ID}, nil
}

// parseURLStyle handles vless:// and trojan://, which share the
// user@host:port?query#name shape.
func parseURLStyle(link string, proto model.Protocol) (Parsed, error) {
	u, err := url.Parse(link)
	if err != nil {
		return Parsed{}, fmt.Errorf("%s: %v", proto, err)
	}
	cred := u.User.Username()
	host := u.Hostname()
	port, _ := strconv.Atoi(u.Port())
	if host == "" || cred == "" || port <= 0 || port > 65535 {
		return Parsed{}, fmt.Errorf("%s: missing host, credential or port", proto)
	}
	q := u.Query()
	params := map[string]any{}
	for key, dst := range map[string]string{"type": "type", "security": "security", "sni": "sni", "fp": "fp", "pbk": "pbk", "sid": "sid", "flow": "flow", "encryption": "encryption", "spx": "spx"} {
		if v := q.Get(key); v != "" {
			params[dst] = v
		}
	}
	// Transport details that live under streamSettings rather than shorthand.
	switch q.Get("type") {
	case "ws":
		ws := map[string]any{}
		if p := q.Get("path"); p != "" {
			ws["path"] = p
		}
		if h := q.Get("host"); h != "" {
			ws["headers"] = map[string]string{"Host": h}
		}
		if len(ws) > 0 {
			params["streamSettings"] = map[string]any{"network": "ws", "wsSettings": ws}
			applySecurity(params)
		}
	case "grpc":
		if sn := q.Get("serviceName"); sn != "" {
			params["streamSettings"] = map[string]any{"network": "grpc", "grpcSettings": map[string]any{"serviceName": sn}}
			applySecurity(params)
		}
	case "xhttp", "splithttp":
		x := map[string]any{}
		if p := q.Get("path"); p != "" {
			x["path"] = p
		}
		if h := q.Get("host"); h != "" {
			x["host"] = h
		}
		if m := q.Get("mode"); m != "" {
			x["mode"] = m
		}
		params["streamSettings"] = map[string]any{"network": "xhttp", "xhttpSettings": x}
		applySecurity(params)
	}
	name, _ := url.QueryUnescape(u.Fragment)
	if name == "" {
		name = host
	}
	return Parsed{Node: model.Node{Name: name, Protocol: proto, Address: host, Port: port, Params: mustJSON(params), Enabled: true}, Credential: cred}, nil
}

// applySecurity folds shorthand TLS/REALITY keys into an explicit
// streamSettings when one was built for a non-tcp transport, since the
// renderer only expands shorthand when streamSettings is absent.
func applySecurity(params map[string]any) {
	ss, _ := params["streamSettings"].(map[string]any)
	if ss == nil {
		return
	}
	sec, _ := params["security"].(string)
	str := func(k string) string { v, _ := params[k].(string); return v }
	switch sec {
	case "tls":
		ss["security"] = "tls"
		tls := map[string]any{}
		if v := str("sni"); v != "" {
			tls["serverName"] = v
		}
		if v := str("fp"); v != "" {
			tls["fingerprint"] = v
		}
		ss["tlsSettings"] = tls
	case "reality":
		ss["security"] = "reality"
		rs := map[string]any{"serverName": str("sni"), "publicKey": str("pbk"), "shortId": str("sid"), "spiderX": "/"}
		if v := str("fp"); v != "" {
			rs["fingerprint"] = v
		}
		if v := str("spx"); v != "" {
			rs["spiderX"] = v
		}
		ss["realitySettings"] = rs
	}
	for _, k := range []string{"type", "security", "sni", "fp", "pbk", "sid", "spx"} {
		delete(params, k)
	}
}

// parseShadowsocks handles SIP002 (ss://base64(method:pass)@host:port#name)
// and the legacy fully base64 form.
func parseShadowsocks(link string) (Parsed, error) {
	body := strings.TrimPrefix(link, "ss://")
	name := ""
	if i := strings.LastIndex(body, "#"); i >= 0 {
		name, _ = url.QueryUnescape(body[i+1:])
		body = body[:i]
	}
	if q := strings.Index(body, "?"); q >= 0 {
		body = body[:q] // plugin params are not supported by the renderer
	}
	var userinfo, hostport string
	if at := strings.LastIndex(body, "@"); at >= 0 {
		userinfo, hostport = body[:at], body[at+1:]
		if dec, err := decodeB64(userinfo); err == nil && strings.Contains(string(dec), ":") {
			userinfo = string(dec)
		} else if u, err := url.PathUnescape(userinfo); err == nil {
			userinfo = u
		}
	} else {
		dec, err := decodeB64(body)
		if err != nil {
			return Parsed{}, fmt.Errorf("ss: invalid base64")
		}
		at := strings.LastIndex(string(dec), "@")
		if at < 0 {
			return Parsed{}, fmt.Errorf("ss: malformed legacy link")
		}
		userinfo, hostport = string(dec[:at]), string(dec[at+1:])
	}
	method, pass, ok := strings.Cut(userinfo, ":")
	if !ok || method == "" {
		return Parsed{}, fmt.Errorf("ss: missing method or password")
	}
	host, portStr, err := splitHostPort(hostport)
	if err != nil {
		return Parsed{}, fmt.Errorf("ss: %v", err)
	}
	port, _ := strconv.Atoi(portStr)
	if port <= 0 || port > 65535 {
		return Parsed{}, fmt.Errorf("ss: invalid port")
	}
	if name == "" {
		name = host
	}
	return Parsed{Node: model.Node{Name: name, Protocol: model.ProtocolShadowsocks, Address: host, Port: port, Params: mustJSON(map[string]any{"method": method}), Enabled: true}, Credential: pass}, nil
}

// parseWireGuard handles wireguard://privkey@host:port?publickey=..&address=..&mtu=..&reserved=a,b,c#name
func parseWireGuard(link string) (Parsed, error) {
	u, err := url.Parse(link)
	if err != nil {
		return Parsed{}, fmt.Errorf("wireguard: %v", err)
	}
	priv := u.User.Username()
	host := u.Hostname()
	port, _ := strconv.Atoi(u.Port())
	if priv == "" || host == "" || port <= 0 || port > 65535 {
		return Parsed{}, fmt.Errorf("wireguard: missing private key, host or port")
	}
	q := u.Query()
	pub := q.Get("publickey")
	if pub == "" {
		pub = q.Get("peer_public_key")
	}
	if pub == "" {
		return Parsed{}, fmt.Errorf("wireguard: missing peer public key")
	}
	peer := map[string]any{"endpoint": fmt.Sprintf("%s:%d", host, port), "publicKey": pub}
	if psk := q.Get("presharedkey"); psk != "" {
		peer["preSharedKey"] = psk
	}
	settings := map[string]any{"peers": []any{peer}}
	if addr := q.Get("address"); addr != "" {
		settings["address"] = strings.Split(addr, ",")
	}
	if mtu, err := strconv.Atoi(q.Get("mtu")); err == nil && mtu > 0 {
		settings["mtu"] = mtu
	}
	if rsv := q.Get("reserved"); rsv != "" {
		var nums []int
		for _, p := range strings.Split(rsv, ",") {
			if n, err := strconv.Atoi(strings.TrimSpace(p)); err == nil {
				nums = append(nums, n)
			}
		}
		if len(nums) == 3 {
			settings["reserved"] = nums
		}
	}
	name, _ := url.QueryUnescape(u.Fragment)
	if name == "" {
		name = host
	}
	return Parsed{Node: model.Node{Name: name, Protocol: model.ProtocolWireGuard, Address: host, Port: port, Params: mustJSON(map[string]any{"settings": settings}), Enabled: true}, Credential: priv}, nil
}

// parseHysteria2 handles hysteria2://auth@host[:port]/?sni=&obfs=salamander&
// obfs-password=&pinSHA256=&insecure=#name (also hy2://). The port may be a
// port-hopping spec ("443,20000-30000"): the first port is the node port and
// the whole spec becomes the hop range. url.Parse rejects such a port, so
// the authority is split by hand.
func parseHysteria2(link string) (Parsed, error) {
	rest := link[strings.Index(link, "://")+3:]
	name := ""
	if i := strings.LastIndex(rest, "#"); i >= 0 {
		name, _ = url.QueryUnescape(rest[i+1:])
		rest = rest[:i]
	}
	query := ""
	if i := strings.Index(rest, "?"); i >= 0 {
		query = rest[i+1:]
		rest = rest[:i]
	}
	rest = strings.TrimSuffix(rest, "/")
	auth := ""
	if at := strings.LastIndex(rest, "@"); at >= 0 {
		auth, _ = url.PathUnescape(rest[:at])
		rest = rest[at+1:]
	}
	host, portSpec := rest, "443"
	if strings.HasPrefix(rest, "[") {
		if i := strings.Index(rest, "]"); i >= 0 {
			host = rest[1:i]
			if len(rest) > i+1 && rest[i+1] == ':' {
				portSpec = rest[i+2:]
			}
		}
	} else if i := strings.LastIndex(rest, ":"); i >= 0 {
		host, portSpec = rest[:i], rest[i+1:]
	}
	first := portSpec
	if i := strings.IndexAny(portSpec, ",-"); i >= 0 {
		first = portSpec[:i]
	}
	port, _ := strconv.Atoi(first)
	if host == "" || auth == "" || port <= 0 || port > 65535 {
		return Parsed{}, fmt.Errorf("hysteria2: missing host, auth or port")
	}
	q, _ := url.ParseQuery(query)
	params := map[string]any{}
	if v := q.Get("sni"); v != "" {
		params["sni"] = v
	}
	if v := q.Get("pinSHA256"); v != "" {
		params["pin"] = v
	}
	if q.Get("insecure") == "1" && q.Get("pinSHA256") == "" {
		return Parsed{}, fmt.Errorf("hysteria2: insecure=1 without pinSHA256 is not supported (Xray no longer skips certificate verification; pin the server certificate or use a valid one)")
	}
	switch obfs := q.Get("obfs"); obfs {
	case "":
	case "salamander":
		params["obfs"] = obfs
		params["obfs_password"] = q.Get("obfs-password")
	default:
		return Parsed{}, fmt.Errorf("hysteria2: unsupported obfs %q", obfs)
	}
	if portSpec != first {
		params["hop"] = portSpec
	}
	if name == "" {
		name = host
	}
	return Parsed{Node: model.Node{Name: name, Protocol: model.ProtocolHysteria, Address: host, Port: port, Params: mustJSON(params), Enabled: true}, Credential: auth}, nil
}

func decodeB64(s string) ([]byte, error) {
	s = strings.TrimSpace(s)
	for _, enc := range []*base64.Encoding{base64.StdEncoding, base64.RawStdEncoding, base64.URLEncoding, base64.RawURLEncoding} {
		if b, err := enc.DecodeString(s); err == nil {
			return b, nil
		}
	}
	return nil, fmt.Errorf("invalid base64")
}

func anyPort(v any) int {
	switch t := v.(type) {
	case float64:
		return int(t)
	case string:
		n, _ := strconv.Atoi(t)
		return n
	case json.Number:
		n, _ := t.Int64()
		return int(n)
	}
	return 0
}

func splitHostPort(s string) (string, string, error) {
	i := strings.LastIndex(s, ":")
	if i < 0 {
		return "", "", fmt.Errorf("missing port in %q", s)
	}
	host := strings.Trim(s[:i], "[]")
	if host == "" {
		return "", "", fmt.Errorf("missing host")
	}
	return host, s[i+1:], nil
}

func mustJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return "{}"
	}
	return string(b)
}
