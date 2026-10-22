# EdgeRouteGW 2.0 变更记录

## [Unreleased]

- 「检查新版本」失败多半是 GitHub API 匿名配额(每出口 IP 每小时 60 次)用尽:现在错误信息直接写明「请求次数用尽,HH:MM 恢复」;releases API 改用 ETag 条件请求(304 不计配额);配额用尽时「最新版本」退回用 releases/latest 的跳转取标签(不限流);下载通道新增可选 GitHub Token(5000 次/小时)。
- 网络与模式 → 数据面参数不再显示「LAN 默认策略」:它和分流页末尾的兜底规则是同一个设置,只保留分流页那一处。
- 组件卡片布局:版本信息与操作按钮在上,自动更新区块(计划选择 + 说明)和组件说明文字放在卡片最下方。

## [2.0.0-rc.21] - 2026-10-22

- 「选择版本」对话框不再用原生下拉(rc.20 的 option 修复在实机上仍打不开弹层):改为可滚动的版本列表,每行标出预发布 / 已安装与发布日期,点选后按钮显示将安装的版本。

## [2.0.0-rc.20] - 2026-10-22

- 组件自动更新:Xray、Mosdns、GeoData 三张卡片各自带「自动更新」设置,可选关闭 / 每天 / 每周(选星期)/ 每月(选 1~28 日)加时间(本机时区);独立的「GeoData 自动更新」卡片移除。到点先查 GitHub,只有出现新版本才执行更新,所以 Xray/Mosdns 不会被每天无谓重启。设置存 `update.schedule.<name>`(GeoData 沿用 `geodata.auto_update`,与 1.x 导入和备份兼容);`GET/PUT /components/{name}/schedule`,`GET /components` 每项带 `schedule`。
- 修复:「选择版本」对话框里的下拉框点开立即关闭。原因是对话框任何一次重渲染都会让 Vue 重设每个 option 的 value,Chromium 遇到 option 变动就收起原生下拉;现在 Select 只在选项真的变化时才碰 option,版本列表也改为稳定的 computed。

## [2.0.0-rc.19] - 2026-10-22

- 托管主机调优:每次部署成功后自动在主机上探测并应用(BBR + fq、缓冲区与 TCP 参数、文件数上限、网卡多队列或 RPS、conntrack(有 NAT 时)、内核 WireGuard),结果记在主机上;主机列表新增「调优」按钮查看报告或重跑(`POST /hosts/{id}/tune`)。
- 出站组对话框:类型(故障转移 / 负载均衡)和四种负载策略(leastPing 最低延迟、leastLoad 最稳定、random 随机、roundRobin 轮询)都有说明文字,写明 Xray 每 30 秒探测一次成员、探测失败的成员不会被选中;出站组列表显示所用策略。

## [2.0.0-rc.18] - 2026-10-22

- DNS 上游默认改为加密:国内 `https://223.5.5.5/dns-query`(阿里 DoH)+ `tls://1.12.12.12`(DNSPod DoT),远端 `https://1.1.1.1/dns-query`(Cloudflare DoH)+ `tls://8.8.8.8`(Google DoT,经节点代理)。IP 形式不需要 bootstrap;域名形式自动加 `bootstrap: 223.5.5.5`;加密上游保持长连接(idle_timeout 30s),tcp/tls 开启 pipeline。迁移 0005 只把仍是原明文默认值的库切换过去,自定义列表不动。
- 修复:Mosdns 的远端上游经本机 SOCKS 进入 Xray 后按兜底处理,兜底为直连时实际没有走节点;现在来自 socks_in、目的为远端上游的连接固定走默认代理出站(规则 `sys-dns-upstream-N`)。
- 修复:Mode A 下兜底「代理」不生效(Xray 的 catch-all 被写死为直连);现在三种模式都按兜底策略处理,Trace 文案同步。

## [2.0.0-rc.17] - 2026-10-22

