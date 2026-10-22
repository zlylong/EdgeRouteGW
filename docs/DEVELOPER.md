# 开发者与架构指南

## 1. 架构

```
UI (Vue 3 + TanStack Query)  ──HTTP/SSE──>  edgeroutegw serve (Go, 单二进制)
                                              │
                      ┌───────────────────────┼─────────────────────────┐
                      │ store (SQLite)        │ reconcile               │ observe / update / provision / tune
                      │ 期望状态 + generation │ snapshot → render →     │ 流量采样、连接跟踪、DNS tap、
                      │ changesets / events   │ drivers(diff + apply)  │ 延迟探测、组件更新、托管主机、调优
                      └───────────────────────┴─────────────────────────┘
                                              │
                 xray · mosdns · nftables · frr+route(table 200) · wg(netlink, table 3xx)
```

- **期望状态**:节点、出站组、规则集 / 规则、设备策略、绕过目标、网络与 DNS 配置都存在 SQLite 里;每次写入经 `mutate` 事务记录一条 changeset 并把 `generation` 加一。
- **协调器**(`internal/reconcile`):监听 generation 变化,取一份 `desired.Snapshot`,由 `internal/render` 渲染出全部产物(Xray JSON、Mosdns YAML + 域名列表、nftables 规则集、FRR 配置、路由前缀、WireGuard 接口定义),再交给各 driver 做 diff 与 apply。Xray 的出站与路由能热更新(gRPC API),结构变了才重启;nftables 用 `nft -f` 原子替换;失败写事件并退避重试,总览页显示最近一次下发错误。
- **驱动**(`internal/driver/*`):每个 driver 实现 Plan / Apply,外部命令全部走 `execx.Runner` 分参数调用(可在测试里替换);`wg` driver 用 netlink + wgctrl 直接配内核接口。
- **渲染规则**(`internal/rulespec`):域名模式、geo 标签、IP / CIDR、MAC、上游地址的校验与转换,API 校验和渲染共用同一套实现,所以两边语义一致。
- **认证**(`internal/auth`):单管理员,argon2id;会话 cookie(HttpOnly + SameSite=Strict)+ CSRF 双提交;API token `ergw_…` 只存 sha256,scope read / write;初始密码会话只能改密。
- **可观测**(`internal/observe`):Xray access log 尾随成连接环形缓冲;`xray api statsquery` 每 10 秒采样流量;DNS tap 从 nflog 组 100 读取 Mosdns 的应答把 IP 关联到域名;主机指标读 `/proc`;SSE `/api/v2/events/stream` 推状态、下发、连接、流量、更新进度。

## 2. 代码布局

| 目录 | 内容 |
|---|---|
| `cmd/edgeroutegw` | 入口:`serve`、`import-v1`、`doctor`、`version` |
| `internal/api/v2` | HTTP 路由与 handler(stdlib ServeMux),`v2.go` 注册全部路由 |
| `internal/store` | SQLite(modernc)、嵌入式迁移 `migrations/000N_*.sql`、内置规则集种子 |
| `internal/model` | 领域模型(Node、Rule、RuleSet、OutboundGroup、DNSConfig、NetworkConfig…) |
| `internal/desired` | 从 store 取快照 |
| `internal/render` | 渲染:`xray.go`、`mosdns.go`、`nft.go`、`frr` / `routes.go`、`wg.go` |
| `internal/driver` | xray、mosdns、nft、frr、route、wg、systemd |
| `internal/reconcile` | 协调循环 |
| `internal/routes`、`internal/resolver` | Mode C 的域名解析与前缀计算、发布校验 |
| `internal/geo` | geosite / geoip 解析与反查索引 |
| `internal/update` | 组件下载校验安装、自升级、定时、内置包、上传、下载通道 |
| `internal/provision` | 托管主机 SSH 部署脚本、主机密钥、调优脚本 |
| `internal/tune` | 环境探测式调优 |
| `internal/observe` | 连接、流量、延迟、DNS tap、主机指标 |
| `internal/migrate1x` | 1.x `proxygw.db` 导入 |
| `internal/secret` | 主密钥与 AES-GCM |
| `web/` | Vue 3 + TypeScript + Tailwind 前端,`src/api` 为类型化客户端 |
| `packaging/` | nfpm 配置、systemd 单元、postinst / prerm、sysctl、modules-load、离线安装脚本 |
| `scripts/` | `install.sh`、`fetch-components.sh`、`e2e.sh` |
| `docs/` | 文档 |

