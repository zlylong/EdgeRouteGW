# EdgeRouteGW 2.0 收尾安全审计(2026-10-22)

对象:v2 分支 2.0.0-rc.22(commit 65e4954 之后)。方式:三路并行的只读代码审计,分别覆盖 HTTP/API 与认证面、本地命令执行与托管主机部署面、更新链与打包。每条结论都对应到代码位置;未读到代码的推断单独列在「待实机验证」。

状态列:**已修** = 本次随 rc.23 修复;**待修** = 记入 2.1 计划;**设计取舍** = 保持现状并写明原因。

## 高

| # | 发现 | 位置 | 状态 |
|---|---|---|---|
| H1 | 只读 API token 可导出全部节点明文凭证:read scope 只限制 HTTP 方法,`GET /backup?include_secrets=1` 和 `GET /artifacts/xray`(渲染后的 Xray 配置含 UUID / 密码 / WireGuard 私钥)对 token 全开。一枚泄露的只读 token 等于全部机场凭证泄露。 | `internal/auth/service.go` Middleware;`internal/api/v2/backup.go` exportBackup;`internal/api/v2/state.go` handleArtifact | 已修:两处只允许浏览器会话,含凭证导出写 warn 事件 |
| H2 | 镜像前缀同时接管制品、校验值与版本列表,且允许 http://。恶意或被劫持的镜像可同时伪造 zip 与 .dgst 通过校验,冒烟测试以 root 执行即 RCE;自升级的 SHA256SUMS 同理。 | `internal/update/github.go` via()/Text();`internal/update/self.go` | 部分已修:镜像只接受 https;校验文件优先直连 GitHub,镜像仅兜底。**待修**:SHA256SUMS 签名(minisign)并内置公钥、禁止自升级降级 |
| H3 | GitHub Token 以 Bearer 头发给镜像站;`GET /download-settings` 明文回传 token,只读 token 也能读。 | `internal/update/github.go` getJSON;`internal/api/v2/components.go` | 已修:配置镜像时不发 token;GET 只回 `has_github_token`,PUT 留空保持、`clear_github_token` 清除。**待修**:token 经 Keyring 加密落库(现为 settings 表明文) |

## 中

| # | 发现 | 位置 | 状态 |
|---|---|---|---|
| M1 | REALITY 部署参数 `server_name` / `dest` 不校验直接拼进远端 bash 双引号字符串,`$(...)` 可在托管主机以 root 执行;参数加密留存,回滚时再次触发。 | `internal/provision/scripts.go` 冒烟测试段;`internal/provision/keys.go` NewVlessParams | 已修:主机名 / host:port 白名单校验,脚本改单引号 |
| M2 | nft 只放行私网目的,网关若持有公网地址(云主机、PPPoE、Mode B/C 上联口),外网发往该 IP 任意端口的 TCP/UDP 都被 TPROXY 进 Xray:所有端口在握手层"开放",兜底为代理时还会经节点回连自己形成放大。 | `internal/render/nft.go` prerouting / dns_hijack | 已修:两条链开头 `fib daddr type local return` |
| M3 | LAN 内直接连 `<网关>:12345`(TPROXY 端口)可让 Xray 把自己当目标反复自连,耗尽 fd(推断,见待验证 A)。 | `internal/render/xray.go` tproxy_in 监听 `::`;nft 无 input 链 | 已修:新增 input 链,只有带 TPROXY 标记的包能到该端口 |
| M4 | Mosdns 监听 `:53`、TPROXY 监听 `::`,全接口且无 input 过滤;有 WAN 地址时 Mosdns 成开放递归(上游还经代理节点出去)。 | `internal/render/mosdns.go`;`internal/render/xray.go` | TPROXY 部分已修(M3);**待修**:Mosdns 改监听 LAN 口 / 回环 + 网关 LAN IP,或 input 链按 iifname 限制 53(需要可靠的 WAN 口判定) |
| M5 | 管理面全程明文 HTTP,无 TLS 路径,`ForceSecure` 未接线;同 LAN 即可嗅探密码、会话与 H1 的凭证导出。 | `internal/httpserver`;`internal/config` | 部分已修:`-secure-cookies` / `ERGW_SECURE_COOKIES` 接线,供 TLS 反代场景。**待修(2.1)**:内置 TLS(自签 + 可选 ACME) |
| M6 | 组件上传没有总请求体上限:`ParseMultipartForm(32MB)` 只是内存阈值,超出部分无限落盘到 /tmp,512 MB 检查发生在整个 body 落盘之后。 | `internal/api/v2/components.go` uploadComponent | 已修:`MaxBytesReader` 600 MB |
| M7 | 解析结果可把私网 /32 发布进 OSPF:validate 只拒 RFC1918 超网,被投毒的 DNS 答案指向 192.168.x.y 就会把该 LAN 主机的流量吸进网关(Mode C)。 | `internal/routes/engine.go` validate;`internal/resolver/resolver.go` | 已修:私网 / CGNAT / 0.0.0.0/8 任何前缀拒绝,解析器丢弃 bogon 答案 |
| M8 | 本地上传留空 sha256 时 UI 承诺"记录事件",实际只写日志。 | `internal/update/bundle.go` runUploadJob | 已修:`upload.unverified` / `upload.installed` / `upload.failed` 事件 |
| M9 | 自升级安排重启失败只打 Warn,任务仍显示成功,磁盘已是新版本而进程仍是旧版本;固定单元名会撞上前一次;暂存目录永久遗留。 | `internal/update/self.go` installSelf | 已修:失败即报错并清理,单元名带时间戳 |
| M10 | 1.x 导入与备份恢复绕过模型校验(节点端口、地址、接口名等直接落库再渲染),坏数据可让整条 apply 链失败或进入 FRR 配置。 | `internal/migrate1x/import.go`;`internal/api/v2/backup.go` restoreBackup;`internal/api/v2/nodes.go` importNodes | **待修(2.1)**:把 NodeInput / Network / DNS 校验下沉到 model 层,三条入口复用 |
| M11 | 数据库文件按进程 umask 创建(0644),目录由 StateDirectory 默认 0755;库内有明文 GitHub token、代理 URL、会话 ID。 | `packaging/systemd/edgeroutegw.service`;`internal/store/store.go` | 已修:UMask=0027、StateDirectoryMode=0750、RuntimeDirectoryMode=0750;store.Open 对 db/-wal/-shm 显式 chmod 0600 |

