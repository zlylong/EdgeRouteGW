# 系统运维与故障排查手册

本文档面向 EdgeRouteGW 的系统管理员，提供服务的日常维护、平滑升级、数据备份以及故障排除的指导原则。

## 🔧 自动化生命周期脚本

项目在 `scripts/` 目录下为您准备了全生命周期的自动化部署与维护脚本：

- **📦 初始安装/重置环境**：
  ```bash
  bash <(curl -s -4 -L https://raw.githubusercontent.com/zlylong/EdgeRouteGW/main/scripts/install.sh)
  ```
  用于全新环境安装，或彻底修复系统底层依赖与内核环境。此操作会覆盖 Systemd 文件并重新注入 TProxy 路由规则。

- **🔄 一键平滑升级**：
  ```bash
  bash <(curl -s -4 -L https://raw.githubusercontent.com/zlylong/EdgeRouteGW/main/scripts/update.sh)
  ```
  推荐的日常维护命令。它会：补装依赖、`git fetch`、下载并校验当前架构的最新 Release 后端二进制（下载/校验失败即中止，工作树不动）、`git reset --hard` 到 `main` 最新提交（`config/aes.key` 会被保护）、若仓库跟踪的 amd64 `core/xray/xray`、`core/mosdns/mosdns` 在本机不可运行则重新下载对应架构的版本、重写 proxygw/mosdns/xray 三个 systemd 单元文件（**不**重启 mosdns/xray）、清空 `domain_resolve_cache` / `routes_table` / `geosite_expand_cache` 三张缓存表，最后只重启 `proxygw`。检测到本机 10809/10808 端口的代理时，Release 下载走该代理（git 传输不走）。

  > 安全策略：脚本必须拿到发布页的 `SHA256SUMS` 并校验通过才会安装二进制；获取失败即中止（fail-closed）。安装早于校验文件的旧版本可显式设置 `PROXYGW_ALLOW_UNVERIFIED=1`（只影响后端二进制的校验）。`update.sh` 会把旧二进制保留为 `proxygw-backend.prev`，重启后连续观察 10 秒，期间只要 `systemctl is-active` 失败一次或单元发生过自动重启（`NRestarts` 非 0）即判定失败，自动回滚到旧二进制并重启；10 秒之后才崩溃的情况仍需人工处理（`cp backend/proxygw-backend.prev backend/proxygw-backend && systemctl restart proxygw`）。回滚只覆盖二进制，不回退 git 树与前端。不再内置固定的回退版本号：无法从 GitHub API 或本地 git 标签确定版本时脚本直接报错退出。
  >
  > 升级后的两个副作用：仓库跟踪的 `core/mosdns/proxy_domains.txt` 会被 `git reset` 还原，因此后端首次启动时会检测到差异并重启一次 mosdns；仓库同样跟踪 amd64 的 `core/xray/xray` 与 `core/mosdns/mosdns`，`git reset` 会把它们还原；脚本随后检查两者能否运行，不能则重新下载本机架构的版本（arm64 主机），amd64 主机上则意味着通过 UI 升级到的 Xray/Mosdns 版本会回到仓库提交的版本。浏览器侧 `/ui/libs/*` 缓存 1 小时，升级后请强制刷新页面。

  > 自 `v1.6.16+` 起，`install.sh` / `update.sh` 在服务启动后会自动执行一次数据库低风险优化（`scripts/db_optimize.sh --index-only`）：
  > - 幂等创建关键索引（`domain_geoip_lock` / `gateway_events`）
  > - 执行 `ANALYZE` 与 `PRAGMA optimize`
  > - 不执行 `VACUUM`（避免在安装/升级流程引入长时间写锁）

- **🗑️ 彻底卸载系统**：
  ```bash
  bash scripts/uninstall.sh
  ```
  先停用 proxygw 并清除 TProxy 的 nft 表、策略路由（含 IPv6）与 `rt_tables.d/proxygw.conf`，再停用并禁用 mosdns/xray/frr，删除对应的 systemd 单元、`proxygw.service.d` drop-in 与 `/etc/frr/frr.conf`。`/etc/nftables.conf` 与 `/etc/resolv.conf` 若存在安装时的备份（`*.pre-proxygw`）则原样还原；否则前者被删除并禁用 `nftables.service`，后者在启用了 systemd-resolved 的主机上重新指向 stub 解析器，其它主机保留安装时写入的公共 DNS。`ip_forward`、BBR 等 sysctl 运行时值到重启才恢复。脚本只询问一次：是否删除整个 `/root/proxygw`（含配置与 SQLite 数据库）；非交互执行时默认保留。

