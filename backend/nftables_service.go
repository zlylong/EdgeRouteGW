package main

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"text/template"
	"time"
)

const nftablesTmpl = `#!/usr/sbin/nft -f
flush ruleset
table inet proxygw {
    set mac_proxy {
        type ether_addr
        {{if .MacProxy}}elements = { {{.MacProxy}} }{{end}}
    }
    set mac_direct {
        type ether_addr
        {{if .MacDirect}}elements = { {{.MacDirect}} }{{end}}
    }
    set ip_proxy {
        type ipv4_addr; flags interval
        {{if .IPProxy}}elements = { {{.IPProxy}} }{{end}}
    }
    set ip_direct {
        type ipv4_addr; flags interval
        {{if .IPDirect}}elements = { {{.IPDirect}} }{{end}}
    }
    set ip6_proxy {
        type ipv6_addr; flags interval
        {{if .IP6Proxy}}elements = { {{.IP6Proxy}} }{{end}}
    }
    set ip6_direct {
        type ipv6_addr; flags interval
        {{if .IP6Direct}}elements = { {{.IP6Direct}} }{{end}}
    }
    set protected_ips {
        type ipv4_addr; flags interval
        {{if .ProtectedIPs}}elements = { {{.ProtectedIPs}} }{{end}}
    }

    chain prerouting {
        type filter hook prerouting priority mangle; policy accept;
        meta mark 0x02 return
        
        # Local & Multicast Bypasses
        ip daddr { 127.0.0.0/8, 192.168.0.0/16, 10.0.0.0/8, 172.16.0.0/12, 224.0.0.0/4, 255.255.255.255/32 } return
        ip6 daddr { ::1/128, fc00::/7, fe80::/10, ff00::/8 } return

        # LAN ACL overrides
        ether saddr @mac_direct counter return comment "acl_mac_direct"
        ip saddr @ip_direct counter return comment "acl_ip_direct"
        ip6 saddr @ip6_direct counter return comment "acl_ip6_direct"
        
        ether saddr @mac_proxy meta l4proto { tcp, udp } meta nfproto ipv4 mark set 1 tproxy ip to 127.0.0.1:12345 counter accept comment "proxy_acl_mac_v4"
        ether saddr @mac_proxy meta l4proto { tcp, udp } meta nfproto ipv6 mark set 1 tproxy ip6 to [::1]:12345 counter accept comment "proxy_acl_mac_v6"
        
        ip saddr @ip_proxy meta l4proto { tcp, udp } mark set 1 tproxy ip to 127.0.0.1:12345 counter accept comment "proxy_acl_ip_v4"
        ip6 saddr @ip6_proxy meta l4proto { tcp, udp } mark set 1 tproxy ip6 to [::1]:12345 counter accept comment "proxy_acl_ip_v6"

        {{if eq .Mode "A"}}
        # Mode A: protected destination IPs always go direct (avoid rule-change disconnect)
        ip daddr @protected_ips counter return comment "mode_a_protected_ip_direct"

        # Mode A: reject QUIC (UDP/443) immediately to force fast TCP fallback
        meta l4proto udp th dport 443 counter reject with icmpx type port-unreachable comment "mode_a_quic_reject"
        {{end}}

        # Default policy
        {{if eq .DefaultPolicy "proxy"}}
        meta l4proto { tcp, udp } meta nfproto ipv4 mark set 1 tproxy ip to 127.0.0.1:12345 counter accept comment "proxy_default_v4"
        meta l4proto { tcp, udp } meta nfproto ipv6 mark set 1 tproxy ip6 to [::1]:12345 counter accept comment "proxy_default_v6"
        {{else}}
        counter return comment "default_direct"
        {{end}}
    }

    chain output {
        type route hook output priority mangle; policy accept;
        meta mark 0x02 return
        ip daddr { 127.0.0.0/8, 192.168.0.0/16, 10.0.0.0/8, 172.16.0.0/12, 224.0.0.0/4, 255.255.255.255/32 } return
        ip6 daddr { ::1/128, fc00::/7, fe80::/10, ff00::/8 } return
        {{if eq .Mode "A"}}
        ip daddr @protected_ips counter return comment "mode_a_protected_ip_direct_output"
        {{end}}
        # Do NOT hijack host-originated traffic into TProxy; keep host egress direct to avoid self-blackhole
        counter return comment "host_egress_direct"
    }
}
`

