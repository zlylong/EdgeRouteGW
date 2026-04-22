# 系统运维与故障排查手册

本文档面向 ProxyGW 的系统管理员，提供服务的日常维护、平滑升级、数据备份以及故障排除的指导原则。

## 🔧 自动化生命周期脚本

项目在 `scripts/` 目录下为您准备了全生命周期的自动化部署与维护脚本：

- **📦 初始安装/重置环境**：
  ```bash
  bash <(curl -s -4 -L https://raw.githubusercontent.com/zlylong/proxygw/main/scripts/install.sh)
  ```
  用于全新环境安装，或彻底修复系统底层依赖与内核环境。此操作会覆盖 Systemd 文件并重新注入 TProxy 路由规则。

- **🔄 一键平滑升级**：
  ```bash
  bash <(curl -s -4 -L https://raw.githubusercontent.com/zlylong/proxygw/main/scripts/update.sh)
  ```
  推荐的日常维护命令。它将自动从 GitHub `main` 分支拉取最新代码，智能检查依赖，自动获取 GitHub Releases 最新版预编译后端二进制，并平滑重启所有相关守护服务。

- **🗑️ 彻底卸载系统**：
  ```bash
  bash scripts/uninstall.sh
  ```
  安全停用相关守护进程，清理所有的二进制文件，剥离 Linux 内核级别的 TProxy 劫持规则和路由表，恢复纯净的宿主机网络环境。卸载过程中会询问是否保留用户配置文件和 SQLite 数据库。

## ⚙️ 系统服务状态管理

ProxyGW 基于标准的 Systemd 协同工作，日常系统诊断可使用标准命令：

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

**数据备份**：
建议在进行重大变更前定期备份 `config/` 目录。
```bash
# 备份数据库与关键核心文件
tar -czvf proxygw_backup_$(date +%F).tar.gz /root/proxygw/config/ /root/proxygw/core/
```

**紧急密码重置**：
管理员密码经过 Bcrypt 强哈希加密。如果遗忘且无法登入 UI，请使用 `sqlite3` 直接重置数据库中的条目：
```bash
sqlite3 /root/proxygw/config/proxygw.db "UPDATE users SET password_hash = '' WHERE username = 'admin';"
```
*(注意：请根据实际情况或通过初始化生成脚本恢复访问。)*

## 🩺 常见故障排查 (Troubleshooting)

### 1. 局域网内设备无法上网或无法走代理 (Mode A)
- **排查 Nftables 劫持**：运行 `nft list ruleset` 检查 `prerouting` 链是否正常工作。
- **排查内核路由**：执行 `ip rule` 检查是否存在 `fwmark 0x1 lookup tproxy`，执行 `ip route show table tproxy` 检查本地回环路由是否正确。
- **排查 Xray 错误**：使用日志命令检查 Xray 是否因为端口冲突 (12345/10808) 导致配置加载失败。

### 2. Mosdns 启动失败退出 / 频繁重启
- **快速诊断**：停止守护进程后，手动在前台运行以暴露错误信息：
  ```bash
  systemctl stop mosdns
  /usr/local/bin/mosdns start -d /root/proxygw/core/mosdns
  ```
- **典型错误**：如果看到 IP 集合相关的 Error，请检查你是否不小心放入了二进制格式的 `geoip.dat` 文件。Mosdns v5+ 必须使用纯文本的 CIDR 格式。

### 3. OSPF 路由不生效 / 无法无感接管 (Mode B / Mode C)
- **检查邻居状态**：执行 `vtysh` 进入路由器交互模式，输入 `show ip ospf neighbor`。
- **诊断要素**：确保主路由（如 MikroTik ROS / OpenWrt）已将此代理服务器的 IP 网段加入相同的 OSPF Area 并且 Interface Network Type 匹配（通常应设为 Broadcast）。
- **防环路漏配 (Mode C)**：如果在 Mode C 发现代理通缩、网速极慢或完全断网，请检查主路由上是否正确配置了源地址绕过（PBR 策略路由）以防止 OSPF 环路。

### 4. MikroTik ROS 防环路 PBR 配置示例 (Mode C 必备)
在 Mode C 下，ProxyGW 会通过 OSPF 将大量的真实代理 IP 网段发给 ROS，这会覆盖 ROS 的默认路由。当 ProxyGW 自身向代理节点发起出站连接时，如果节点的 IP 刚好命中这些 OSPF 路由，流量又会被 ROS 踢回给 ProxyGW，造成死循环。
您必须在 ROS 中强制让 ProxyGW 发出的流量直连公网：

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
- `224.0.0.0/4` 及以上（含保留/广播）

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