## 🔩 运行时环境变量

`install.sh` / `update.sh` 每次都会重写 `/etc/systemd/system/proxygw.service`，直接改它的设置会在下次升级时丢失。请使用 drop-in：

```bash
systemctl edit proxygw        # 生成 /etc/systemd/system/proxygw.service.d/override.conf
# 写入：
# [Service]
# Environment=PROXYGW_LISTEN_ADDR=192.168.1.2:80
systemctl daemon-reload && systemctl restart proxygw
```

后端进程读取的变量：

| 变量 | 作用 |
| :--- | :--- |
| `PROXYGW_HOME` | 安装根目录，默认 `/root/proxygw`。脚本与单元文件均硬编码该路径，改动仅适合开发/测试；`go test` 必须设置它以免误写生产文件。 |
| `PROXYGW_LISTEN_ADDR` | 管理界面监听地址，默认 `:80`。 |
| `PROXYGW_BOOTSTRAP_PASSWORD` | 首次初始化管理员密码时使用该值而不是随机生成（此时不写 `bootstrap_password.txt`）。 |
| `PROXYGW_CMD_LOG=1` | 记录每条外部命令的开始/结束（默认只记失败与 ≥2s 的慢命令）。启动时读取一次。 |
| `PROXYGW_FORCE_RESTART_ON_BOOT=1` | 后端启动时无条件重启 Xray（默认：渲染配置与磁盘一致且服务运行中则跳过）。mosdns 不受此变量影响，只在配置变化、服务未运行或 Mode B 的 FakeIP 刷新（5s 内去重）时重启。 |
| `PROXYGW_E2E_TOKEN=1` | 让固定 token `e2e-token` 通过鉴权，仅供端到端测试。**生产环境绝对不能设置。** |
| `GIN_MODE` | 显式指定 gin 模式（默认 release）。 |

时区被程序固定为 `Asia/Shanghai`（`TZ` 环境变量会被覆盖）。

脚本与工具读取的变量：

| 变量 | 作用 |
| :--- | :--- |
| `PROXYGW_ALLOW_UNVERIFIED=1` | `install.sh` / `update.sh` 在取不到 `SHA256SUMS` 时仍安装后端二进制。 |
| `PROXYGW_RESTART_AFTER_BUILD=1` | `scripts/build.sh` 构建后重启 proxygw。 |
| `GOPROXY` | `scripts/build.sh` 使用的 Go 模块代理，默认 `https://goproxy.cn,direct`。 |
| `DB_OPTIMIZE_BACKUP_KEEP` | `scripts/db_optimize.sh` 保留的备份份数，默认 3。 |
| `TAILWIND_VERSION` | `scripts/build_frontend_css.sh` 下载的 Tailwind CLI 版本，默认 3.4.17。 |
| `PW_CHROMIUM_EXECUTABLE` / `PLAYWRIGHT_BROWSERS_PATH` / `PLAYWRIGHT_PORT` / `PLAYWRIGHT_BASE_URL` | e2e 测试使用的浏览器与服务地址。 |

`xray.service` 通过 `Environment=XRAY_LOCATION_ASSET=/root/proxygw/core/xray` 指定 geodata 目录。

## ⚙️ 系统服务状态管理

EdgeRouteGW 基于标准的 Systemd 协同工作，日常系统诊断可使用标准命令：

```bash
# 查看所有关联服务的当前运行状态
systemctl status proxygw mosdns xray frr nftables --no-pager

# 诊断后端服务（UI报错、API异常、数据库锁）
journalctl -u proxygw -n 100 --no-pager -f

# 诊断 Mosdns（DNS解析异常、假死）
journalctl -u mosdns -n 100 --no-pager -f

# 诊断 Xray（代理不通、节点连接拒绝）
journalctl -u xray -n 100 --no-pager -f
```

## 💾 数据备份与密码重置

系统所有持久化状态（节点配置、规则策略、管理员信息）都保存在本地：

### 数据库优化（SQLite）

当出现以下任一信号时，建议执行数据库优化：
- 升级后查询响应变慢（尤其事件查询、GeoIP 锁查询）
- `proxygw.db` 体积异常膨胀
- `PRAGMA freelist_count` 占比明显偏高

**低风险在线优化（推荐日常）**：
```bash
/root/proxygw/scripts/db_optimize.sh /root/proxygw/config/proxygw.db --index-only
```

**完整压缩优化（维护窗口执行）**：
```bash
/root/proxygw/scripts/db_optimize.sh /root/proxygw/config/proxygw.db --full
```

