# 运维与故障排查手册

## 1. 服务与文件

| | 路径 / 名称 |
|---|---|
| 控制面 | `edgeroutegw.service`,二进制 `/usr/bin/edgeroutegw`,监听 `:80` |
| Xray / Mosdns | `edgeroutegw-xray.service`、`edgeroutegw-mosdns.service`(`PartOf` 控制面,随它重启) |
| FRR | 系统的 `frr.service`,只在 Mode B / C 启用 |
| 数据目录 | `/var/lib/edgeroutegw`:`edgeroutegw.db`(SQLite,0600)、`keys/master.key`(凭证加密主密钥,0600)、`components/`(Xray、Mosdns 二进制与上一版 `.prev`)、`geodata/`、`xray/`、`mosdns/`、`nft/ruleset.nft`、`bootstrap_password.txt`(改密后删除) |
| 运行时目录 | `/run/edgeroutegw`(tmpfs):`xray_access.log`、`mosdns.log`,任一超过 64 MB 由控制面原地截断(只是实时视图) |
| 环境文件 | `/etc/edgeroutegw/edgeroutegw.env`:`ERGW_LISTEN`、`ERGW_DATA_DIR`、`ERGW_LOG_LEVEL`、`ERGW_SECURE_COOKIES`(TLS 反代后置 1) |
| 内核参数 | `/etc/sysctl.d/99-edgeroutegw.conf`、`/etc/modules-load.d/edgeroutegw.conf`(wireguard、tcp_bbr、nfnetlink_log) |
| 内置组件包 | `edgeroutegw-components` deb → `/usr/share/edgeroutegw/components`(离线 full 包自带,首次启动种子化) |

端口:80 管理面;53 Mosdns(双栈);12345 TPROXY 入站(只接受带标记的包,直连会被 input 链丢弃);10808 / 10809 本机 SOCKS / HTTP(只监听 127.0.0.1,Mosdns 远端上游经它走节点);10810 探测入站;10085 Xray API;5353 Fake-IP DNS(Mode B)。

命令行子命令:`edgeroutegw serve`(服务)、`doctor`(检查二进制、sysctl、端口、unit、调优项)、`import-v1`(导入 1.x 库)、`version`。

## 2. 看状态和日志

```bash
systemctl status edgeroutegw edgeroutegw-xray edgeroutegw-mosdns
edgeroutegw doctor
journalctl -u edgeroutegw --since -30min --no-pager        # 控制面:协调、下发、更新、调优
journalctl -u edgeroutegw-xray --since -30min --no-pager   # Xray 自身日志(默认 warning 级)
tail -f /run/edgeroutegw/xray_access.log                   # 每条连接:来源、目标、入站 → 出站
tail -f /run/edgeroutegw/mosdns.log
```

页面上:总览(组件状态、最近下发是否失败、主机监控)、观测(实时连接、事件、流量、节点延迟)、系统 → 生成的配置(Xray / Mosdns / nftables 当前渲染结果)。远程机器上跑 `journalctl` 记得带 `--since`,否则很慢。

**日志会不会堆满?** 系统 → 日志与保留。历史记录与流量样本按天清理(默认 90 天),/run 下的 Xray / Mosdns 日志超过上限(默认 64 MB)原地清空,systemd journal 由本服务写的 `/etc/systemd/journald.conf.d/60-edgeroutegw.conf` 限定(默认 30 天 / 500 MB)。每小时自动跑一次;「立即清理」把这些立刻做完并压缩数据库。1.x 在同一台机器上留下的几 GB journal 也在这里清。

控制面以 root 运行但受 systemd 沙箱约束(ProtectSystem=strict、ProtectKernelModules 等),所以它自己不 `modprobe`、不写 `/usr`(日志保留需要的 journald 目录是显式放开的例外);需要的内核模块由 `modules-load.d` 在开机时加载。

## 3. 备份与恢复

