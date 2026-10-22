package provision

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/zlylong/EdgeRouteGW/internal/model"
	"github.com/zlylong/EdgeRouteGW/internal/store"
	"github.com/zlylong/EdgeRouteGW/internal/tune"
)

// HostTuneReport is the last tuning run on a managed host.
type HostTuneReport struct {
	At    time.Time   `json:"at"`
	Items []tune.Item `json:"items"`
	Error string      `json:"error,omitempty"`
}

func tuneKey(hostID string) string { return "host_tune:" + hostID }

// TuneScript inspects a managed host and applies what depends on what it
// finds, mirroring the gateway's own tuning: BBR + fq, buffers and TCP
// knobs via a sysctl file, file limits, NIC queues or RPS, conntrack
// sizing when NAT is in use (WireGuard servers), the WireGuard module.
// Every item prints one "TUNE|key|status|detail|hint" line.
func TuneScript() string {
	return `#!/bin/bash
set -u
say() { printf 'TUNE|%s|%s|%s|%s\n' "$1" "$2" "$3" "${4:-}"; }
CPUS=$(nproc 2>/dev/null || echo 1)
VIRT=$(systemd-detect-virt 2>/dev/null || echo none)
say host info "$CPUS CPU, kernel $(uname -r), $VIRT" ""

# --- congestion control -------------------------------------------------
modprobe tcp_bbr >/dev/null 2>&1 || true
if sysctl -n net.ipv4.tcp_available_congestion_control 2>/dev/null | grep -qw bbr; then
  F=/etc/sysctl.d/99-edgeroutegw-bbr.conf
  if [ "$(sysctl -n net.ipv4.tcp_congestion_control)" = bbr ] && [ "$(sysctl -n net.core.default_qdisc)" = fq ]; then
    say bbr ok "bbr / fq 生效" ""
  else
    printf 'net.core.default_qdisc=fq\nnet.ipv4.tcp_congestion_control=bbr\n' > "$F" && sysctl -q -p "$F" && say bbr applied "已启用 bbr / fq" "" || say bbr unavailable "写入 sysctl 失败" ""
  fi
else
  say bbr fallback "内核没有 tcp_bbr,保持 $(sysctl -n net.ipv4.tcp_congestion_control)" "换带 tcp_bbr 的内核"
fi

# --- sysctl: buffers, TCP, forwarding -------------------------------------
F=/etc/sysctl.d/99-edgeroutegw-tune.conf
cat > "$F" <<'EOT'
net.ipv4.ip_forward = 1
net.core.rmem_max = 16777216
net.core.wmem_max = 16777216
net.ipv4.tcp_rmem = 4096 87380 16777216
net.ipv4.tcp_wmem = 4096 16384 16777216
net.ipv4.tcp_fastopen = 3
net.ipv4.tcp_slow_start_after_idle = 0
net.ipv4.tcp_mtu_probing = 1
net.ipv4.tcp_tw_reuse = 1
net.ipv4.tcp_fin_timeout = 15
net.ipv4.ip_local_port_range = 1024 65535
net.ipv4.tcp_max_syn_backlog = 8192
net.core.somaxconn = 32768
net.core.netdev_max_backlog = 16384
EOT
BAD=0; TOTAL=0
while IFS='=' read -r k v; do
  k=$(echo "$k" | xargs); v=$(echo "$v" | xargs); [ -z "$k" ] && continue
  TOTAL=$((TOTAL+1))
  sysctl -q -w "$k=$v" >/dev/null 2>&1 || BAD=$((BAD+1))
done < "$F"
if [ "$BAD" = 0 ]; then say sysctl ok "$TOTAL 项生效(/etc/sysctl.d/99-edgeroutegw-tune.conf)" ""; else say sysctl unavailable "$BAD/$TOTAL 项写入失败" ""; fi

# --- conntrack (only meaningful when NAT/ct is in use, e.g. WireGuard) ----
if [ -e /proc/sys/net/netfilter/nf_conntrack_max ]; then
  CUR=$(cat /proc/sys/net/netfilter/nf_conntrack_max)
  if [ "$CUR" -ge 1048576 ]; then say conntrack ok "max=$CUR" ""; else
    sysctl -q -w net.netfilter.nf_conntrack_max=1048576 >/dev/null 2>&1 && \
    sysctl -q -w net.netfilter.nf_conntrack_tcp_timeout_established=3600 >/dev/null 2>&1 && \
    printf 'net.netfilter.nf_conntrack_max = 1048576\nnet.netfilter.nf_conntrack_tcp_timeout_established = 3600\n' > /etc/sysctl.d/99-edgeroutegw-conntrack.conf && \
    say conntrack applied "max $CUR -> 1048576" "" || say conntrack unavailable "设置失败" ""
  fi
else
  say conntrack info "模块未加载,无 NAT 流量" ""
fi

# --- file limits ----------------------------------------------------------
if [ "$(ulimit -n)" -ge 1048576 ] 2>/dev/null; then say nofile ok "打开文件数上限 $(ulimit -n)" ""; else
  printf '* soft nofile 1048576\n* hard nofile 1048576\nroot soft nofile 1048576\nroot hard nofile 1048576\n' > /etc/security/limits.d/99-edgeroutegw.conf
  say nofile applied "limits.d 已写入 1048576(新会话生效;xray/wg 服务由 unit 的 LimitNOFILE 决定)" ""
fi

# --- NIC queues / RPS -----------------------------------------------------
IF=$(ip -o route show default 2>/dev/null | awk '{print $5; exit}')
if [ -n "$IF" ]; then
  DRV=$(basename "$(readlink /sys/class/net/$IF/device/driver 2>/dev/null)" 2>/dev/null || echo "?")
  NQ=$(ls -d /sys/class/net/$IF/queues/rx-* 2>/dev/null | wc -l)
  if command -v ethtool >/dev/null 2>&1; then
    MAX=$(ethtool -l "$IF" 2>/dev/null | awk '/Pre-set/{p=1} /Current/{p=0} p && /Combined:/{print $2}')
    CUR=$(ethtool -l "$IF" 2>/dev/null | awk '/Current/{p=1} p && /Combined:/{print $2}')
    case "$MAX" in ''|n/a) MAX=1;; esac; case "$CUR" in ''|n/a) CUR=$NQ;; esac
    T=$CPUS; [ "$T" -gt "$MAX" ] && T=$MAX
    if [ "$MAX" -le 1 ]; then
      if [ "$VIRT" != none ]; then say nic-queues unavailable "网卡 $IF($DRV)只有 1 个队列,CPU $CPUS 个" "在宿主机给网卡开多队列后重启生效;已用 RPS 替代"; else say nic-queues unavailable "网卡 $IF($DRV)不支持多队列" "已用 RPS 替代"; fi
    elif [ "$CUR" -ge "$T" ]; then say nic-queues ok "$IF($DRV):$CUR/$MAX 个队列,CPU $CPUS 个" ""
    else ethtool -L "$IF" combined "$T" >/dev/null 2>&1 && { say nic-queues applied "$IF($DRV):队列 $CUR -> $T" ""; NQ=$T; } || say nic-queues unavailable "ethtool -L 失败" ""; fi
  else
    say nic-queues info "$IF($DRV):$NQ 个接收队列;未安装 ethtool,无法调整" "apt install ethtool"
  fi
  if [ "$NQ" -lt "$CPUS" ]; then
    MASK=$(printf '%x' $(( (1 << CPUS) - 1 )))
    OK=1; for q in /sys/class/net/$IF/queues/rx-*; do echo "$MASK" > "$q/rps_cpus" 2>/dev/null || OK=0; echo 4096 > "$q/rps_flow_cnt" 2>/dev/null; done
    sysctl -q -w net.core.rps_sock_flow_entries=32768 >/dev/null 2>&1
    [ "$OK" = 1 ] && say rps applied "软中断分散到 $CPUS 个 CPU(掩码 $MASK)" "" || say rps unavailable "写入 rps_cpus 失败" ""
  else
    say rps info "队列数不少于 CPU 数,无需 RPS" ""
  fi
else
  say nic-queues unavailable "没有默认路由,未能确定网卡" ""
fi

# --- WireGuard module (relevant to WireGuard servers) ----------------------
if systemctl is-enabled wg-quick@wg0 >/dev/null 2>&1; then
  if [ -d /sys/module/wireguard ]; then say wireguard ok "内核模块已加载" ""; else say wireguard fallback "wg-quick 用的是 wireguard-go(用户态)" "安装带 wireguard 模块的内核"; fi
fi
echo "TUNE_DONE"
`
}

