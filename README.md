# EdgeRouteGW 2.0(v2 分支)

这是 EdgeRouteGW 2.0 的开发分支,从零搭建,不继承 1.x 代码。设计文档见 [docs/DESIGN-2.0.md](docs/DESIGN-2.0.md),API 见 [docs/API-v2.md](docs/API-v2.md)。

- 1.x 代码与发布在 `main` / `v1` 分支与 `v1.x.y` 标签。
- 2.0 的实施按设计文档第 6 节 M0 到 M5 推进。

## 进度

| 阶段 | 状态 | 内容 |
|---|---|---|
| M0 骨架 | 完成 | cmd/internal 分层、SQLite 迁移、`/api/v2/state`、CI、.deb 打包 |
| M1 Reconciler(Mode A) | 完成 | render(xray/mosdns/nft 纯函数渲染)、Reconciler(合并窗口、退避重试、dry-run)、xray/mosdns/nft/systemd Driver、节点/出站组/规则集/规则/设备策略/绕过/DNS/网络 API、SSE 事件流、分享链接导入 |
| M2 前端 Alpha | 未开始 | Vite 工程、鉴权、总览/节点/分流/观测 |
| M3 Mode B/C | 未开始 | netlink 路由、FRR、解析器、模式向导 |
| M4 托管主机与更新 | 未开始 | |
| M5 发布 | 未开始 | .deb、1.x 导入、迁移 |

## 开发

```bash
make build          # 产出 bin/edgeroutegw(CGO_ENABLED=0 静态二进制)
make test           # go vet + go test -race
make run            # 以 127.0.0.1:18080 与 ./.dev-data 启动
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

Xray 与 Mosdns 以 `edgeroutegw-xray.service` / `edgeroutegw-mosdns.service` 运行,后端经 D-Bus 控制;nftables 只操作 `table inet edgeroutegw`,不会 `flush ruleset`。