说明：
- `--index-only`：仅建索引 + `ANALYZE` + `PRAGMA optimize`，不做 `VACUUM`
- `--full`：包含 `VACUUM`，会持有写锁，建议低峰执行
- 脚本用 `sqlite3 .backup` 在线生成时间戳备份 `proxygw.db.bak.YYYYmmdd_HHMMSS`，默认只保留最近 3 份（`DB_OPTIMIZE_BACKUP_KEEP`）

**数据备份**：
数据库以 WAL 模式运行且后端持续写入，直接 `cp`/`tar` 主文件可能丢掉 `-wal` 里的最新事务。请用在线备份，或先停服务：
```bash
# 在线一致性备份数据库
sqlite3 /root/proxygw/config/proxygw.db ".backup /root/backup/proxygw_$(date +%F).db"
# 同时备份密钥与配置（aes.key 是解密所有已存 SSH 凭证的唯一密钥，务必保密保存）
tar -czvf /root/backup/proxygw_config_$(date +%F).tar.gz -C /root/proxygw config/aes.key config/bootstrap_password.txt 2>/dev/null
```
其它可回退文件：`backend/proxygw-backend.prev`（上一版后端）、`core/xray/xray.bak`、`core/mosdns/mosdns.bak`（应用内更新前的二进制）。

**恢复**：
```bash
systemctl stop proxygw
cp /root/backup/proxygw_YYYY-MM-DD.db /root/proxygw/config/proxygw.db
rm -f /root/proxygw/config/proxygw.db-wal /root/proxygw/config/proxygw.db-shm
tar -xzf /root/backup/proxygw_config_YYYY-MM-DD.tar.gz -C /root/proxygw
systemctl start proxygw
```
没有对应的 `aes.key` 时，远程节点的 SSH 凭证无法解密，需要重新录入。

### 紧急密码重置
管理员密码以 Bcrypt 哈希保存在 `settings` 表。如果遗忘且无法登入 UI，删除哈希后重启，后端会重新生成一次性初始密码：
```bash
sqlite3 /root/proxygw/config/proxygw.db "DELETE FROM settings WHERE key IN ('password_hash','password');"
systemctl restart proxygw
cat /root/proxygw/config/bootstrap_password.txt
```
也可以在重启前通过 drop-in 设置 `PROXYGW_BOOTSTRAP_PASSWORD=<新密码>`，此时不会写 txt 文件（用完记得移除该变量）。

## 📦 数据库存储与定期清理规则

EdgeRouteGW 后端自 `v1.7.5+` 起内置了自动化的数据库维护任务（`db_maintenance.go`），每 24 小时执行一次清理。以下是当前执行的数据留存策略：

| 数据类别 | 留存期限 / 清理规则 | 说明 |
| :--- | :--- | :--- |
| **API 审计日志** | 7 天 | `module='api'` 的事件，包含频繁的配置下发与心跳。 |
| **系统事件日志** | 30 天 | 除 API 外的其他模块（OSPF, DNS, Nodes）日志。 |
| **流量统计历史** | 60 天 | 包含总流量与单节点流量的分时历史数据。 |
| **远程节点日志** | 30 天 | 远程节点的部署、检查与状态变更日志（UI 详情弹窗“部署与检查日志”读取此表）。 |
| **远程节点历史参数** | 30 天 | 重新生成参数前归档的旧版本，决定了回退能追溯多久。 |
| **DNS 解析缓存** | 即时清理 | `expire_at`（Unix 时间戳）已过期的解析条目。此前的比较把整数与文本混比，导致每天清空整表，已修复。 |
| **其他中间缓存** | 30 天 | 包含 Geosite 展开缓存、GeoIP 自动锁定记录等。 |

**维护操作细节：**
- **每日维护**：检查并删除过期数据，确保存储压力不随时间无限增长。
- **每周优化**：日常维护恰好落在周日的那一次会额外执行 `VACUUM`（重组文件）与 `ANALYZE`（更新索引统计信息）；具体时刻取决于后端启动时间 + 5 分钟的周期。

如果您需要手动触发清理，可以通过重启 `proxygw` 服务来实现，维护任务会在启动 5 分钟后执行首次扫描。

## 🩺 常见故障排查 (Troubleshooting)

