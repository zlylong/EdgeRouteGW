# 主路由配套配置指南(OSPF / DNS / 防环路)

Mode A 不需要动主路由。Mode B 和 Mode C 需要主路由和网关建立 OSPF 邻居,从网关学到"该引过来的流量":本文给出 RouterOS 7 和 OpenWrt(FRR)的配置,以及验证与排障方法。

## 1. 基础假设

- 主路由 LAN 地址 `192.168.20.1`,内网 `192.168.20.0/24`
- 网关 EdgeRouteGW 地址 `192.168.20.155`,默认网关指向 `192.168.20.1`
- 网关的「网络与模式 → 网卡与参数」里,**业务网卡(service_iface)** 选的是接 LAN 的那块(单网卡就是它本身);OSPF 的 network 语句按这块网卡的地址生成,不填时用默认路由所在网卡兜底。

## 2. Mode A:只改设备

把设备的默认网关和 DNS 都指向 `192.168.20.155`。最省事的做法是在主路由的 DHCP 里改:

- RouterOS:`IP → DHCP Server → Networks` 里把网段的 **Gateway** 和 **DNS Servers** 都改成 `192.168.20.155`。
- OpenWrt:`网络 → 接口 → LAN → DHCP 服务器 → 高级设置 → DHCP 选项` 填 `3,192.168.20.155` 和 `6,192.168.20.155`。

网关自身仍走主路由出网。网关上的「分流 → 绕过目标」填上管理入口、堡垒机等绝不能进代理的目的 IP,防止规则改错把自己切断。

## 3. RouterOS 7

### OSPF(Mode B / C 必备)

```routeros
/routing ospf instance add name=ospf-1 version=2 router-id=192.168.20.1
/routing ospf area add name=backbone area-id=0.0.0.0 instance=ospf-1
/routing ospf interface-template add area=backbone interfaces=bridge-lan networks=192.168.20.0/24 type=broadcast
```

`interfaces` 填接 LAN 的桥或口。网关侧 FRR 的 router-id 是业务网卡地址,区域固定 backbone,网络类型 broadcast。切到 B / C 后,`/routing ospf neighbor print` 应看到网关处于 `Full`,`/ip route print where dynamic` 能看到学到的前缀:Mode B 是 `198.18.0.0/16`(Fake-IP 池),Mode C 是一批 `/32`、`/24` 等真实前缀。

### DNS(推荐)

`IP → DHCP Server → Networks` 把 **DNS Servers** 改成 `192.168.20.155`。Mode B 必须这样做,否则设备拿不到 Fake-IP,流量不会被引过来;Mode C 可选,但统一走网关解析能让分流和 DNS 保持一致,还能用网关的防泄露和 ECS。

### Mode C 防环路(可选,建议)

2.0 在宣告路由时会自动把所有节点和托管主机的 `/32` 排除掉(受保护前缀),网关去节点的流量不会被主路由再引回来,一般不会成环。如果你还有别的规则会把网关自己的出站流量拉回来,或者想彻底隔离,给网关的源地址单独一张干净的路由表:

```routeros
/routing table add name=bypass_proxy fib
/ip route add dst-address=0.0.0.0/0 gateway=pppoe-out1 routing-table=bypass_proxy
/routing rule add src-address=192.168.20.155/32 action=lookup-only-in-table table=bypass_proxy
```

`pppoe-out1` 换成你的真实公网出口。

## 4. OpenWrt(FRR)

### OSPF

```sh
opkg update && opkg install frr frr-ospfd frr-vtysh
```

`/etc/frr/frr.conf`:

```text
router ospf
 ospf router-id 192.168.20.1
 network 192.168.20.0/24 area 0.0.0.0
```

```sh
/etc/init.d/frr enable && /etc/init.d/frr restart
vtysh -c "show ip ospf neighbor"
```

### DNS

`网络 → 接口 → LAN → DHCP 服务器 → 高级设置 → DHCP 选项`:`6,192.168.20.155`。

### Mode C 防环路(可选)

`/etc/hotplug.d/iface/99-edgeroutegw-bypass`(加执行权限):

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

## 5. 网关侧:切换模式后看什么

「网络与模式 → OSPF 状态」显示 FRR 邻居和已宣告的路由数。命令行:

```bash
vtysh -c "show ip ospf neighbor"          # 邻居应为 Full
ip route show table 200 | head            # Mode C 宣告的真实前缀(proto 200)
ip rule                                   # 应只有一条 fwmark 0x1 lookup 100,WireGuard 节点各一条 0x1xx lookup 3xx
nft list table inet edgeroutegw | head -60
```

Mode B / C 下「分流 → 设备策略」和「绕过目标」不会下发:流量是主路由引过来的,在网关上绕过会形成环路。

## 6. OSPF 宣告参数(网络与模式 → 网卡与参数)

| 参数 | 含义 | 建议 |
|---|---|---|
| 每批路由数 | 一次向内核/FRR 写多少条 | 500(规则多时主路由 CPU 弱可调小) |
| 批间隔(秒) | 两批之间的停顿 | 2 |
| 解析并发 | Mode C 解析域名规则的并发数 | 8~16 |
| 宣告白名单 | 非空时只宣告这些前缀内的路由 | 需要严格控制时才填 |

私网、CGNAT、`0.0.0.0/8`、回环、链路本地、组播以及所有节点 / 托管主机的 `/32` 永远不会被宣告;被投毒的解析结果因此进不了主路由。

## 7. 排障

| 现象 | 看什么 |
|---|---|
| 主路由没有邻居 | 两边的 OSPF 区域都是 `0.0.0.0`、网络类型都是 broadcast;网关上 `systemctl is-active frr`(Mode A 下 FRR 本来就是停的);业务网卡选错会让 FRR 没有 network 语句 |
| 邻居 Full 但主路由没学到路由 | Mode B 看「网络与模式」里 Fake-IP 池是否 `198.18.0.0/16`;Mode C 看「分流」里是否有域名 / geosite 规则且 GeoData 已安装,「OSPF 状态」的已宣告数 |
| Mode C 下某些站极慢或超时,MTR 在主路由和网关之间来回 | 环路:确认节点地址没有被别的静态路由指回网关;加第 3 / 4 节的源地址 PBR |
| 切回 Mode A 后主路由还残留路由 | 网关会撤销宣告并清空 table 200;主路由侧 `/ip route print where dynamic` 应几秒内消失,否则重启主路由的 OSPF 实例 |
| 设备在 Mode B 下不走代理 | 设备用的不是网关 DNS,拿不到 Fake-IP;`nslookup <代理域名> 192.168.20.155` 应返回 `198.18.x.x` |