- 防 DNS 泄露:经过网关的所有 53 端口查询(私网目的地址除外)在 nat 链 redirect 到本机 Mosdns,设备手动指定的 8.8.8.8 之类也落入分流逻辑;DoT/DoQ(853)拒绝,客户端退回普通 DNS。Mosdns 改为双栈监听。DNS 页两个开关,默认开启;迁移 0004;从 1.x 导入默认开启。DoH 无法通用识别,不在范围内。
- 分流页兜底区增加「节点失效策略」(退回默认出站 / 拦截),从网络与模式页移出,两条兜底规则放在一起。
- 系统 → 安全:「管理员密码」与「登录会话」合并为一张「账户与会话」卡,修改密码作为卡片右上角的按钮;三张卡改为单列,不再有半空的大卡片。
- 系统调优:「不可用」改为「需外部处理」,表示网关自身做不了、提示里写了你要做的事。

## [2.0.0-rc.16] - 2026-10-22

- 「系统 → 系统调优」:环境探测式调优成为功能。启动时(和点「重新检测并应用」时)探测并处理:固定 sysctl 值校验、BBR 可用性(无 tcp_bbr 则报告回退 cubic)、网卡多队列(能升则 `ethtool -L` 到 min(上限, CPU 数);上限为 1 的虚拟机给出宿主机设置提示并启用 RPS 把软中断分散到所有 CPU)、内核 WireGuard 模块、conntrack 容量。每项显示已生效 / 本次应用 / 已回退 / 不可用及建议;`edgeroutegw doctor` 打印同一份报告;`GET /tuning`、`POST /tuning/apply`。deb 新增依赖 ethtool,modules-load 加 tcp_bbr。

## [2.0.0-rc.15] - 2026-10-22

- WireGuard 节点改走内核:有 wireguard 内核模块时,每个 WireGuard 节点变成一个内核接口(`ergw-xxxxxxxx`)+ 专用路由表 + fwmark 规则,Xray 的该节点出站变为带 mark 的 freedom,隧道加解密不再在 Xray 用户态进行(实验环境实测用户态 WG 只有约 260 Mbps 且吃满 2.4 核)。无模块、带 reserved 字节(WARP)或参数不全的节点自动回退用户态实现。新组件「WireGuard(内核)」显示握手健康。
- nftables 新增 forward 链 MSS 钳制(`tcp option maxseg size set rt mtu`),转发流量经隧道不再分片。
- sysctl 补充 `tcp_slow_start_after_idle=0`、`tcp_mtu_probing=1`、`netdev_max_backlog=16384`。

## [2.0.0-rc.14] - 2026-10-22

- 内核调优补齐 1.x 安装脚本的全部项:sysctl 文件新增 `tcp_fastopen=3`;conntrack 参数(max 1048576、established 3600s 等)由服务在 nf_conntrack 模块存在时运行时写入(2.0 的 TPROXY 规则本身不用 conntrack,不再强制加载模块)。`edgeroutegw doctor` 检查 BBR、fq_codel、fastopen 与 conntrack。

## [2.0.0-rc.13] - 2026-10-22

- 观测 → 连接:目标旁显示它所属的 geoip/geosite 标签(最多 3 个,其余折叠),点击跳到 GeoData 查询;过滤框可按标签搜。
- 分流 → GeoData 查询:输入域名或 IP 看属于哪些标签,输入 `geosite:xxx` / `geoip:xxx` 看标签内容(可过滤)。新增 `GET /geo/lookup?target=` 与 `GET /geo/entries?kind=&tag=&q=&limit=`;反查索引按需建立、GeoData 更新后自动重建。

## [2.0.0-rc.12] - 2026-10-21

- 修复:LAN 默认策略为「直连」时 nftables 不再拦截未命中流量,导致域名规则失效、Mode B 的 Fake-IP 和 Mode C 的发布路由流量被投递到网关自身(连接立刻被拒)。现在所有 LAN 流量都进 Xray,默认策略只决定 Xray 的兜底出站。

## [2.0.0-rc.11] - 2026-10-21

- 兜底规则可见化:「默认」规则集最后固定一行「所有规则集都没有命中的流量 → 直连 / 代理」,直接在分流页切换(即 LAN 默认策略)。新装网关兜底为直连(迁移 0003 只改没有任何节点和自定义规则的库;导入与已配置的网关保持原值)。

## [2.0.0-rc.10] - 2026-10-21