func applyNftablesConfig() error {
	var defaultPolicy string
	if err := getDB().QueryRow("SELECT value FROM settings WHERE key='lan_default_policy'").Scan(&defaultPolicy); err != nil {
		defaultPolicy = "proxy"
	}

	var mode string
	if err := getDB().QueryRow("SELECT value FROM settings WHERE key='mode'").Scan(&mode); err != nil {
		mode = "A" // default
	}

	var macProxy, macDirect, ipProxy, ipDirect, ip6Proxy, ip6Direct, protectedIPs string

	// ONLY process LAN ACLs if we are in Mode A.
	// In Mode B and C, these cause loops or blackholes, so we keep the variables empty.
	if mode == "A" {

		// Mode A protected IP list: always direct, to avoid disconnect during xray rule reload
		if rows, err := getDB().Query("SELECT value FROM protected_ips ORDER BY id"); err == nil {
			defer rows.Close()
			for rows.Next() {
				var v string
				if err := rows.Scan(&v); err == nil {
					v = strings.TrimSpace(v)
					if v == "" {
						continue
					}
					if protectedIPs != "" {
						protectedIPs += ", "
					}
					protectedIPs += v
				}
			}
		}
		rows, err := getDB().Query("SELECT type, value, policy FROM lan_acls")
		if err == nil {
			defer rows.Close()
			for rows.Next() {
				var t, v, p string
				if err := rows.Scan(&t, &v, &p); err == nil {
					if t == "mac" {
						if p == "proxy" {
							if macProxy != "" {
								macProxy += ", "
							}
							macProxy += v
						} else if p == "direct" {
							if macDirect != "" {
								macDirect += ", "
							}
							macDirect += v
						}
					} else if t == "ip" {
						isIPv6 := strings.Contains(v, ":")
						if p == "proxy" {
							if isIPv6 {
								if ip6Proxy != "" {
									ip6Proxy += ", "
								}
								ip6Proxy += v
							} else {
								if ipProxy != "" {
									ipProxy += ", "
								}
								ipProxy += v
							}
						} else if p == "direct" {
							if isIPv6 {
								if ip6Direct != "" {
									ip6Direct += ", "
								}
								ip6Direct += v
							} else {
								if ipDirect != "" {
									ipDirect += ", "
								}
								ipDirect += v
							}
						}
					}
				}
			}
		}
	}

	data := struct {
		MacProxy      string
		MacDirect     string
		IPProxy       string
		IPDirect      string
		IP6Proxy      string
		IP6Direct     string
		ProtectedIPs  string
		DefaultPolicy string
		Mode          string
	}{
		MacProxy:      macProxy,
		MacDirect:     macDirect,
		IPProxy:       ipProxy,
		IPDirect:      ipDirect,
		IP6Proxy:      ip6Proxy,
		IP6Direct:     ip6Direct,
		ProtectedIPs:  protectedIPs,
		DefaultPolicy: defaultPolicy,
		Mode:          mode,
	}

	tmpl, err := template.New("nftables").Parse(nftablesTmpl)
	if err != nil {
		return fmt.Errorf("failed to parse template: %v", err)
	}

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return fmt.Errorf("failed to execute template: %v", err)
	}

	newConfig := buf.Bytes()
	stagingPath := "/etc/nftables.conf.proxygw.new"
	activePath := "/etc/nftables.conf"
	runtimeBackupPath := filepath.Join(os.TempDir(), "proxygw-nftables-runtime-backup.nft")

	if err := os.WriteFile(stagingPath, newConfig, 0644); err != nil {
		return fmt.Errorf("failed to stage nftables config: %v", err)
	}
	defer os.Remove(stagingPath)

	if res := sysCmd.runCombinedOutput("nft", "-c", "-f", stagingPath); res.Err != nil {
		return fmt.Errorf("nft config validation failed: %v, out: %s", res.Err, strings.TrimSpace(string(res.Output)))
	}

	// Backup current runtime ruleset for rollback.
	var haveRuntimeBackup bool
	if res := sysCmd.runCombinedOutput("nft", "list", "ruleset"); res.Err == nil && len(bytes.TrimSpace(res.Output)) > 0 {
		if err := os.WriteFile(runtimeBackupPath, res.Output, 0600); err == nil {
			haveRuntimeBackup = true
			defer os.Remove(runtimeBackupPath)
		}
	}

	// Backup persisted config for rollback.
	oldConfig, oldConfigErr := os.ReadFile(activePath)
	hadOldConfig := oldConfigErr == nil

	if err := os.WriteFile(activePath, newConfig, 0644); err != nil {
		return fmt.Errorf("failed to write active nftables config: %v", err)
	}

	if res := sysCmd.runCombinedOutput("nft", "-f", activePath); res.Err != nil {
		// Roll back runtime rules first if possible.
		if haveRuntimeBackup {
			if rb := sysCmd.runCombinedOutput("nft", "-f", runtimeBackupPath); rb.Err != nil {
				logGatewayEventThrottled("nft_apply_runtime_rollback_failed", 2*time.Minute, "error", "nftables", "apply_runtime_rollback_failed", "failed to rollback nftables runtime ruleset after apply error", map[string]interface{}{"reason": rb.Err.Error(), "output": strings.TrimSpace(string(rb.Output))})
			}
		}
		// Roll back persisted config file.
		if hadOldConfig {
			if werr := os.WriteFile(activePath, oldConfig, 0644); werr != nil {
				logGatewayEventThrottled("nft_apply_config_rollback_failed", 2*time.Minute, "error", "nftables", "apply_config_rollback_failed", "failed to restore previous nftables config file", map[string]interface{}{"reason": werr.Error()})
			}
		}
		return fmt.Errorf("nft apply failed (rolled back): %v, out: %s", res.Err, strings.TrimSpace(string(res.Output)))
	}

	return nil
}
