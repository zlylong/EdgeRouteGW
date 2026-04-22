# OpenWrt 新手配置指南

为了让 ProxyGW 与 OpenWrt 完美配合，实现无缝的三种路由模式（特别是基于 OSPF 的旁路模式），请按照以下指南在 OpenWrt 中进行基础配置。

## 1. 基础要求
- 假设 OpenWrt 主路由的 LAN IP 为：`192.168.20.1` (请根据实际情况替换)
- 假设 ProxyGW 开发机的 IP 为：`192.168.20.155`
- 假设内网网段为：`192.168.20.0/24`

---

## 2. OSPF 动态路由配置 (Mode B / Mode C 必备)

ProxyGW 的 Mode B (Fake-IP) 和 Mode C (真实 IP 路由) 依赖 OSPF 协议向 OpenWrt 宣告目标网段，需要开启 OpenWrt 的 OSPF 功能。

### 步骤 A：安装 FRR (OSPF 守护进程)
在 OpenWrt 终端（SSH）中执行，或者在“系统”->“软件包”中安装：
```bash
opkg update
opkg install frr frr-ospfd frr-vtysh
```

### 步骤 B：配置 OSPF
编辑 `/etc/frr/frr.conf` 文件（如果不存在请创建），并写入以下基础配置：
```text
router ospf
 ospf router-id 192.168.20.1
 network 192.168.20.0/24 area 0.0.0.0
```
*(注意替换成你的实际内网网段和路由 IP)*

### 步骤 C：启用并重启服务
```bash
/etc/init.d/frr enable
/etc/init.d/frr restart
```

> **验证**: 当 ProxyGW 开启 Mode B 或 Mode C 时，在 OpenWrt 终端输入 `vtysh -c "show ip route ospf"`，或者在“网络” -> “路由”页面，应该能看到动态接收到的路由条目（例如 `198.18.0.0/16`）。

---

## 3. DNS 配置

为了防止 DNS 污染并配合 ProxyGW，你需要将内网的 DNS 解析交由 ProxyGW (内部运行的 Mosdns) 处理。

### 方案 1：通过 DHCP 下发 (推荐)
让内网设备直接向 ProxyGW 请求 DNS，减轻主路由负担。
- 登录 OpenWrt 后台 -> `网络` -> `接口` -> `LAN` -> `编辑`
- 找到底部的 `DHCP 服务器` -> `高级设置`
- 在 `DHCP 选项` 中填入：`6,192.168.20.155` （即：下发 DNS 服务器为 155）
- 保存并应用。

### 方案 2：将 OpenWrt 的上游 DNS 指向 ProxyGW
如果你希望 Dnsmasq 继续做本地缓存：
- `网络` -> `DHCP/DNS` -> `基本设置`
- **DNS 转发**: 填入 `192.168.20.155`
- 建议勾选 **忽略解析文件**。

---

## 4. 全局网关配置 (仅限 Mode A 使用)

如果你不想折腾 OSPF 旁路模式，而是想使用 **Mode A (全局 TProxy 透明网关)**，你需要将内网设备的默认网关指向 ProxyGW。

- 登录 OpenWrt 后台 -> `网络` -> `接口` -> `LAN` -> `编辑`
- `DHCP 服务器` -> `高级设置`
- 在 `DHCP 选项` 中填入：`3,192.168.20.155` （即：下发默认网关为 155）
*(注意：使用 Mode A 时，不要安装和开启 OSPF)*

---

## 5. Mode C 防环路 PBR（强烈建议）

当 Mode C 发布真实 IP 网段时，主路由若把“ProxyGW 节点目标 IP”回指给 ProxyGW，会形成回弹环路：
`ProxyGW -> OpenWrt -> ProxyGW`。

建议在 OpenWrt 配置源地址旁路：让 **ProxyGW 主机自身** 的出站流量永远走 WAN 主路。

### 临时生效（立即止血）
```bash
PROXY_IP="192.168.20.155/32"  # 改成你的 ProxyGW IP
TABLE="100"

WAN_DEV="$(ip -4 route show default | awk 'NR==1{print $5}')"
WAN_GW="$(ip -4 route show default | awk 'NR==1{print $3}')"

ip -4 rule del from "$PROXY_IP" table "$TABLE" 2>/dev/null
if [ -n "$WAN_GW" ]; then
  ip -4 route replace default via "$WAN_GW" dev "$WAN_DEV" table "$TABLE"
else
  ip -4 route replace default dev "$WAN_DEV" table "$TABLE"
fi
ip -4 rule add pref 100 from "$PROXY_IP" table "$TABLE"
ip -4 route flush cache
```

### 持久化（推荐）
新建 `/etc/hotplug.d/iface/99-proxygw-bypass`：
```sh
#!/bin/sh
[ "$ACTION" = "ifup" ] || exit 0
case "$INTERFACE" in
  wan|pppoe-wan) ;;
  *) exit 0 ;;
esac

PROXY_IP="192.168.20.155/32"   # 改成你的 ProxyGW IP
TABLE="100"

WAN_DEV="$(ip -4 route show default | awk 'NR==1{print $5}')"
WAN_GW="$(ip -4 route show default | awk 'NR==1{print $3}')"

ip -4 rule del from "$PROXY_IP" table "$TABLE" 2>/dev/null
if [ -n "$WAN_GW" ]; then
  ip -4 route replace default via "$WAN_GW" dev "$WAN_DEV" table "$TABLE"
else
  ip -4 route replace default dev "$WAN_DEV" table "$TABLE"
fi
ip -4 rule add pref 100 from "$PROXY_IP" table "$TABLE"
ip -4 route flush cache
```

启用：
```bash
chmod +x /etc/hotplug.d/iface/99-proxygw-bypass
ACTION=ifup INTERFACE=wan /etc/hotplug.d/iface/99-proxygw-bypass
```

验证：
```bash
ip -4 rule show | grep "from 192.168.20.155/32"
ip -4 route show table 100
```

---

## 6. 防火墙伪装 (Masquerade) 检查
OpenWrt 默认会自动为 WAN 口开启动态伪装。如果你遇到了无法上网的问题，可以检查：
- `网络` -> `防火墙` -> `基本设置`
- 确保 `wan` 区域的 **IP 动态伪装 (Masquerading)** 已勾选。

---
🎉 **至此，OpenWrt 端的基础配置已完成！可以享受智能分流了。**