- 内置默认规则集「默认」:永远第一个匹配,不能删除、移动或改名(规则可以编辑)。新装自带三条直连规则:私有网络(geoip:private)、国内域名(geosite:cn)、国内 IP(geoip:cn);从 1.x 导入或升级时,原「默认」规则集被认定为内置集,导入的规则排在这三条之前,不会被它们遮住。只种子化一次,删掉的内置规则不会再回来。
- 观测 → 连接:所有从回环口入站的连接(本机 SOCKS/HTTP、探测、内部 API、Mosdns 远程上游)都算系统连接,默认隐藏。

## [2.0.0-rc.9] - 2026-10-21

- 观测 → 连接:出站显示节点/出站组名称而不是 Xray 标签(`node-<id>`、`api`),直连/拦截/DNS 转发/内部 API 用中文;网关自身的探测与内部 API 连接默认隐藏,可勾选显示;非 LAN 来源标注入站类型(本机 SOCKS/HTTP、节点探测)。
- 节点探测改为端到端:Xray 渲染一个只监听 127.0.0.1:10810 的探测入站(64 个固定账号槽位,路由规则把槽位映射到各节点出站,规则热更新不重启),探测器经该入站对每个节点请求 `http://cp.cloudflare.com/generate_204`(可用设置 `probe.url` 覆盖),量到的是真实出口连通与延迟,WireGuard 等 UDP 协议不再永远显示"不可达"。未纳管 Xray(开发模式)或超出槽位的节点退回 TCP 探测。节点列表显示探测方式与时间,失败原因在悬停提示里。

## [2.0.0-rc.8] - 2026-10-21

- 托管主机:同一主机可同时部署 WireGuard 和 VLESS-REALITY。原先注册节点只按主机复用上一次成功部署的节点,第二种服务会把第一种的节点改写掉;现在只复用同类型部署的节点,不同类型各自一个节点(重名时加「(WireGuard)/(VLESS)」后缀)。
- 托管主机密钥变化(重装、重新生成)时,「检查连接」会弹出对比旧/新指纹的重新信任确认,不必再找 API;部署对话框的失败原因也会提示。

## [2.0.0-rc.7] - 2026-10-21

- 规则编辑器的 GeoSite / GeoIP 标签改为从已安装的 GeoData 读取并多选(可搜索、回车添加、chip 移除;每个标签生成一条规则),编辑单条规则时为单选;未安装 GeoData 时退回手工输入。新增 `GET /geo/tags?kind=`。

## [2.0.0-rc.6] - 2026-10-21

- 控制面重启后若 FRR 配置与路由都无需变更,总览会把 FRR 显示为"已禁用"(模式只在 Apply 时记录);现在 Plan 阶段也记录模式。

## [2.0.0-rc.5] - 2026-10-21

- 自升级第二处修正:旧包的 prerm 在升级时 stop 服务,把正在等 dpkg 的进程杀掉,服务升级后停着不起;dpkg 输出接在进程管道上也随之失败,包停在 half-configured。现在 prerm/postinst 在 `EDGEROUTEGW_SELFUPDATE=1` 下都不碰服务,dpkg 输出写文件,升级目录由延迟重启任务清理。**从 rc.4 及更早版本用页面自升级到本版时仍会碰到旧 prerm,升级后需手动 `systemctl start edgeroutegw`;之后的版本不再有此问题。**

## [2.0.0-rc.4] - 2026-10-21

- 自升级(系统 → 组件更新 → EdgeRouteGW):`dpkg -i` 原先在服务进程内执行,被 unit 的 `ProtectSystem=strict` 挡在只读的 /var/lib/dpkg 外;现在交给 `systemd-run` 瞬态 unit 执行,postinst 在该场景下不再自行重启,仍由延迟任务重启服务。
- 跑 pre-release 的网关在检查新版本时也会看到 pre-release,不再报"no 2.x release found"。

## [2.0.0-rc.3] - 2026-10-21

在 PVE 实验环境(ROS CHR 7.23 主路由 + VLESS Reality 上游 + 1.x v1.8.0 Mode C 网关)做离线包升级验收时发现并修复:

