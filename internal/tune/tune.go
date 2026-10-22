// Package tune inspects the host and applies the performance settings that
// depend on what it finds: congestion control, NIC queues or RPS, the
// WireGuard module, conntrack sizing. Plain sysctl values are shipped in
// /etc/sysctl.d and only verified here. Every item reports what was
// detected, what was done and why, for the UI, doctor and the log.
package tune

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/zlylong/EdgeRouteGW/internal/driver/execx"
)

// Status of one item.
const (
	StatusOK          = "ok"          // in effect as shipped
	StatusApplied     = "applied"     // changed by this run
	StatusFallback    = "fallback"    // wanted setting unavailable, alternative in effect
	StatusUnavailable = "unavailable" // nothing could be done; hint says what the operator can do
	StatusInfo        = "info"        // informational
)

// Item is one line of the report.
type Item struct {
	Key    string `json:"key"`
	Title  string `json:"title"`
	Status string `json:"status"`
	Detail string `json:"detail"`
	Hint   string `json:"hint,omitempty"`
}

// Report is the result of one inspection/apply run.
type Report struct {
	At      time.Time `json:"at"`
	Applied bool      `json:"applied"` // false for a dry inspection
	Host    HostInfo  `json:"host"`
	Items   []Item    `json:"items"`
}

// HostInfo is the context the decisions were made in.
type HostInfo struct {
	CPUs   int    `json:"cpus"`
	Kernel string `json:"kernel"`
	Virt   string `json:"virt"` // systemd-detect-virt output or "none"
	Iface  string `json:"iface"`
}

// Tuner holds the last report.
type Tuner struct {
	Exec       execx.Runner
	SysctlFile string
	// Iface resolves the interface to tune (service interface or default route).
	Iface func() string

	mu   sync.Mutex
	last *Report
}

// New returns a tuner.
func New(run execx.Runner, iface func() string) *Tuner {
	return &Tuner{Exec: run, SysctlFile: "/etc/sysctl.d/99-edgeroutegw.conf", Iface: iface}
}

// Last returns the most recent report (nil before the first run).
func (t *Tuner) Last() *Report {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.last
}

// Run inspects and, when apply is true, changes what it can. It never
// fails as a whole: each item records its own outcome.
func (t *Tuner) Run(ctx context.Context, apply bool) *Report {
	r := &Report{At: time.Now(), Applied: apply, Host: t.hostInfo(ctx)}
	r.Items = append(r.Items, t.sysctlItems()...)
	r.Items = append(r.Items, t.congestion(ctx, apply))
	r.Items = append(r.Items, t.nic(ctx, r.Host, apply)...)
	r.Items = append(r.Items, t.wireguard())
	r.Items = append(r.Items, t.conntrack(apply))
	t.mu.Lock()
	t.last = r
	t.mu.Unlock()
	return r
}

func (t *Tuner) hostInfo(ctx context.Context) HostInfo {
	h := HostInfo{CPUs: runtime.NumCPU(), Virt: "none"}
	if b, err := os.ReadFile("/proc/sys/kernel/osrelease"); err == nil {
		h.Kernel = strings.TrimSpace(string(b))
	}
	if res, err := t.Exec.Run(ctx, "systemd-detect-virt"); err == nil || len(res.Output) > 0 {
		if v := strings.TrimSpace(string(res.Output)); v != "" {
			h.Virt = v
		}
	}
	if t.Iface != nil {
		h.Iface = t.Iface()
	}
	return h
}

// --- sysctl verification -------------------------------------------------

func (t *Tuner) sysctlItems() []Item {
	b, err := os.ReadFile(t.SysctlFile)
	if err != nil {
		return []Item{{Key: "sysctl", Title: "内核参数文件", Status: StatusUnavailable, Detail: err.Error()}}
	}
	var wrong []string
	total := 0
	for _, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		k, v = strings.TrimSpace(k), strings.TrimSpace(v)
		if strings.HasPrefix(k, "net.netfilter.") || k == "net.ipv4.tcp_congestion_control" || k == "net.core.default_qdisc" {
			continue // reported by their own items
		}
		total++
		cur, err := os.ReadFile("/proc/sys/" + strings.ReplaceAll(k, ".", "/"))
		if err != nil {
			wrong = append(wrong, k+" (missing)")
			continue
		}
		if normalize(string(cur)) != normalize(v) {
			wrong = append(wrong, fmt.Sprintf("%s=%s (want %s)", k, normalize(string(cur)), normalize(v)))
		}
	}
	if len(wrong) == 0 {
		return []Item{{Key: "sysctl", Title: "内核参数(转发、端口、缓冲区、fastopen 等)", Status: StatusOK, Detail: fmt.Sprintf("%d 项全部生效", total)}}
	}
	return []Item{{Key: "sysctl", Title: "内核参数", Status: StatusUnavailable, Detail: fmt.Sprintf("%d/%d 项与文件不符:%s", len(wrong), total, strings.Join(wrong, "; ")), Hint: "sysctl -p /etc/sysctl.d/99-edgeroutegw.conf;另一份 sysctl 文件可能在之后覆盖了它"}}
}

