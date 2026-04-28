# ProxyGW Mode C 测试环境联调与内网环路修复记录

## 1. 测试拓扑（按现场）
- ProxyGW 开发机：
  - `eth0` = `192.168.20.155/24`
  - `eth1` = `192.168.100.252/24`
- ROS 路由器：
  - WAN `192.168.20.156/24`
  - LAN `192.168.100.1/24`
- 内网测试机：`192.168.100.251`
- 外网测试节点：`192.168.20.163`

## 2. Mode C 基线检查
在 `192.168.20.155` 检查到：
- ProxyGW 模式：`mode=C`（SQLite settings）
- OSPF 邻居：`192.168.100.1 Full/DR`
- 已发布静态路由（来自规则展开）：
  - `138.199.46.0/24`
  - `160.79.96.0/20`

在 ROS (`192.168.20.156`) 检查到：
- 收到 OSPF 路由：
  - `138.199.46.0/24 -> 192.168.100.252`
  - `160.79.96.0/20 -> 192.168.100.252`

## 3. 问题复现与根因
### 现象
- 从 ProxyGW 主机发往被 OSPF 引流网段（如 `138.199.46.10`）时，最初表现为不通。
- 从内网测试机访问相关目标也出现超时。

### 根因
这是典型 Mode C 旁路由回流风险：
1. ProxyGW 自身（源地址 `192.168.100.252`）发包给被引流前缀；
2. ROS 主路由命中 OSPF 路由后又把流量送回 ProxyGW；
3. 形成自回注入/环路风险（或黑洞式超时），影响稳定性。

> 核心矛盾：OSPF 引流“面向 LAN 终端”是正确的，但 ProxyGW 自身源流量必须绕过该引流策略，直出 WAN。

## 4. 修复方案（已实施）
在 ROS 增加 **源地址旁路规则**：

```routeros
/routing table add fib name=wan-bypass
/ip route add dst-address=0.0.0.0/0 gateway=192.168.20.1 routing-table=wan-bypass comment="proxygw-src-bypass"
/routing rule add src-address=192.168.100.252/32 action=lookup-only-in-table table=wan-bypass comment="proxygw-src-bypass"
```

设计说明：
- 仅对 ProxyGW 主机源地址 `192.168.100.252/32` 生效；
- 强制使用独立 `wan-bypass` 表的默认路由出 WAN，避免再命中 OSPF 回流路径；
- 对普通 LAN 客户端流量不改语义，保留 Mode C 引流行为。

## 5. 验证结果
### 5.1 路由状态
- ROS 新增路由表：`wan-bypass`
- ROS 新增规则：`src=192.168.100.252/32 -> lookup-only-in-table wan-bypass`
- ROS 新增默认路由（wan-bypass）：`0.0.0.0/0 -> 192.168.20.1`

### 5.2 行为验证
修复后从 ProxyGW 对 `138.199.46.10` 执行 traceroute：
- Hop1: `192.168.100.1`
- Hop2: `192.168.20.1`
- 后续继续向公网转发

这表明 ProxyGW 源流量已不再被 ROS 通过 OSPF 打回到 ProxyGW，环路链路被切断。

## 6. 运维建议（Mode C 必做）
1. ROS 永久保留 ProxyGW 源地址旁路规则（PBR）。
2. ProxyGW 后端继续保留“受保护端点不发布 OSPF”的防护（节点地址、SSH 端点、WG/VLESS endpoint）。
3. 每次切 Mode C 后执行三项巡检：
   - FRR 邻居 `Full`
   - ROS OSPF 路由条目存在
   - ProxyGW 源地址到已发布前缀 traceroute 不回跳至 `192.168.100.252`

## 7. 一键巡检命令（建议纳入 SOP）
### ProxyGW 主机（155）
```bash
vtysh -c 'show ip ospf neighbor'
vtysh -c 'show ip ospf route'
ip route get 138.199.46.10
traceroute -n -m 6 -w 1 138.199.46.10
```

### ROS（156）
```routeros
/ip route print detail where ospf
/routing rule print detail
/ip route print detail where routing-table="wan-bypass"
```