## 低

| # | 发现 | 状态 |
|---|---|---|
| L1 | 强制改密只在前端路由守卫,用初始密码直接调 API 畅通无阻,`bootstrap_password.txt` 无限期留在盘上 | 已修:中间件对 bootstrap 会话除 `/auth/*` 外返回 403 |
| L2 | 改密不作废已签发的 API token,会话被盗期间签发的 token 在改密后仍有效 | 已修:改密默认吊销全部 token(`keep_tokens: true` 可保留) |
| L3 | CSRF 为纯双提交 cookie,token 与会话无绑定,目前靠 SameSite=Strict 兜底 | **待修**:csrf 入 sessions 表或 HMAC(sessionID) |
| L4 | 多处把内部错误原文回给客户端(数据目录路径、SSH banner) | **待修**:统一走 fail() 记日志,对外稳定 code |
| L5 | 无 ReadTimeout,未认证的 login 端点可被慢速 body 占连接;无连接数上限 | **待修**:login 用 MaxBytesReader + 超时,全局连接数限制 |
| L6 | 登录限速器仅内存、按源 IP,重启清零,IPv6 换地址即绕过 | **待修** |
| L7 | 下载体积无上限;SwapFile 先挪走旧文件再拷贝,窗口期目标文件缺失、失败不回滚 | 已修:1 GB 上限 + Content-Length 预检;先写完 .new 再换名,失败回滚 |
| L8 | 代理 / 镜像 URL 可指向内网(受限 SSRF,响应只按 JSON/文本解析) | 设计取舍:代理允许 127.0.0.1(网关自己的隧道);镜像限 https 后风险可接受 |
| L9 | `/run/edgeroutegw` 下的 Xray access log 含全部 LAN 客户端 IP 与访问目标,目录 0755 | 已修:RuntimeDirectoryMode=0750 |
| L10 | install.sh / install-offline.sh 把初始密码 `cat` 到 stdout,会进 CI/终端日志 | **待修**:只提示文件路径 |
| L11 | 版本正则允许 `..`(GitHub 侧会规范化,当前无害) | **待修**:禁止连续点 |
| L12 | 分享链接导入不校验端口范围 / 名称长度 / WireGuard 地址,坏节点让 `xray -test` 拒绝整份配置、内核 WG 留下半配置接口 | **待修**:与 M10 一起下沉校验 |
| L13 | 接口名只拒绝 `>15` 字符与空格斜杠,换行可进 frr.conf | 已修:`^[A-Za-z0-9_.-]{1,15}$` 且禁 `..` |
| L14 | 远端 `/usr/local/etc/xray` 以默认 umask 创建,config.json 落盘到 chmod 600 之间有全局可读窗口 | 已修:`install -d -m 0700` |
| L15 | 导出含明文凭证的备份没有审计事件 | 已修(随 H1) |
| L16 | 托管主机只在换 SSH host 时清主机密钥指纹,换端口不清;首次钉死无事件 | 已修:换端口也清。**待修**:首次钉死写事件并在 UI 展示指纹 |
| L17 | 内核 WireGuard 删除时按 fwmark 删 ip rule 不看 table,会误删运维手工加的同标记规则 | 已修:同时要求本接口的路由表 |
| L18 | 内核 WireGuard 端点若是域名,在 reconcile 内同步解析,Mosdns 未起时拖慢整轮 | **待修**:渲染期解析或要求端点为 IP |
| L19 | systemd 单元缺 ProtectKernelLogs / ProtectClock / ProtectHostname / RestrictNamespaces / SystemCallArchitectures / RestrictAddressFamilies / UMask;Xray、Mosdns 单元缺 ProtectKernelTunables 等一批 | 已修(见 CHANGELOG)。**待实测**:主单元去掉 CAP_SYS_ADMIN(D-Bus 重启对 uid 0 按 polkit 放行,应不需要)、Xray/Mosdns 加 PrivateDevices、SystemCallFilter=@system-service |