func normalize(s string) string { return strings.Join(strings.Fields(s), " ") }

// --- congestion control --------------------------------------------------

func (t *Tuner) congestion(ctx context.Context, apply bool) Item {
	it := Item{Key: "bbr", Title: "拥塞控制 BBR + fq_codel"}
	avail, _ := os.ReadFile("/proc/sys/net/ipv4/tcp_available_congestion_control")
	cur, _ := os.ReadFile("/proc/sys/net/ipv4/tcp_congestion_control")
	qdisc, _ := os.ReadFile("/proc/sys/net/core/default_qdisc")
	c, q := strings.TrimSpace(string(cur)), strings.TrimSpace(string(qdisc))
	// Fields, not a substring match: bbr is usually the last entry and sits
	// before the trailing newline.
	hasBBR := slices.Contains(strings.Fields(string(avail)), "bbr")
	if !hasBBR && apply && modulesVisible() {
		// Under ProtectKernelModules /lib/modules is hidden and modprobe can
		// only fail; the package loads tcp_bbr via modules-load.d instead.
		if _, err := t.Exec.Run(ctx, "modprobe", "tcp_bbr"); err == nil {
			avail, _ = os.ReadFile("/proc/sys/net/ipv4/tcp_available_congestion_control")
			hasBBR = slices.Contains(strings.Fields(string(avail)), "bbr")
		}
	}
	switch {
	case c == "bbr" && q == "fq_codel":
		it.Status, it.Detail = StatusOK, "bbr / fq_codel 生效"
	case hasBBR && apply:
		if err := os.WriteFile("/proc/sys/net/ipv4/tcp_congestion_control", []byte("bbr\n"), 0o644); err == nil {
			_ = os.WriteFile("/proc/sys/net/core/default_qdisc", []byte("fq_codel\n"), 0o644)
			it.Status, it.Detail = StatusApplied, fmt.Sprintf("由 %s 切换为 bbr", c)
		} else {
			it.Status, it.Detail = StatusUnavailable, err.Error()
		}
	case hasBBR:
		it.Status, it.Detail = StatusUnavailable, fmt.Sprintf("当前 %s,bbr 可用但未启用", c)
	default:
		it.Status, it.Detail = StatusFallback, fmt.Sprintf("内核没有 tcp_bbr 模块,使用 %s / %s", c, q)
		it.Hint = "需要带 tcp_bbr 的内核(Debian/Ubuntu 标准内核都有);云厂商精简内核可能没有"
	}
	return it
}

// --- NIC queues / RPS ----------------------------------------------------

// Channels is what ethtool -l reports.
type Channels struct {
	MaxCombined, CurCombined int
	MaxRX, CurRX             int
}

// ParseEthtoolChannels parses "ethtool -l" output.
func ParseEthtoolChannels(out string) (Channels, error) {
	var c Channels
	section := ""
	seen := false
	for _, line := range strings.Split(out, "\n") {
		l := strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(l, "Pre-set maximums"):
			section = "max"
		case strings.HasPrefix(l, "Current hardware settings"):
			section = "cur"
		case strings.HasPrefix(l, "Combined:") || strings.HasPrefix(l, "RX:"):
			k, v, _ := strings.Cut(l, ":")
			n, err := strconv.Atoi(strings.TrimSpace(v))
			if err != nil {
				continue // n/a
			}
			seen = true
			switch {
			case k == "Combined" && section == "max":
				c.MaxCombined = n
			case k == "Combined" && section == "cur":
				c.CurCombined = n
			case k == "RX" && section == "max":
				c.MaxRX = n
			case k == "RX" && section == "cur":
				c.CurRX = n
			}
		}
	}
	if !seen {
		return c, fmt.Errorf("no channel information")
	}
	return c, nil
}

