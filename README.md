# EdgeRouteGW 2.0(v2 分支)

这是 EdgeRouteGW 2.0 的开发分支,从零搭建,不继承 1.x 代码。设计文档见 [docs/DESIGN-2.0.md](docs/DESIGN-2.0.md),API 见 [docs/API-v2.md](docs/API-v2.md)。

- 1.x 代码与发布在 `main` / `v1` 分支与 `v1.x.y` 标签。
- 2.0 的实施按设计文档第 6 节 M0 到 M5 推进。

## 进度

| 阶段 | 状态 | 内容 |
|---|---|---|
| M0 骨架 | 完成 | cmd/internal 分层、SQLite 迁移、`/api/v2/state`、CI、.deb 打包 |
| M1 Reconciler(Mode A) | 完成 | render(xray/mosdns/nft 纯函数渲染)、Reconciler(合并窗口、退避重试、dry-run)、xray/mosdns/nft/systemd Driver、节点/出站组/规则集/规则/设备策略/绕过/DNS/网络 API、SSE 事件流、分享链接导入 |
| M2 前端 Alpha | 完成 | 鉴权(argon2id、cookie 会话 + CSRF、API token、登录限速、引导密码)、Vite + Vue3 + TS + Tailwind v4 + reka-ui 前端:登录/强制改密、总览、节点(含出站组、分享链接导入)、分流(规则集、拖拽排序、实时 Trace、设备策略、绕过)、观测(实时连接、事件、变更历史、下发记录)、系统(会话、token、生成配置);顶栏变更状态指示器;Xray access log 连接追踪;`/trace` |
| M3 Mode B/C | 完成 | 路由发布引擎(候选/策略/受保护端点/剪枝/preflight)、miekg/dns 解析器与 TTL 缓存、netlink 路由驱动(table 100)、FRR 驱动(frr.conf/daemons/生命周期/邻居)、`/ospf` `/routes` `/interfaces` `/network/preflight` 接口、网络与模式页(三模式向导 + 预检、网卡与参数、OSPF 面板)、DNS 页 |
| M4 托管主机与更新 | 完成 | SSH 一键部署 WireGuard / VLESS-REALITY(指纹钉定、脚本走 stdin、REALITY 握手自检、实时日志、历史与重新下发、自动注册出站节点);Xray/Mosdns/GeoData 校验下载、冒烟、`.prev` 回退、GeoData 定时;systemd 单元 `edgeroutegw-xray/mosdns`;前端托管主机 tab 与组件更新 tab |
| M5 发布 | 未开始 | .deb、1.x 导入、迁移 |

## 开发

```bash
make web            # 构建前端到 internal/web/dist(需要 bun)
make build          # 产出 bin/edgeroutegw(CGO_ENABLED=0 静态二进制,内嵌前端)
make test           # go vet + go test -race
make web-check      # 前端 typecheck + vitest
make dev-api        # 后端:127.0.0.1:18080,不驱动组件
make dev-web        # 前端:Vite 5173,代理 /api 到 18080
```

```bash
bin/edgeroutegw version
bin/edgeroutegw migrate -data-dir /var/lib/edgeroutegw
bin/edgeroutegw serve -listen :80 -data-dir /var/lib/edgeroutegw
bin/edgeroutegw serve -no-reconcile -listen 127.0.0.1:18080 -data-dir ./.dev-data   # 只跑 API,不碰 Xray/Mosdns/nft
```

环境变量与同名 flag 等价:`ERGW_LISTEN`、`ERGW_DATA_DIR`、`ERGW_LOG_LEVEL`。

## 运行时布局

```
/var/lib/edgeroutegw/
├── edgeroutegw.db            SQLite(WAL)
├── keys/master.key           凭证加密主密钥(0600,永不进备份导出)
├── components/{xray,mosdns}/ 下载的二进制(M4)
├── geodata/{geosite,geoip}.dat
├── xray/config.json          渲染产物(0600)
├── mosdns/{config.yaml,proxy_domains.txt,geosite_cn.txt}
└── nft/ruleset.nft
/run/edgeroutegw/             Xray access log、mosdns log
```

Xray 与 Mosdns 以 `edgeroutegw-xray.service` / `edgeroutegw-mosdns.service` 运行,后端经 D-Bus 控制;nftables 只操作 `table inet edgeroutegw`,不会 `flush ruleset`。Mode B/C 的路由写进内核 table 100(proto 200),FRR(`frr.service`,需 `apt install frr`)通过 `/etc/frr/frr.conf` 的 `ip import-table 100` 与 `redistribute table 100` 宣告;语法已在 FRR 8.4 上用 `vtysh -C` 验证。