## 实机验证(2026-10-22,测试环境 Edge 192.168.100.199,Mode A)

- 直连 `<网关>:12345`:nettest 上 `nc -z` 失败,input 链 `tproxy_port_direct` 计数器 +3;同时 google 经代理 204、baidu 直连 200、`dig @8.8.8.8` 仍被 Mosdns 接管、网关 UI 正常。
- 三份加固后的单元均正常启动,journal 无 warning;`RestrictAddressFamilies`、`UMask=0027`、`StateDirectoryMode=0750` 已生效,`/var/lib/edgeroutegw` 与 `/run/edgeroutegw` 为 0750;`edgeroutegw doctor` 无 FAIL。

## 待实机验证

- **A. TPROXY 端口自连环路**:LAN 主机 `nc <网关> 12345` 后观察 `ss -tnp | grep 12345` 是否持续增长。input 链已按最坏情况加上,验证只是确认推断。
- **B. systemd 对已存在的 StateDirectory 是否每次启动重设 mode**(影响 M11 的严重度;StateDirectoryMode=0750 两种情况下都正确)。
- **C. 去掉 CAP_SYS_ADMIN 后自升级(systemd-run)与单元重启是否正常**。

## 核实过且无问题的要点

密码 argon2id(m=64MiB, t=3);会话 ID 32 字节随机、登录新建、注销真删、24h 滑动 + 7d 绝对;API token sha256 存储、Bearer 无效不回落 cookie;SSE 受认证;未认证路由只有 `/healthz`、login、`/auth/me`、静态资源;CSRF 对所有非安全方法(含 multipart)生效;JSON 严格解码 + 1 MB 上限;SQL 全参数化;路径参数白名单;不信任 X-Forwarded-For;日志不记密码 / token;前端无 v-html;CSP `default-src 'self'`、frame-ancestors none、nosniff、XFO DENY;本地命令全部 `exec.Command` 分参数,唯一的 `sh -c` 参数全部 shellQuote 且版本号受白名单;nft / FRR / Mosdns 中的用户值全部经 rulespec 规范化或 JSON 编码;凭证 AES-GCM,主密钥 0600,配置原子写;下载校验先于解压、flat 解压防穿越、.prev 回退、冒烟测试;postinst 不放宽已有目录权限;SSH 主机密钥 TOFU 且变更时拒绝而非静默覆盖。

## 总体评价

认证、会话、注入防护与下载校验这些"常见坑"在 2.0 里做得扎实;真正的结构性缺口集中在三处:授权粒度(read scope 只按方法判定,让只读 token 能读到所有秘密)、镜像被当作完全信任根、以及 nft 与监听面没有"网关自身地址 / 非 LAN 口"的边界。这三处都已在 rc.23 修掉或收口。2.1 的安全待办按优先级:SHA256SUMS 签名、内置 TLS、校验下沉到 model 层(导入 / 恢复 / 分享链接)、Mosdns 监听面、token 加密落库、CSRF 绑定会话。