- **页面**:系统 → 安全 → 备份与恢复。「导出(不含凭证)」得到节点、出站组、规则、设备策略、DNS 与网络设置的 JSON;「导出(含凭证)」连节点密码 / 私钥一起导出(只对浏览器会话开放,会记一条事件);「恢复」整体替换当前配置。
- **API**:`GET /api/v2/backup[?include_secrets=1]`、`POST /api/v2/restore`。
- **文件级**:停服务后拷走整个 `/var/lib/edgeroutegw`(库 + `keys/master.key` 必须一起,凭证靠主密钥解密);恢复时放回同样位置、保持 0600 / 0750 权限再启动。`sqlite3 edgeroutegw.db ".backup /path/x.db"` 可以在线取一致的库副本。

不在备份里的东西:事件日志、流量历史、解析缓存、宣告中的路由(启动后重算)、组件二进制(重新安装即可)。

## 4. 管理员密码

- 改密:系统 → 安全 → 账户与会话 → 修改密码。改密会注销其它会话,并默认吊销所有 API token(接口带 `keep_tokens: true` 可保留)。
- 初始密码:`/var/lib/edgeroutegw/bootstrap_password.txt`,改密后删除。初始密码登录的会话只能改密,其它接口一律 403 `PASSWORD_CHANGE_REQUIRED`。
- **忘记密码 / 新密码登不上**:在网关上重置回"初始密码"状态。不要在 bash 里把哈希拼进命令行(哈希含 `$` 会被展开),用下面的方式,运行后在 `bootstrap_password.txt` 里读新初始密码:

```bash
systemctl stop edgeroutegw
sqlite3 /var/lib/edgeroutegw/edgeroutegw.db "DELETE FROM sessions; DELETE FROM auth_admin;"
systemctl start edgeroutegw          # 启动时没有管理员记录就会重新生成初始密码
cat /var/lib/edgeroutegw/bootstrap_password.txt
```

登录限速按来源 IP,连续失败后要等几分钟(日志 429);密码输错和限速都是 401 / 429,不会是 500。

## 5. 组件与自升级

- **Xray / Mosdns / GeoData**:系统 → 组件更新。「更新到最新」按发布方的摘要校验(Xray `.dgst`、GitHub 资产 digest、`rules.zip.sha256sum`),先跑 `version` 冒烟,再原子替换;服务起不来自动回退;「回退」随时切回 `.prev`。每张卡可设每天 / 每周 / 每月自动更新,到点只在出现新版本时才动,不会无谓重启。
- **GitHub 不可达**:装离线 full 包里的 `edgeroutegw-components`(「从内置包安装」);或「本地上传」官方压缩包(可填 sha256,不填会按未校验安装并记事件);或在「下载通道」填镜像前缀(只接受 https,校验文件优先直连 GitHub)、下载代理(可以是网关自己的 `socks5://127.0.0.1:10808`);匿名 GitHub API 每小时只有 60 次,多台共用出口时填一个无权限的 GitHub Token。
- **网关本体**:同一页的 EdgeRouteGW 卡片。自升级下载 deb、校验 SHA256SUMS,通过 `systemd-run` 跑 `dpkg -i`(控制面自己的沙箱写不了 `/var/lib/dpkg`),3 秒后由瞬态 unit 重启服务。新版本发布后一两分钟内 GitHub 的列表接口可能返回空,此时会退回用发布页跳转取标签;再不行就手动 `apt install ./edgeroutegw_<版本>_<架构>.deb`。
- 升级永远不动 `/var/lib/edgeroutegw`;库结构变更由内置迁移在启动时自动完成(日志 `applied 000N_...`)。

## 6. 系统调优

