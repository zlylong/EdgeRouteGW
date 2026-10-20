# EdgeRouteGW 2.0(v2 分支)

这是 EdgeRouteGW 2.0 的开发分支,从零搭建,不继承 1.x 代码。设计文档见 [docs/DESIGN-2.0.md](docs/DESIGN-2.0.md)。

- 1.x 代码与发布在 `main` / `v1` 分支与 `v1.x.y` 标签。
- 2.0 的实施按设计文档第 6 节 M0 到 M5 推进;当前阶段:**M0 骨架**。

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
```

环境变量与同名 flag 等价:`ERGW_LISTEN`、`ERGW_DATA_DIR`、`ERGW_LOG_LEVEL`。