// CPUMask renders a hex CPU bitmask for n CPUs ("f" for 4, "ffffffff,ffffffff" style for >32).
func CPUMask(n int) string {
	if n <= 0 {
		return "0"
	}
	words := (n + 31) / 32
	parts := make([]string, words)
	for i := 0; i < words; i++ {
		bits := n - i*32
		if bits > 32 {
			bits = 32
		}
		v := uint64(1)<<uint(bits) - 1
		parts[words-1-i] = fmt.Sprintf("%x", v)
	}
	return strings.Join(parts, ",")
}

func (t *Tuner) nic(ctx context.Context, h HostInfo, apply bool) []Item {
	iface := h.Iface
	if iface == "" {
		return []Item{{Key: "nic", Title: "网卡队列", Status: StatusUnavailable, Detail: "未能确定服务网卡"}}
	}
	driver := "?"
	if p, err := os.Readlink("/sys/class/net/" + iface + "/device/driver"); err == nil {
		driver = filepath.Base(p)
	}
	rxQueues, _ := filepath.Glob("/sys/class/net/" + iface + "/queues/rx-*")
	nq := len(rxQueues)
	var items []Item
	qi := Item{Key: "nic-queues", Title: fmt.Sprintf("网卡 %s(%s)多队列", iface, driver)}
	res, err := t.Exec.Run(ctx, "ethtool", "-l", iface)
	ch, perr := Channels{}, fmt.Errorf("ethtool unavailable")
	if err == nil {
		ch, perr = ParseEthtoolChannels(string(res.Output))
	}
	target := h.CPUs
	switch {
	case perr != nil:
		qi.Status, qi.Detail = StatusInfo, fmt.Sprintf("%d 个接收队列;ethtool 未提供队列信息(%v)", nq, perr)
	case ch.MaxCombined <= 1 && ch.MaxRX <= 1:
		qi.Status = StatusUnavailable
		qi.Detail = fmt.Sprintf("硬件/宿主机只提供 1 个队列,CPU 有 %d 个", h.CPUs)
		if h.Virt != "none" {
			qi.Hint = "虚拟机:在宿主机给网卡开多队列后重启生效(Proxmox: qm set <vmid> -net0 ...,queues=" + strconv.Itoa(h.CPUs) + ");在此之前用 RPS 分散软中断"
		} else {
			qi.Hint = "该网卡不支持多队列;用 RPS 分散软中断"
		}
	default:
		max := ch.MaxCombined
		cur := ch.CurCombined
		if max == 0 {
			max, cur = ch.MaxRX, ch.CurRX
		}
		if target > max {
			target = max
		}
		if cur >= target {
			qi.Status, qi.Detail = StatusOK, fmt.Sprintf("%d/%d 个队列已启用(CPU %d 个)", cur, max, h.CPUs)
		} else if apply {
			if _, err := t.Exec.Run(ctx, "ethtool", "-L", iface, "combined", strconv.Itoa(target)); err == nil {
				qi.Status, qi.Detail = StatusApplied, fmt.Sprintf("队列由 %d 提升到 %d(上限 %d,CPU %d 个)", cur, target, max, h.CPUs)
				nq = target
			} else {
				qi.Status, qi.Detail = StatusUnavailable, fmt.Sprintf("ethtool -L 失败:%v", err)
			}
		} else {
			qi.Status, qi.Detail = StatusInfo, fmt.Sprintf("可由 %d 提升到 %d 个队列", cur, target)
		}
	}
	items = append(items, qi)

	// RPS: software steering when queues < CPUs.
	ri := Item{Key: "rps", Title: "RPS 软中断分散"}
	if nq >= h.CPUs {
		ri.Status, ri.Detail = StatusInfo, "队列数已不少于 CPU 数,无需 RPS"
		return append(items, ri)
	}
	mask := CPUMask(h.CPUs)
	want := true
	for _, q := range rxQueues {
		cur, _ := os.ReadFile(filepath.Join(q, "rps_cpus"))
		if normalizeMask(string(cur)) != mask {
			want = false
		}
	}
	switch {
	case want:
		ri.Status, ri.Detail = StatusOK, fmt.Sprintf("%d 个接收队列的软中断分散到 %d 个 CPU", len(rxQueues), h.CPUs)
	case apply:
		failed := 0
		for _, q := range rxQueues {
			if err := os.WriteFile(filepath.Join(q, "rps_cpus"), []byte(mask+"\n"), 0o644); err != nil {
				failed++
				continue
			}
			_ = os.WriteFile(filepath.Join(q, "rps_flow_cnt"), []byte("4096\n"), 0o644)
		}
		_ = os.WriteFile("/proc/sys/net/core/rps_sock_flow_entries", []byte("32768\n"), 0o644)
		if failed == 0 {
			ri.Status, ri.Detail = StatusApplied, fmt.Sprintf("已把 %d 个接收队列的软中断分散到 %d 个 CPU(掩码 %s)", len(rxQueues), h.CPUs, mask)
		} else {
			ri.Status, ri.Detail = StatusUnavailable, fmt.Sprintf("%d 个队列写入失败", failed)
		}
	default:
		ri.Status, ri.Detail = StatusInfo, "可启用(当前关闭)"
	}
	return append(items, ri)
}

