# RouterOS (ROS) 新手配置指南

为了让 ProxyGW 与 MikroTik RouterOS (ROS) 完美配合，实现无缝的三种路由模式（特别是基于 OSPF 的旁路模式），请按照以下指南在 ROS 中进行基础配置。

## 1. 基础要求
- 假设 ROS 主路由的 LAN IP 为：`192.168.20.1` (请根据实际情况替换)
- 假设 ProxyGW 开发机的 IP 为：`192.168.20.155`
- 假设内网网段为：`192.168.20.0/24`

---

## 2. OSPF 动态路由配置 (Mode B / Mode C 必备)

ProxyGW 的 Mode B (Fake-IP) 和 Mode C (真实 IP 路由) 依赖 OSPF 协议向 ROS 宣告目标网段，需要开启 ROS 的 OSPF 功能。

### 步骤 A：添加 OSPF 实例 (Instance)
在 WinBox 中依次点击：`Routing` -> `OSPF` -> `Instances` -> `+` 号
- **Name**: `default-v2` (保持默认或自定义)
- **Version**: `2`
- **Router ID**: `192.168.20.1` (建议填 ROS 的内网 IP)
*(如果是 ROS v7，可能在 `Routing` -> `OSPF` -> `Instances`)*

### 步骤 B：添加 OSPF 区域 (Area)
- `Routing` -> `OSPF` -> `Areas` -> `+` 号
- **Name**: `backbone`
- **Instance**: 选择刚才创建的 `default-v2`
- **Area ID**: `0.0.0.0`

### 步骤 C：添加 OSPF 网络 (Networks/Interfaces)
告知 OSPF 在哪个网段接收 ProxyGW 的宣告。
**(ROS v6)**: `Routing` -> `OSPF` -> `Networks` -> `+` 号
- **Network**: `192.168.20.0/24`
- **Area**: `backbone`

**(ROS v7)**: `Routing` -> `OSPF` -> `Interfaces` (或者 `Interface Templates`) -> `+` 号
- **Interfaces**: 选择你的内网桥接网卡 (如 `bridge-lan`)
- **Area**: `backbone`
- **Network Type**: `broadcast`

> **验证**: 配置完成后，当 ProxyGW 开启 Mode B 或 Mode C 时，在 ROS 的 `IP` -> `Routes` 中，你应该能看到带有 `DAo` (Dynamic, Active, OSPF) 标志的路由条目（例如 `198.18.0.0/16`）。

---

## 3. DNS 配置

为了防止 DNS 污染并配合 ProxyGW 更好地工作，你需要将内网的 DNS 解析交由 ProxyGW (内部运行的 Mosdns) 处理。

### 方案 1：通过 DHCP 下发 (推荐)
让内网设备直接向 ProxyGW 请求 DNS。
- `IP` -> `DHCP Server` -> `Networks` -> 双击你的内网网段
- **DNS Servers**: `192.168.20.155` (ProxyGW 的 IP)

### 方案 2：ROS 自身作为 DNS 缓存并转发
如果你希望 ROS 继续作为客户端的 DNS，你需要把 ROS 的上游指向 ProxyGW。
- `IP` -> `DNS` -> `Servers`: `192.168.20.155`
- 确切勾选 **Allow Remote Requests**。

---

## 4. 全局网关配置 (仅限 Mode A 使用)

如果你不打算使用 OSPF 旁路模式，而是想使用 **Mode A (全局 TProxy 透明网关)**，你需要将内网所有设备的默认网关指向 ProxyGW。

- `IP` -> `DHCP Server` -> `Networks` -> 双击你的内网网段
- **Gateway**: `192.168.20.155` (ProxyGW 的 IP)
*(注意：使用 Mode A 时，无需开启 ROS 的 OSPF 功能)*

---

## 5. Mode C 防环路 PBR（强烈建议）

当 Mode C 发布真实 IP 网段时，如果主路由把“ProxyGW 节点目标 IP”回指给 ProxyGW，会形成回弹环路：
`ProxyGW -> ROS -> ProxyGW`。

建议在 ROS 保留一条源地址旁路：让 **ProxyGW 主机自身** 的出站流量永远查 WAN 主路由表，不受 OSPF 回流影响。

### ROS v7（推荐命令）
```routeros
# 1) 新建独立旁路路由表
/routing table add name=bypass_proxy fib

# 2) 在旁路表里放一条默认路由（按你的真实 WAN 出口替换）
/ip route add dst-address=0.0.0.0/0 gateway=pppoe-out1 routing-table=bypass_proxy

# 3) 源地址策略：ProxyGW(示例 192.168.20.155) 仅查旁路表
/routing rule add src-address=192.168.20.155/32 action=lookup-only-in-table table=bypass_proxy
```

### ROS v6（等价思路）
```routeros
/ip route add dst-address=0.0.0.0/0 gateway=pppoe-out1 routing-mark=bypass_proxy
/ip firewall mangle add chain=prerouting src-address=192.168.20.155 action=mark-routing new-routing-mark=bypass_proxy passthrough=no
```

> 注意：`192.168.20.155` 请替换成你的 ProxyGW LAN IP；`pppoe-out1` 请替换成你的真实 WAN 出口。

### 验证
```routeros
/routing rule print detail where src-address=192.168.20.155/32
/ip route print detail where routing-table=bypass_proxy
```

若验证通过，ProxyGW 发往代理节点的流量不会再被 OSPF 回指，能稳定避免回弹环路。

---

## 6. 防火墙伪装 (Masquerade) 检查
确保你的 ROS 已经正确配置了上网的源地址转换 (NAT)，通常默认已经存在：
- `IP` -> `Firewall` -> `NAT` -> `+` 号
- **Chain**: `srcnat`
- **Out. Interface**: 选择你的外网拨号网卡 (例如 `pppoe-out1`)
- **Action**: `masquerade`

---
🎉 **至此，ROS 端的基础配置已全部完成，快去 ProxyGW 管理面板体验丝滑的模式切换吧！**