系统 → 系统调优。启动时和点「重新检测并应用」时探测并处理:sysctl(转发、端口范围、缓冲区、fastopen)、BBR + fq_codel、网卡多队列(能升就 `ethtool -L`,虚拟机单队列时提示在宿主机开多队列并用 RPS 分散软中断)、内核 WireGuard 模块、conntrack 表容量(有 NAT 时设到 1048576)。每项显示 已生效 / 本次应用 / 已回退 / 需外部处理;`edgeroutegw doctor` 打印同一份报告。托管主机在每次部署后也会跑同样的调优并把报告存在主机记录里。

## 7. 常见故障

### Mode A 设备不通或不走代理
1. `nft list chain inet edgeroutegw prerouting`:`intercept_v4` 计数器在涨说明在拦截;`fib daddr type local return` 之前的几条是放行私网、网关自身、DNS。
2. `ip rule` 里要有 `fwmark 0x1 lookup 100`,`ip route show table 100` 是 `local default dev lo`。1.x 遗留的 `/etc/iproute2/rt_tables` 会把 100 显示成 `tproxy`,用 `ip -N rule` 看数字。
3. `journalctl -u edgeroutegw-xray` 看 Xray 是否因端口被占(12345 / 10808)起不来;总览上的「最近一次下发失败」会写明原因。
4. 设备确实把网关和 DNS 指向了本机?`dig @<网关> www.google.com` 能解析、`curl -I https://www.google.com` 能通。

### DNS 解析异常
- `dig @127.0.0.1 x.com`、`dig @8.8.8.8 x.com`(第二条也会被劫持到 Mosdns,除非目的是私网 DNS);Mosdns 起不来看 `/run/edgeroutegw/mosdns.log` 第一屏。
- 远端上游经节点出去:默认节点挂了,所有境外域名都解析不了。观测页看默认节点探测;临时把远端上游换成明文公共 DNS 验证。
- 内网域名解析不到:DNS 页「内网域名转发」把后缀指到内网 DNS;它排在缓存和分流之前。
- 国内站拿到境外 CDN 地址:填「ECS 客户端子网」为你的国内出口网段。

### 分流不符合预期
用「分流」页顶部的 Trace 输入域名或 IP,它按当前规则给出命中的规则与去向。常见原因:前面的规则遮蔽了后面的(内置集里种子的 `geosite:cn / geoip:cn → 直连` 排在你自建的 CN 集前面)、`ip` 规则排在 `geoip` 之后、兜底策略设成了直连。access log 里目标一律是 IP(嗅探是 routeOnly),看域名靠观测页的「≈ 域名」标注。

### OSPF(Mode B / C)
见 [NETWORK_SETUP.md](NETWORK_SETUP.md) 第 7 节。

### 自升级后页面连不上
升级过程会重启控制面(含 Xray / Mosdns),约 5~10 秒。`systemctl status edgeroutegw`;`dpkg -l edgeroutegw` 看版本;`ls /var/lib/edgeroutegw/.stage-*` 残留说明重启没被安排上,手动 `systemctl restart edgeroutegw` 并删掉暂存目录。

### 「检查新版本」失败
多半是 GitHub 匿名配额用尽,错误信息里有恢复时间;填 GitHub Token 或等它恢复。网关出口在境内又没有镜像时,用离线包或本地上传。

### 托管主机部署失败
- 主机密钥变化:主机列表会提示重新信任,确认后重钉。
- REALITY 冒烟失败:选的 `dest` / `server_name` 从那台主机不可用,换一个(默认 `www.apple.com`)。
- 同一主机同一种服务只能有一条部署记录;两种服务(WireGuard + VLESS)可以并存。

## 8. 回退到 1.x / 卸载

回退见 [UPGRADE-FROM-1.x.md](UPGRADE-FROM-1.x.md) 第 5 节。彻底卸载:

```bash
apt remove edgeroutegw edgeroutegw-components
nft delete table inet edgeroutegw 2>/dev/null; ip rule del fwmark 1 lookup 100 2>/dev/null
rm -rf /var/lib/edgeroutegw /etc/edgeroutegw      # 连同数据,确认不再需要
```