func normalizeMask(s string) string {
	s = strings.TrimSpace(strings.ToLower(s))
	parts := strings.Split(s, ",")
	for i, p := range parts {
		p = strings.TrimLeft(p, "0")
		if p == "" {
			p = "0"
		}
		parts[i] = p
	}
	// drop leading all-zero words
	for len(parts) > 1 && parts[0] == "0" {
		parts = parts[1:]
	}
	return strings.Join(parts, ",")
}

// --- WireGuard module ----------------------------------------------------

func (t *Tuner) wireguard() Item {
	it := Item{Key: "wireguard", Title: "内核 WireGuard"}
	if _, err := os.Stat("/sys/module/wireguard"); err == nil {
		it.Status, it.Detail = StatusOK, "模块已加载,WireGuard 节点走内核隧道"
	} else {
		it.Status, it.Detail = StatusFallback, "模块未加载,WireGuard 节点由 Xray 用户态实现(吞吐约为内核的一半以下)"
		it.Hint = "modprobe wireguard;包已写入 /etc/modules-load.d/edgeroutegw.conf,重启后自动加载"
	}
	return it
}

// --- conntrack ----------------------------------------------------------

var conntrackWant = map[string]string{
	"nf_conntrack_max":                     "1048576",
	"nf_conntrack_tcp_timeout_established": "3600",
	"nf_conntrack_tcp_timeout_time_wait":   "120",
	"nf_conntrack_tcp_timeout_close_wait":  "60",
	"nf_conntrack_tcp_timeout_fin_wait":    "120",
}

func (t *Tuner) conntrack(apply bool) Item {
	it := Item{Key: "conntrack", Title: "conntrack 表容量"}
	if _, err := os.Stat("/proc/sys/net/netfilter/nf_conntrack_max"); err != nil {
		it.Status, it.Detail = StatusInfo, "模块未加载;TPROXY 规则不使用 conntrack,无需设置"
		return it
	}
	keys := make([]string, 0, len(conntrackWant))
	for k := range conntrackWant {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	changed, wrong := 0, 0
	for _, k := range keys {
		p := "/proc/sys/net/netfilter/" + k
		cur, _ := os.ReadFile(p)
		if strings.TrimSpace(string(cur)) == conntrackWant[k] {
			continue
		}
		if apply && os.WriteFile(p, []byte(conntrackWant[k]+"\n"), 0o644) == nil {
			changed++
		} else {
			wrong++
		}
	}
	switch {
	case wrong > 0:
		it.Status, it.Detail = StatusUnavailable, fmt.Sprintf("%d 项未能设置", wrong)
	case changed > 0:
		it.Status, it.Detail = StatusApplied, fmt.Sprintf("已设置 %d 项(max=1048576,established=3600s)", changed)
	default:
		it.Status, it.Detail = StatusOK, "max=1048576,超时已按网关用途设置"
	}
	return it
}

// modulesVisible reports whether the running kernel's module tree can be
// read by this process (false inside a ProtectKernelModules sandbox).
func modulesVisible() bool {
	var u syscall.Utsname
	if err := syscall.Uname(&u); err != nil {
		return false
	}
	var b []byte
	for _, c := range u.Release {
		if c == 0 {
			break
		}
		b = append(b, byte(c))
	}
	_, err := os.Stat("/lib/modules/" + string(b))
	return err == nil
}
