# EdgeRouteGW 2.0 变更记录

## [2.0.0-rc.1] - 未发布

从零重写。设计见 `docs/DESIGN-2.0.md`,与 1.x 的差异见 `docs/UPGRADE-FROM-1.x.md`。

### 架构
- 声明式下发:写操作只改期望状态,单一 Reconciler 渲染 Xray / Mosdns / nftables / 路由四类产物并按组件做最小动作(热更新、写文件、重启、原子换表),300ms 合并窗口,失败退避重试,30s 周期对齐。
- 出站组取代 `bal-ha-a-b` 字串;规则集、规则、设备策略、绕过目标分表;版本化 SQL 迁移;凭证 AES-256-GCM 加密,主密钥永不进仓库。
- 纯 Go 静态二进制(modernc sqlite),.deb 发布,前端内嵌;仓库不再提交 Xray/Mosdns 二进制,组件在 UI 内校验下载。
- systemd 走 D-Bus;OSPF 路由用 netlink 写内核 table 100,FRR `ip import-table` 导入;DNS 用 miekg/dns 查本机 Mosdns。

### 功能
- 三种模式、模式向导与切换预检(受保护端点、通配规则、网卡、节点)。
- 鉴权:argon2id、HttpOnly 会话 + CSRF、API token、登录限速、引导密码。
- 前端:总览、节点(含出站组、分享链接导入、托管主机)、分流(实时 Trace)、DNS、网络与模式(OSPF 面板)、观测(实时连接、事件、变更历史、下发记录)、系统(安全、组件更新、生成配置)。
- 托管主机:SSH 指纹钉定、WireGuard / VLESS-REALITY 一键部署、REALITY 握手自检、实时日志、重新下发。
- 组件更新:Xray / Mosdns / GeoData / EdgeRouteGW 本体,校验、冒烟、回退、GeoData 定时。
- `import-v1`:从 1.x 数据库导入。

### 与 1.x 的行为差异
- strict 故障转移改为拦截而不是直连。
- Mode C 的域名只发布解析出的 /32,不再做 geoip 网段提升。
- 下发失败不回滚数据库;组件标记降级并自动重试。
