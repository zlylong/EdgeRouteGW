# 从 1.x 迁移到 2.0

两代共存会争抢 53/80/12345 端口、nftables 表和 ip rule,所以迁移是"停 1.x → 装 2.0 → 导入 → 启动"的顺序。整个过程 LAN 断网约 1 到 3 分钟(安装与组件下载期间)。旧目录 `/root/proxygw` 全程保留,可回退。

## 0. 准备

- 网关上 `/root/proxygw/config/proxygw.db` 是 1.x 的全部配置,`config/aes.key` 是托管主机 SSH 凭证的密钥。先做一份备份:
  ```bash
  mkdir -p /root/v1-backup && cp /root/proxygw/config/proxygw.db /root/proxygw/config/aes.key /root/v1-backup/
  ```
- 记下当前模式(UI 顶栏)。Mode B/C 的用户:主路由侧的 OSPF 配置不用改;2.0 的 FRR 配置形态变了(内核 table 100 导入),但对主路由只是同一个邻居重新宣告。
- 2.0 不再 `git pull` 升级,也不再使用 `/root/proxygw` 下的任何东西。

## 1. 停掉 1.x

```bash
systemctl disable --now proxygw xray mosdns frr 2>/dev/null
nft delete table inet proxygw 2>/dev/null || true
ip rule del fwmark 1 lookup tproxy 2>/dev/null || true
```

## 2. 安装 2.0

能访问 GitHub 的网关:

```bash
bash <(curl -fsSL https://raw.githubusercontent.com/zlylong/EdgeRouteGW/v2/scripts/install.sh)
```

**不能访问 GitHub 的网关(国内常见)**:在任何能下载的机器上取发布页的 `edgeroutegw-full_<版本>_<架构>.tar.gz`,scp 到网关后:

```bash
tar xzf edgeroutegw-full_*.tar.gz && cd edgeroutegw-full_* && bash install-offline.sh
```

full 包里带了经校验的 Xray、Mosdns 和 GeoData(`edgeroutegw-components` 包),首次启动自动装进数据目录,不需要联网。之后的组件更新可以在「系统 → 组件更新 → 下载通道」配置代理(例如网关自己的隧道 `socks5://127.0.0.1:10808`)或 GitHub 镜像前缀,也可以本地上传官方压缩包安装。

安装后先**不要**登录 UI 做配置,直接导入。

## 3. 导入 1.x 配置

```bash
systemctl stop edgeroutegw
edgeroutegw import-v1 --dry-run --old-key /root/proxygw/config/aes.key /root/proxygw/config/proxygw.db   # 先看报告
edgeroutegw import-v1 --old-key /root/proxygw/config/aes.key /root/proxygw/config/proxygw.db
systemctl start edgeroutegw
```

导入内容与映射:

| 1.x | 2.0 |
|---|---|
| `nodes`(type/uuid/params) | 节点;协议名转小写;WireGuard 的 `settings.secretKey` 移到凭证;`default_node_id` → 默认节点;`ping` → 延迟 |
| `rules.policy = ha-A-B` | 一个故障转移出站组「HA A → B」,规则指向该组 |
| `rules.policy = proxy-N` | 规则指向节点 N |
| `rules.group_id/group_name` | 规则集(按 priority 顺序);无分组的进「默认」 |
| `lan_acls` / `protected_ips` | 设备策略 / 绕过目标 |
| `settings`(mode、网卡、DNS、OSPF 参数、cron) | 网络与 DNS 配置;GeoData 定时改为每日 |
| `remote_nodes` + `remote_node_wg/vless` | 托管主机 + 一条"已成功"的部署记录(可直接「重新下发」);SSH 凭证需要 `--old-key`,否则主机导入但要重新输入凭证 |

不导入:管理员密码(2.0 生成新的初始密码)、事件日志、流量历史、解析缓存、路由表(会重新计算)。

## 4. 首次登录

- 初始密码:`cat /var/lib/edgeroutegw/bootstrap_password.txt`,登录后强制改密。
- 系统 → 组件更新:在线安装 Xray、Mosdns、GeoData;用 full 包安装的话它们已经在了。安装完成后 Reconciler 会自动启动它们并下发配置。
- 总览页四个组件都变绿后,用「分流」页顶部的 Trace 框验证几个域名的去向;Mode B/C 看「网络与模式 → OSPF 状态」的邻居和已宣告路由数。

## 5. 回退到 1.x

```bash
systemctl disable --now edgeroutegw edgeroutegw-xray edgeroutegw-mosdns
nft delete table inet edgeroutegw 2>/dev/null || true
ip route flush table 100 2>/dev/null || true
systemctl enable --now proxygw xray mosdns
```

1.x 的目录和数据库未被改动。

## 文件与服务对照

| | 1.x | 2.0 |
|---|---|---|
| 二进制 | `/root/proxygw/backend/proxygw-backend` | `/usr/bin/edgeroutegw` |
| 数据 | `/root/proxygw/config/proxygw.db` | `/var/lib/edgeroutegw/edgeroutegw.db` |
| 密钥 | `config/aes.key` | `/var/lib/edgeroutegw/keys/master.key` |
| Xray/Mosdns | `core/xray`、`core/mosdns`(git 提交) | `/var/lib/edgeroutegw/components/`(UI 安装) |
| 服务 | `proxygw` `xray` `mosdns` | `edgeroutegw` `edgeroutegw-xray` `edgeroutegw-mosdns` |
| nft 表 | `inet proxygw` | `inet edgeroutegw` |
| OSPF 路由 | FRR 静态路由 tag 100 | 内核 table 100 + `ip import-table` |
| 升级 | `update.sh`(git pull) | 系统 → 组件更新 → EdgeRouteGW,或 `apt install ./edgeroutegw_x.deb` |
