# OpenWrt + ProxyGW (Mode C) OSPF 环路与 `mtr` 诊断异常排障

## 背景
在 OpenWrt 作为主路由、ProxyGW 通过 OSPF 发布分流前缀时，常见两类现象会被混淆：

1. **真实环路**：ProxyGW 源流量被主路由再打回 ProxyGW（会导致回流/不通）。
2. **诊断假象**：`mtr`/`ping`（ICMP）失败，但 TCP/业务可用（常见于透明代理路径）。

本文给出可复现的判定方法与修复方案。

---

## 1) 快速判定：是否真环路

### 在 OpenWrt 检查旁路规则是否生效
```bash
ip rule show
ip route show table 100
ip route get 1.1.1.1 from <PROXYGW_LAN_IP> iif br-lan
```

期望：
- 存在 `from <PROXYGW_LAN_IP>/32 lookup 100`
- `table 100` 默认路由指向 WAN 网关
- `route get` 结果为 `via <WAN_GW> dev <WAN_IF>`，而不是回到 `br-lan -> ProxyGW`

### 在 ProxyGW 主机检查实际外联路径
```bash
traceroute -n -m 6 -q 1 1.1.1.1
mtr -n -r -c 5 -w 1.1.1.1
```

若路径为 `OpenWrt(LAN) -> WAN网关 -> 上游` 且最终可达，说明无环路。

---

## 2) 为什么 `mtr -t 1.1.1.1` 看起来“失败”

在 OpenWrt 的 mtr 中：
- `-t` 是 **curses 终端界面模式**，不是 TCP 探测。
- TCP 探测参数是 `-T`（大写）。

常见现象：
- ICMP (`mtr`/`ping`) 可能失败或中途 `???`
- TCP (`mtr -T -P 443`) 正常

这通常不是环路，而是透明代理/上游对 ICMP 或 TTL Exceeded 的处理差异导致。

---

## 3) 修复：同时旁路 ProxyGW 源流量 + OpenWrt 自身诊断流量

> 目标：
> - 防止 ProxyGW 源流量被 OSPF 回注（真环路修复）
> - 避免 OpenWrt 自身 `mtr/ping` 被 OSPF 引流到 ProxyGW 造成误判

### 推荐脚本（OpenWrt）
保存为 `/usr/bin/proxygw-src-bypass.sh`：

```sh
#!/bin/sh
set -eu

PROXYGW_IP=$(ip -4 route show 2>/dev/null | grep -m1 -oE 'via 192\.168\.100\.[0-9]+' | sed 's/^via //' || true)
WAN_GW=$(ip -4 route show default 2>/dev/null | awk 'NR==1{print $3}')
WAN_DEV=$(ip -4 route show default 2>/dev/null | awk 'NR==1{print $5}')
LAN_IP=$(ip -4 -o addr show dev br-lan 2>/dev/null | awk 'NR==1{split($4,a,"/"); print a[1]}' || true)

[ -n "$PROXYGW_IP" ] || exit 0
[ -n "$WAN_GW" ] && [ -n "$WAN_DEV" ] || exit 0

ip route replace table 100 default via "$WAN_GW" dev "$WAN_DEV"

# 100: ProxyGW source bypass (anti-loop)
while ip rule del pref 100 2>/dev/null; do true; done
ip rule add pref 100 from "$PROXYGW_IP"/32 lookup 100

# 110: OpenWrt self source bypass (diagnostic consistency)
if [ -n "$LAN_IP" ]; then
  while ip rule del pref 110 2>/dev/null; do true; done
  ip rule add pref 110 from "$LAN_IP"/32 lookup 100
fi
```

赋权并执行：
```bash
chmod +x /usr/bin/proxygw-src-bypass.sh
/usr/bin/proxygw-src-bypass.sh
```

持久化到 `/etc/rc.local`（`exit 0` 之前）：
```bash
/usr/bin/proxygw-src-bypass.sh || true
```

---

## 4) 验证命令（必须）

```bash
# 规则/路由
ip rule show
ip route show table 100

# 防环路验证：ProxyGW 源地址必须走 WAN
ip route get 8.8.8.8 from <PROXYGW_LAN_IP> iif br-lan

# 诊断一致性：OpenWrt 自身源地址也走 WAN
ip route get 1.1.1.1 from <OPENWRT_LAN_IP> iif lo

# mtr 对比
mtr -n -r -c 5 -w 1.1.1.1
mtr -n -r -c 5 -w -T -P 443 1.1.1.1
```

---

## 5) 251 客户端实测案例（已复现）

在 `192.168.100.251` 上：

- `mtr -n -r -c 10 -w -T -P 443 1.1.1.1`：2 跳直达，`0% loss`
- `mtr -n -r -c 10 -w 1.1.1.1`（ICMP）：出现 `192.168.100.1 <-> 192.168.100.204` 往返

抓包证据（ProxyGW `eth1`）显示同一 ICMP Echo 序列号被重复看到，符合 OSPF 回注循环特征（仅发生在 ICMP 诊断流）。

### 临时止血（ProxyGW 侧）

在 ProxyGW 上插入 ICMP 丢弃规则，立刻打断回注环：

```bash
nft insert rule inet proxygw prerouting ip saddr 192.168.100.0/24 ip protocol icmp counter drop comment "break_icmp_loop_tmp"
```

效果：
- TCP 代理业务不受影响（TProxy 仍处理 `tcp/udp`）
- ICMP `mtr` 不再出现 100.1/100.204 往返，而是快速终止于 `???`

> 该规则是应急止血，不是最终设计。最终仍建议在主路由（OpenWrt）侧通过策略路由或测试目标静态路由避免将诊断 ICMP 送入 OSPF 回注路径。

## 6) 运维建议

1. **ProxyGW service/LAN IP 尽量固定**（避免 DHCP 变化造成 `/32` 规则漂移）。
2. 保留主路由侧源地址旁路作为最终保险。
3. 排障时优先看 TCP 连通性（`mtr -T`, `curl -v`），不要仅凭 ICMP 结论判定环路。
4. 每次 OSPF 模式切换后固定执行：邻居 Full、规则存在、`ip route get` 双验证。
5. 若现场必须用 ICMP 压测，先在主路由配置诊断目标旁路（如 `1.1.1.1/32 -> WAN`），避免误判。