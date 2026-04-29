# ProxyGW 后端组件关系说明图

> 目标：给出 backend 关键组件的调用关系与数据流，便于后续重构时按层切分（Controller / Service / Repository / Runtime Adapter）。

## 1) 总体关系图（Mermaid）

```mermaid
flowchart TB
  %% ==== UI / API ====
  UI[Web UI / API Client]
  API[api_routes.go\n统一路由入口]

  AuthC[auth_controller.go]
  RuleC[rules_controller.go]
  NodeC[nodes_controller.go]
  DNSC[dns_controller.go]
  SysC[system_controller.go]
  OSPFC[ospf controller services]
  ApplyC[apply_controller.go]

  %% ==== Service Layer ====
  AppS[app_service.go]
  SystemS[system_service.go]
  MosdnsS[mosdns_service.go\nmosdns_runtime_service.go]
  XrayS[xray_service.go\nxray_*_dynamic.go]
  OSPFS[ospf_sync_service.go\nospf_apply_service.go\nospf_reconcile_service.go\nospf_publish_policy.go]
  CronS[cron_domain_service.go]
  WG[wireguard_service.go]

  %% ==== Repository Layer ====
  AppR[app_repository.go]
  RuleR[rules_repository.go]
  NodeR[nodes_repository.go]
  DNSR[dns_repository.go]
  SysR[system_repository.go]
  ProtR[protected_ip_repository.go]
  RemoteR[remote_nodes_repository.go]

  %% ==== Runtime/Infra ====
  DB[(SQLite\nconfig/proxygw.db)]
  CmdExec[command_executor.go]
  Nft[nftables_service.go\nnftables_monitor.go]
  FRR[ospf_frr_adapter.go\n+ vtysh]
  XRAY[(Xray Core)]
  MOSDNS[(Mosdns)]

  %% ==== Entry ====
  UI --> API
  API --> AuthC
  API --> RuleC
  API --> NodeC
  API --> DNSC
  API --> SysC
  API --> OSPFC
  API --> ApplyC

  %% ==== Controller -> Service ====
  AuthC --> AppS
  RuleC --> AppS
  NodeC --> AppS
  DNSC --> AppS
  SysC --> SystemS
  OSPFC --> OSPFS
  ApplyC --> AppS

  %% ==== Service -> Repo ====
  AppS --> AppR
  AppS --> RuleR
  AppS --> NodeR
  AppS --> DNSR
  AppS --> ProtR
  AppS --> RemoteR
  SystemS --> SysR
  OSPFS --> SysR

  %% ==== Repo -> DB ====
  AppR --> DB
  RuleR --> DB
  NodeR --> DB
  DNSR --> DB
  SysR --> DB
  ProtR --> DB
  RemoteR --> DB

  %% ==== Service -> Runtime ====
  AppS --> XrayS
  AppS --> MosdnsS
  AppS --> OSPFS
  AppS --> CmdExec

  OSPFS --> FRR
  OSPFS --> Nft
  OSPFS --> CmdExec

  XrayS --> XRAY
  MosdnsS --> MOSDNS
  OSPFS --> CronS
  AppS --> WG

  %% ==== System ====
  SystemS --> CmdExec
```

## 2) 核心链路说明

### A. 规则变更链路
1. `rules_controller.go` 接收 `/api/rules` 请求
2. 进入 `app_service.go` 聚合流程
3. `rules_repository.go` 写入 SQLite
4. 根据模式触发：
   - Xray 动态路由更新（`xray_*_dynamic.go`）
   - Mosdns 域集更新（`mosdns_service.go`）
   - OSPF 候选/发布集合重算（`ospf_*_service.go`）

### B. 模式切换链路（A/B/C）
1. Controller 进入 `app_service.go` / `ospf_controller_service.go`
2. DB 持久化模式与相关设置
3. 按模式组合刷新 Xray / Mosdns / FRR / nftables
4. `ospf_reconcile_service.go` 负责发布集与 FRR 实际状态对齐

### C. 系统状态与运维链路
1. `system_controller.go` -> `system_service.go`
2. 聚合 DB 统计 + `command_executor.go` 系统命令输出
3. 返回仪表盘、事件日志、路由状态等视图

## 3) 分层重构建议（与现状兼容）

- Controller 保持“参数校验 + HTTP 编排”，不直接触碰运行时命令。
- Service 聚合业务事务（规则/模式/应用流程），统一调用 Runtime Adapter。
- Repository 只负责 SQL 与实体映射。
- Runtime Adapter（Xray/Mosdns/FRR/nftables）统一走 `command_executor.go`，便于门禁、审计和 dry-run。

---

如需可视化静态图（SVG/PNG），可将上面的 Mermaid 直接导入支持 Mermaid 的 Markdown 渲染器，或我可以再生成一份深色主题 HTML 架构图。