## 3. 构建与测试

依赖:Go 1.26、bun 1.3+、Node 20(`vue-tsc` 解析 `.vue` 需要)、nfpm 2.4+、make。

```bash
make build                                   # bin/edgeroutegw(版本号来自 git describe)
cd web && bun install --frozen-lockfile && bun run typecheck && bun run test && bun run build
go vet ./... && go test -count=1 ./...       # 后端单元测试,包含渲染快照、API、导入、调度等
VERSION=2.0.0 ARCH=amd64 nfpm package -f packaging/nfpm.yaml -p deb -t dist/
```

本地跑控制面而不碰系统:`bin/edgeroutegw serve -data-dir ./.dev-data -listen 127.0.0.1:8080 -no-reconcile`(只有 API 和页面,不驱动 Xray / nftables)。`.dev-data` 已在 `.gitignore`,里面有主密钥,别提交。

`scripts/e2e.sh` 在一台真实的 Debian 测试机上走完安装 → 导入 → 三种模式切换 → 自升级;需要 root 和一台能当 OSPF 邻居的路由。

## 4. 打包与发布

- 版本号:`git describe --tags`;deb 版本把 `v2.0.0-rc.3` 写成 `2.0.0~rc.3`。
- 发布:给 `main` 打 `v2.*` 标签并推送,`.github/workflows/release.yml` 构建 amd64 / arm64 的 `edgeroutegw_*.deb`、`edgeroutegw-components_*.deb`(按 `packaging/components.lock` 钉住版本、按上游校验和拉取的 Xray / Mosdns / GeoData)、`edgeroutegw-full_*.tar.gz`(两个 deb + `install-offline.sh`)、裸二进制和 `SHA256SUMS`。
- 自升级依赖 release 资产名和 `SHA256SUMS` 的格式,改动 release.yml 时保持它们。
- deb 的 postinst 在 `EDGEROUTEGW_SELFUPDATE=1` 下不碰服务(自升级时由控制面安排延迟重启),否则 enable + restart。

## 5. 约定

- API 严格 JSON 解码(未知字段报错),错误体 `{"error":{"code","message","details"}}`,校验错误 422 带 `field`。
- 所有写入走 `d.mutate(...)`,产生 changeset 和 generation;备注这类不渲染的字段可以直接写库。
- 凡是回传凭证或渲染后配置的接口只对浏览器会话开放(`sessionOnly`),API token 拿不到。
- 外部命令只用 `exec.Command` 分参数;要进 shell 文本的值必须经 `shellQuote` 或白名单校验。
- 下载先校验再解压;替换二进制先写 `.new` 再换名;保留 `.prev`。
- 新增 sysctl / 模块 / 能力时同步更新 `packaging/systemd/*.service` 的沙箱设置,并在测试机上验证单元能启动。
- 前端模板属性值里不要出现 `//`(vue-tsc 会把它当注释,报 "Cannot find name");Select 下拉的选项数组要用 computed 保持稳定。
- 提交信息用英文、说明动机;用户可见的文案用中文。

## 6. 安全模型(摘要)

控制面以 root 运行(需要 nftables、策略路由、netlink、systemd),通过 CapabilityBoundingSet、ProtectSystem=strict、ProtectKernelModules、RestrictAddressFamilies、UMask=0027 等收紧;Xray / Mosdns 各自独立单元、最小能力集。审计结论与待办见 [SECURITY-AUDIT.md](SECURITY-AUDIT.md),2.1 计划:专用用户 + polkit、SHA256SUMS 签名、内置 TLS、导入 / 恢复校验下沉到 model 层。