- OSPF 发布路由改写内核 table 200:原先与 TPROXY 的 `local default dev lo`(table 100)同表,更具体的 /32 unicast 路由会把打标流量从本地 socket 上抢走。升级自 rc.2 的网关会自动清空旧表并换到新表。
- 服务网卡未配置时(1.x 库里没有这项),FRR 配置缺 `network ... area 0`,OSPF 邻居建不起来。现在导入时按默认路由网卡补齐,渲染时也有同样的兜底并在状态里给出提示。
- `ip rule` 查重改用 `ip -N` 的数字输出:`/etc/iproute2/rt_tables` 把 100 命名为 tproxy 时每次 apply 都会重复加一条规则;旧的重复规则会被收敛。
- `edgeroutegw-components` 的 postinst 发 SIGHUP 时主进程可能还没注册处理器,导致首启被杀(靠 systemd 重启兜底)。处理器现在在启动最早注册,unit 改为 `Restart=always`(systemd 把死于 SIGHUP 算作正常退出,`on-failure` 不会拉起),postinst 也会兜底启动;内置包种子化提前到 geo 读取之前,不再有"geosite.dat not installed"的误报。
- 解析器:域名在 Mosdns 重启瞬间解析失败且尚无旧答案时,改为 10s 起指数退避重试,不再等 5 分钟。

## [2.0.0-rc.2] - 2026-10-20

- 离线安装:发布附带 `edgeroutegw-components`(经上游校验和验证的 Xray / Mosdns / GeoData,版本钉在 `packaging/components.lock`)和 `edgeroutegw-full_<版本>_<架构>.tar.gz`(两个 .deb + `install-offline.sh`)。启动时或收到 SIGHUP 时自动把内置组件种子化到数据目录。
- 组件下载通道:下载代理(http/socks5,可指向网关自身隧道)、GitHub 镜像前缀、本地上传官方压缩包安装(可选 SHA-256 校验)。

## [2.0.0-rc.1] - 2026-10-20

从零重写。设计见 `docs/DESIGN-2.0.md`,与 1.x 的差异见 `docs/UPGRADE-FROM-1.x.md`。

### 架构
- 声明式下发:写操作只改期望状态,单一 Reconciler 渲染 Xray / Mosdns / nftables / 路由四类产物并按组件做最小动作(热更新、写文件、重启、原子换表),300ms 合并窗口,失败退避重试,30s 周期对齐。
- 出站组取代 `bal-ha-a-b` 字串;规则集、规则、设备策略、绕过目标分表;版本化 SQL 迁移;凭证 AES-256-GCM 加密,主密钥永不进仓库。
- 纯 Go 静态二进制(modernc sqlite),.deb 发布,前端内嵌;仓库不再提交 Xray/Mosdns 二进制,组件在 UI 内校验下载。
- systemd 走 D-Bus;OSPF 路由用 netlink 写内核 table 200,FRR `ip import-table` 导入;DNS 用 miekg/dns 查本机 Mosdns。

### 功能
- 三种模式、模式向导与切换预检(受保护端点、通配规则、网卡、节点)。
- 鉴权:argon2id、HttpOnly 会话 + CSRF、API token、登录限速、引导密码。
- 前端:总览、节点(含出站组、分享链接导入、托管主机)、分流(实时 Trace)、DNS、网络与模式(OSPF 面板)、观测(实时连接、事件、变更历史、下发记录)、系统(安全、组件更新、生成配置)。
- 托管主机:SSH 指纹钉定、WireGuard / VLESS-REALITY 一键部署、REALITY 握手自检、实时日志、重新下发。
- 组件更新:Xray / Mosdns / GeoData / EdgeRouteGW 本体,校验、冒烟、回退、GeoData 定时。
- `import-v1`:从 1.x 数据库导入;备份导出/恢复;`doctor`。
- 变更历史可回滚到任一 generation;流量采样与图表;节点延迟探测;Playwright E2E。

### 与 1.x 的行为差异
- strict 故障转移改为拦截而不是直连。
- Mode C 的域名只发布解析出的 /32,不再做 geoip 网段提升。
- 下发失败不回滚数据库;组件标记降级并自动重试。