### 0. 先看这些日志行的含义
- `[AUDIT] Mosdns config unchanged, restart skipped` / `Xray config unchanged and unit active, restart skipped`：规则或节点变更没有改变渲染出的配置，后端有意不重启服务，属正常。
- `[INFO] mosdns restart (fakeip flush) skipped: restarted Ns ago`：Mode B 下 Xray 下发时的 FakeIP 刷新重启与刚发生的 mosdns 重启合并。
- `[AUDIT] Applying Mosdns Config (config_changed=... domains_changed=...)`：本次确实重写了文件并重启。
- `login_locked_out`（事件日志）：同一来源 IP 30 分钟内失败超过 10 次，接口返回 429；重启 proxygw 会清空计数。
- 升级后页面样式或按钮异常：`/ui/libs/*` 在浏览器缓存 1 小时，强制刷新（Ctrl+F5）。

### 1. 局域网内设备无法上网或无法走代理 (Mode A)
- **排查 Nftables 劫持**：运行 `nft list ruleset` 检查 `prerouting` 链是否正常工作。
- **排查内核路由**：执行 `ip rule` 检查是否存在 `fwmark 0x1 lookup tproxy`，执行 `ip route show table tproxy` 检查本地回环路由是否正确。
- **排查 Xray 错误**：使用日志命令检查 Xray 是否因为端口冲突 (12345/10808) 导致配置加载失败。

### 2. Mosdns 启动失败退出 / 频繁重启
- **快速诊断**：停止守护进程后，手动在前台运行以暴露错误信息：
  ```bash
  systemctl stop mosdns
  /root/proxygw/core/mosdns/mosdns start -d /root/proxygw/core/mosdns
  ```
- **典型错误**：如果看到 IP 集合相关的 Error，请检查你是否不小心放入了二进制格式的 `geoip.dat` 文件。Mosdns v5+ 必须使用纯文本的 CIDR 格式。

### 3. OSPF 路由不生效 / 无法无感接管 (Mode B / Mode C)
- **检查 FRR 进程**：先执行 `systemctl is-active frr`。若后端日志反复出现 `vtysh ... failed to connect to any daemons`，说明规则已生成但 FRR 未运行，路由不会真正发布到主路由。
- **检查发布状态**：执行 `sqlite3 config/proxygw.db "select source,status,count(*) from routes_table group by source,status;"`，若大量规则停在 `candidate`，继续查看 `journalctl -u proxygw -n 200 --no-pager` 中的 FRR/vtysh 错误。正常撤回会打印 `[OSPF] N published route(s) expired, withdrawing`；候选路由按 `push_interval_seconds`（默认 10s）分批推送，且需在表中存在 60s 以上。UI 日志面板也提供 FRR 与 nftables 的 journal 输出。
- **检查邻居状态**：执行 `vtysh` 进入路由器交互模式，输入 `show ip ospf neighbor`，应看到上游路由器处于 `Full/DR` 或 `Full/Backup`。
- **诊断要素**：确保主路由（如 MikroTik ROS / OpenWrt）已将此代理服务器的 IP 网段加入相同的 OSPF Area 并且 Interface Network Type 匹配（通常应设为 Broadcast）。
- **防环路漏配 (Mode C)**：如果在 Mode C 发现代理通缩、网速极慢或完全断网，请检查主路由上是否正确配置了源地址绕过（PBR 策略路由）以防止 OSPF 环路。

### 4. MikroTik ROS 防环路 PBR 配置示例 (Mode C 必备)
在 Mode C 下，EdgeRouteGW 会通过 OSPF 将大量的真实代理 IP 网段发给 ROS，这会覆盖 ROS 的默认路由。当 EdgeRouteGW 自身向代理节点发起出站连接时，如果节点的 IP 刚好命中这些 OSPF 路由，流量又会被 ROS 踢回给 EdgeRouteGW，造成死循环。
您必须在 ROS 中强制让 EdgeRouteGW 发出的流量直连公网。

> 完整新手配置与图形界面路径请同时参考：[`docs/NETWORK_SETUP.md`](./NETWORK_SETUP.md)（Mode C 防环路 PBR 章节）。

**ROS v7 配置命令参考：**
```routeros
# 1. 创建一个干净的独立路由表 (不受 OSPF 污染)
/routing table add name=bypass_proxy fib

# 2. 为该表指定真实的物理出口 (假设公网出口为 pppoe-out1)
/ip route add dst-address=0.0.0.0/0 gateway=pppoe-out1 routing-table=bypass_proxy

# 3. 添加策略路由规则 (PBR)：强制网关设备 (如 192.168.20.155) 的所有出站流量只查这个干净的表
/routing rule add src-address=192.168.20.155/32 action=lookup-only-in-table table=bypass_proxy
```
*注：如果是 ROS v6 系统，在第1步无需 `fib` 参数，在 IP -> Routes -> Rules 菜单中配置对应的 Src Address 和 Table 即可。*

