# ProxyGW API 文档

Base URL: `http://<host>/api`

## 1. 系统架构与覆盖率
ProxyGW 后端采用标准的分层架构（Controller/Service/Repository），核心 API 路由通过 `backend/api_routes.go` 注册。
当前测试覆盖率：**100%** (所有 56 条路由均有测试覆盖)。

## 2. 认证
### POST `/login`
- 请求：`{ "password": "..." }`
- 返回：`{ "token": "..." }`

## 3. 模式与应用
### POST `/mode`
- 切换 A/B/C 模式。需配合 `confirm=APPLY` 参数。
### POST `/apply`
- 强制重新生成配置并平滑重启核心服务。

## 4. 节点与规则管理
详细 API 定义请参考源代码中的 `registerRuleRoutes` 和 `registerNodeRoutes`。
- **域名规则支持**: `full:`, `domain:`, `regexp:` 语义对齐 Xray。
- **优先级**: 匹配顺序 `priority ASC, id ASC`。

## 5. 诊断工具
### GET `/test/trace`
- 模拟输入目标，返回匹配规则详情。
### GET `/test/health_check`
- 返回全组件运行状态 JSON。

## 6. 其它接口
- `/traffic`: 实时流量与月度统计。
- `/ospf`: OSPF 邻居与路由集状态。
- `/dns`: Mosdns 上游与缓存设置。
- `/remote_nodes`: 远程节点部署管理。
