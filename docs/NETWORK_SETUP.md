# ProxyGW 主路由配套配置指南 (OSPF/DNS/PBR)

为了让 ProxyGW 与您的主路由器完美配合并实现模式切换，本指南提供了 RouterOS (ROS) 和 OpenWrt 的配置示例。

---

## 1. 基础假设
- **主路由 LAN IP**: `192.168.20.1`
- **ProxyGW IP**: `192.168.20.155`
- **内网网段**: `192.168.20.0/24`

---

## 2. RouterOS (ROS) 配置

### OSPF 动态路由 (Mode B/C 必备)
1. **添加实例**: `Routing` -> `OSPF` -> `Instances` -> `+`
   - Name: `default` (ROS v7) / `default-v2` (ROS v6)
   - Router ID: `192.168.20.1`
2. **添加区域**: `Routing` -> `OSPF` -> `Areas` -> `+`
   - Name: `backbone`, ID: `0.0.0.0`
3. **添加网络/接口**: 
   - **ROS v6**: `Routing` -> `OSPF` -> `Networks` -> `+`, Network: `192.168.20.0/24`, Area: `backbone`
   - **ROS v7**: `Routing` -> `OSPF` -> `Interface Templates` -> `+`, Interfaces: `bridge-lan`, Area: `backbone`, Network Type: `broadcast`

### DNS 配置 (推荐)
- `IP` -> `DHCP Server` -> `Networks` -> 双击网段 -> **DNS Servers**: `192.168.20.155`

### Mode C 防环路 (PBR)
在 ROS v7 中执行：
```routeros
/routing table add name=bypass_proxy fib
/ip route add dst-address=0.0.0.0/0 gateway=pppoe-out1 routing-table=bypass_proxy
/routing rule add src-address=192.168.20.155/32 action=lookup-only-in-table table=bypass_proxy
```

---

## 3. OpenWrt 配置

### OSPF 动态路由 (Mode B/C 必备)
1. **安装软件**: `opkg update && opkg install frr frr-ospfd frr-vtysh`
2. **配置 FRR**: 编辑 `/etc/frr/frr.conf`
   ```text
   router ospf
    ospf router-id 192.168.20.1
    network 192.168.20.0/24 area 0.0.0.0
   ```
3. **启动**: `/etc/init.d/frr enable && /etc/init.d/frr restart`

### DNS 配置 (推荐)
- `网络` -> `接口` -> `LAN` -> `DHCP 服务器` -> `高级设置` -> `DHCP 选项`: `6,192.168.20.155`

### Mode C 防环路 (PBR)
新建脚本 `/etc/hotplug.d/iface/99-proxygw-bypass` 并赋予执行权限：
```sh
#!/bin/sh
[ "$ACTION" = "ifup" ] || exit 0
PROXY_IP="192.168.20.155/32"
TABLE="100"
WAN_GW="$(ip -4 route show default | awk 'NR==1{print $3}')"
ip -4 rule del from "$PROXY_IP" table "$TABLE" 2>/dev/null
ip -4 route replace default via "$WAN_GW" table "$TABLE"
ip -4 rule add pref 100 from "$PROXY_IP" table "$TABLE"
```

---

## 4. 排障：MTR/Ping 环路检测
如果在 Mode C 下出现 `Request timeout` 或 MTR 路径在主路由与 ProxyGW 之间无限循环：
1. 检查 ProxyGW 自身的 **默认网关** 是否指向主路由。
2. 检查主路由的 **PBR (策略路由)** 是否生效。
3. 检查 ProxyGW 的 **保护 IP 列表** (Mode A) 或 **节点地址排除逻辑** (Mode B/C) 是否包含了您的代理节点 IP。