### 5. OSPF 出现异常前缀（如 `0.0.0.0/32`）如何处理

自 `v1.5.19` 起，后端默认启用脏路由过滤与启动自愈清理：
- API 层拒绝无效 IP/CIDR
- OSPF 同步前再次过滤
- 服务启动时自动清理历史脏数据

#### 脏路由判定范围
- `0.0.0.0` / `0.0.0.0/32`
- `0.0.0.0/0`
- `127.0.0.0/8`
- `169.254.0.0/16`
- `198.18.0.0/15`（Fake-IP 网段）
- `224.0.0.0/4` 及以上（含保留/广播）
- RFC1918 私网超集（如整段 `192.168.0.0/16`）

#### 运维核查命令

1) 先看启动后是否有自动清理日志：
```bash
journalctl -u proxygw -n 200 --no-pager | grep -E "purged [0-9]+ dirty routes|OSPF"
```

2) 手动检查数据库中是否还存在典型脏路由：
```bash
sqlite3 /root/proxygw/config/proxygw.db "SELECT ip,source,status FROM routes_table WHERE ip IN ('0.0.0.0','0.0.0.0/32','0.0.0.0/0') OR ip LIKE '127.%' OR ip LIKE '169.254.%' OR ip LIKE '224.%' OR ip LIKE '225.%' OR ip LIKE '226.%' OR ip LIKE '227.%' OR ip LIKE '228.%' OR ip LIKE '229.%' OR ip LIKE '23_.%' OR ip LIKE '24_.%' OR ip LIKE '25_.%';"
```

3) 如需立即触发清理流程（建议）：
```bash
systemctl restart proxygw
```

若重启后仍持续出现脏路由，优先检查是否有外部脚本直接写库绕过 API，或存在旧版二进制未替换成功。

### 6. 开发机出现大量 `via 192.168.20.1 proto static metric 20` 是否环路

这类路由本身不一定是 bug。危险在于：
- 开发机（EdgeRouteGW）访问某目标 `X` 时，下一跳是主路由 `192.168.20.1`
- 主路由又因为 OSPF 把同一目标 `X` 回指到 EdgeRouteGW

这会形成回弹闭环：`EdgeRouteGW -> 主路由 -> EdgeRouteGW`。

#### 系统级防护（已内置）
- `syncStaticRoutesToOSPF()` 自动排除受保护节点地址（`nodes.address` / `remote_nodes.ssh_host` / `remote_node_wg.endpoint` / `remote_node_vless.dest` 及其解析 IP）
- 切换 `Mode B/C` 前做 preflight，若检测到 `candidate_ospf_routes ∩ protected_node_ips` 非空，直接拒绝切换

#### 仍建议保留的网络侧兜底
在主路由保留源地址旁路（PBR）：

**MikroTik ROS（v7）：**
```routeros
/routing table add name=bypass_proxy fib
/ip route add dst-address=0.0.0.0/0 gateway=pppoe-out1 routing-table=bypass_proxy
/routing rule add src-address=192.168.20.155/32 action=lookup-only-in-table table=bypass_proxy
```

**MikroTik ROS（v6 等价）：**
```routeros
/ip route add dst-address=0.0.0.0/0 gateway=pppoe-out1 routing-mark=bypass_proxy
/ip firewall mangle add chain=prerouting src-address=192.168.20.155 action=mark-routing new-routing-mark=bypass_proxy passthrough=no
```

**OpenWrt（临时生效，立即止血）：**
```bash
# 变量按现场替换
PROXY_IP="192.168.10.9/32"
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

**OpenWrt（持久化，推荐）：** 新建 `/etc/hotplug.d/iface/99-proxygw-bypass`
```sh
#!/bin/sh
[ "$ACTION" = "ifup" ] || exit 0
case "$INTERFACE" in
  wan|pppoe-wan) ;;
  *) exit 0 ;;
esac

PROXY_IP="192.168.10.9/32"   # 改成你的 EdgeRouteGW LAN IP
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
然后执行：
```bash
chmod +x /etc/hotplug.d/iface/99-proxygw-bypass
ACTION=ifup INTERFACE=wan /etc/hotplug.d/iface/99-proxygw-bypass
```

验证：
```bash
ip -4 rule show | grep "from 192.168.10.9/32"
ip -4 route show table 100
```

这条规则可确保 EdgeRouteGW 自身出站永远走 WAN 主路，不受 OSPF 回流影响。
