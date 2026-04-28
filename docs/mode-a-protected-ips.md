# Mode A 规则变更保护 IP 列表

## 目标
在 Mode A 下，修改 Xray 规则可能导致短时连接中断。为降低关键业务中断风险，新增“规则变更保护 IP”列表：
- 列表中的目标 IP/CIDR 强制直连（nftables `return`），不进入 TProxy/Xray。
- 因此在规则变更、Xray 重载期间，这些目标仍保持连通。

## 使用入口
Web UI 新增标签页：`规则变更保护IP`（仅 Mode A 显示）。

## 行为说明
- 仅接受 IPv4 或 IPv4 CIDR。
- 录入单 IP 会规范化为 `/32`。
- 新增/删除后即时热生效（调用 `applyNftablesConfig`）。
- Mode B/C 不展示该页面，也不会启用该集合逻辑。

## 后端接口
- `GET /api/protected_ips`
- `POST /api/protected_ips` body: `{ "value": "1.1.1.1", "remark": "optional" }`
- `DELETE /api/protected_ips/:id`

## 数据表
`protected_ips(id, value UNIQUE, remark, created_at)`

## nftables 生效点
- `set protected_ips`
- `chain prerouting` (Mode A): `ip daddr @protected_ips return`
- `chain output` (Mode A): `ip daddr @protected_ips return`

> 建议将以下目标加入保护列表：运维出口、CI/CD、远程控制平面、关键业务上游固定 IP。