// ParseTuneOutput turns the script's TUNE lines into items.
func ParseTuneOutput(lines []string) []tune.Item {
	var items []tune.Item
	for _, l := range lines {
		if !strings.HasPrefix(l, "TUNE|") {
			continue
		}
		f := strings.SplitN(strings.TrimPrefix(l, "TUNE|"), "|", 4)
		if len(f) < 3 {
			continue
		}
		it := tune.Item{Key: f[0], Status: f[1], Detail: f[2], Title: tuneTitle(f[0])}
		if len(f) == 4 {
			it.Hint = f[3]
		}
		items = append(items, it)
	}
	return items
}

func tuneTitle(key string) string {
	switch key {
	case "host":
		return "主机"
	case "bbr":
		return "拥塞控制 BBR + fq"
	case "sysctl":
		return "内核参数(转发、缓冲区、TCP)"
	case "conntrack":
		return "conntrack 表容量"
	case "nofile":
		return "打开文件数上限"
	case "nic-queues":
		return "网卡多队列"
	case "rps":
		return "RPS 软中断分散"
	case "wireguard":
		return "内核 WireGuard"
	}
	return key
}

// Tune runs the tuning script on a host over SSH and stores the report.
func (s *Service) Tune(ctx context.Context, hostID string) (*HostTuneReport, error) {
	h, err := s.Store.GetRemoteHost(ctx, hostID)
	if err != nil {
		return nil, err
	}
	if !s.acquire(h.ID) {
		return nil, ErrBusy
	}
	defer s.release(h.ID)
	t, err := s.target(h)
	if err != nil {
		return nil, err
	}
	sess, fp, err := s.Dialer.Dial(ctx, t)
	if err != nil {
		rep := &HostTuneReport{At: time.Now(), Error: err.Error()}
		s.saveTune(ctx, h.ID, rep)
		return rep, err
	}
	defer sess.Close()
	if h.HostKey == "" && fp != "" {
		_ = s.Store.SetRemoteHostKey(ctx, h.ID, fp)
	}
	var lines []string
	runErr := sess.Run(ctx, TuneScript(), 90*time.Second, func(_, line string) { lines = append(lines, line) })
	rep := &HostTuneReport{At: time.Now(), Items: ParseTuneOutput(lines)}
	if runErr != nil && len(rep.Items) == 0 {
		rep.Error = runErr.Error()
	}
	s.saveTune(ctx, h.ID, rep)
	_ = s.Store.AddEvent(ctx, model.Event{Level: model.LevelInfo, Module: "provision", Type: "host.tuned", Message: fmt.Sprintf("tuning run on %s: %d items", h.Name, len(rep.Items))})
	if rep.Error != "" {
		return rep, fmt.Errorf("%s", rep.Error)
	}
	return rep, nil
}

func (s *Service) saveTune(ctx context.Context, hostID string, rep *HostTuneReport) {
	b, _ := json.Marshal(rep)
	_ = s.Store.SetSetting(ctx, tuneKey(hostID), string(b))
}

// LastTune returns the stored report for a host, if any.
func (s *Service) LastTune(ctx context.Context, hostID string) *HostTuneReport {
	v, err := s.Store.GetSetting(ctx, tuneKey(hostID))
	if err != nil || v == "" {
		return nil
	}
	var rep HostTuneReport
	if json.Unmarshal([]byte(v), &rep) != nil {
		return nil
	}
	return &rep
}

// tuneAfterDeploy runs the script inside a deploy session and records it
// in the deploy log; failures never fail the deploy.
func (s *Service) tuneAfterDeploy(ctx context.Context, sess Session, h store.RemoteHost, deployID string) {
	s.logLine(ctx, deployID, "system", "tuning host (bbr, sysctl, limits, nic queues/rps, conntrack)")
	var lines []string
	err := sess.Run(ctx, TuneScript(), 90*time.Second, func(stream, line string) {
		lines = append(lines, line)
		if strings.HasPrefix(line, "TUNE|") {
			f := strings.SplitN(strings.TrimPrefix(line, "TUNE|"), "|", 4)
			if len(f) >= 3 {
				s.logLine(ctx, deployID, "system", fmt.Sprintf("tune %s: %s — %s", f[0], f[1], f[2]))
			}
		}
	})
	rep := &HostTuneReport{At: time.Now(), Items: ParseTuneOutput(lines)}
	if err != nil && len(rep.Items) == 0 {
		rep.Error = err.Error()
		s.logLine(ctx, deployID, "system", "tuning skipped: "+err.Error())
	}
	s.saveTune(ctx, h.ID, rep)
}
