# API Coverage Matrix (route-level)

- Total routes: 56
- Covered by tests (regex match on `/api/...`): 56
- Missing: 0

## Missing routes


## Covered routes

- `POST /apply` ← backend/feature_suite_test.go
- `GET /config/frr` ← backend/feature_suite_test.go
- `GET /config/mosdns` ← backend/feature_suite_test.go
- `GET /config/nftables` ← backend/feature_suite_test.go
- `GET /config/xray` ← backend/feature_suite_test.go
- `GET /cron` ← backend/api_integration_test.go, backend/feature_suite_test.go, e2e/tests/app-buttons.spec.js
- `POST /cron` ← backend/api_integration_test.go, backend/feature_suite_test.go, e2e/tests/app-buttons.spec.js
- `GET /dns` ← backend/api_integration_test.go, backend/feature_suite_test.go, e2e/tests/app-buttons.spec.js
- `POST /dns` ← backend/api_integration_test.go, backend/feature_suite_test.go, e2e/tests/app-buttons.spec.js
- `GET /geo/query` ← backend/feature_suite_test.go, backend/geo_query_test.go
- `GET /lan_acls` ← backend/feature_suite_test.go
- `POST /lan_acls` ← backend/feature_suite_test.go
- `DELETE /lan_acls/:id` ← backend/feature_suite_test.go
- `POST /lan_acls/default_policy` ← backend/feature_suite_test.go
- `POST /login` ← backend/api_integration_test.go, backend/feature_suite_test.go
- `POST /logout` ← backend/feature_suite_test.go
- `POST /mode` ← backend/mode_switch_test.go
- `GET /mosdns/versions` ← backend/feature_suite_test.go
- `POST /network_config` ← backend/feature_suite_test.go
- `GET /nodes` ← backend/api_integration_test.go, backend/feature_suite_test.go, e2e/tests/app-buttons.spec.js
- `POST /nodes` ← backend/api_integration_test.go, backend/feature_suite_test.go, e2e/tests/app-buttons.spec.js
- `DELETE /nodes/:id` ← backend/api_integration_test.go, backend/feature_suite_test.go, e2e/tests/app-buttons.spec.js
- `PUT /nodes/:id` ← backend/api_integration_test.go, backend/feature_suite_test.go, e2e/tests/app-buttons.spec.js
- `PUT /nodes/:id/default` ← backend/feature_suite_test.go, e2e/tests/app-buttons.spec.js
- `PUT /nodes/:id/toggle` ← backend/feature_suite_test.go, e2e/tests/app-buttons.spec.js
- `GET /nodes/failover_mode` ← backend/feature_suite_test.go, e2e/tests/app-buttons.spec.js
- `PUT /nodes/failover_mode` ← backend/feature_suite_test.go, e2e/tests/app-buttons.spec.js
- `POST /nodes/import` ← backend/feature_suite_test.go, e2e/tests/app-buttons.spec.js
- `POST /nodes/ping` ← backend/feature_suite_test.go
- `GET /ospf` ← backend/feature_suite_test.go
- `POST /ospf/reset_pending` ← backend/feature_suite_test.go
- `POST /ospf/settings` ← backend/feature_suite_test.go
- `POST /password` ← backend/api_integration_test.go
- `GET /protected_ips` ← backend/feature_suite_test.go
- `POST /protected_ips` ← backend/feature_suite_test.go
- `DELETE /protected_ips/:id` ← backend/feature_suite_test.go
- `GET /remote_nodes` ← backend/feature_suite_test.go, backend/remote_node_mock_test.go, e2e/tests/app-buttons.spec.js
- `POST /remote_nodes` ← backend/feature_suite_test.go, backend/remote_node_mock_test.go, e2e/tests/app-buttons.spec.js
- `DELETE /remote_nodes/:id` ← backend/feature_suite_test.go, backend/remote_node_mock_test.go, e2e/tests/app-buttons.spec.js
- `GET /remote_nodes/:id` ← backend/feature_suite_test.go, backend/remote_node_mock_test.go, e2e/tests/app-buttons.spec.js
- `POST /remote_nodes/:id/check` ← backend/feature_suite_test.go, backend/remote_node_mock_test.go, e2e/tests/app-buttons.spec.js
- `GET /remote_nodes/:id/history` ← backend/feature_suite_test.go, e2e/tests/app-buttons.spec.js
- `POST /remote_nodes/:id/regenerate` ← backend/feature_suite_test.go, backend/remote_node_mock_test.go, e2e/tests/app-buttons.spec.js
- `POST /remote_nodes/:id/rollback` ← backend/feature_suite_test.go, backend/remote_node_mock_test.go, e2e/tests/app-buttons.spec.js
- `POST /remote_nodes/batch` ← backend/feature_suite_test.go
- `GET /rules` ← backend/api_integration_test.go, backend/feature_suite_test.go, e2e/tests/app-buttons.spec.js
- `POST /rules` ← backend/api_integration_test.go, backend/feature_suite_test.go, e2e/tests/app-buttons.spec.js
- `DELETE /rules/:id` ← backend/feature_suite_test.go, e2e/tests/app-buttons.spec.js
- `GET /rules/categories` ← e2e/tests/app-buttons.spec.js
- `DELETE /rules/group/:group_id` ← backend/api_integration_test.go, backend/feature_suite_test.go
- `PUT /rules/group/:group_id` ← backend/api_integration_test.go, backend/feature_suite_test.go
- `PUT /rules/reorder` ← backend/feature_suite_test.go
- `GET /status` ← backend/feature_suite_test.go, e2e/tests/app-buttons.spec.js
- `GET /traffic` ← backend/feature_suite_test.go, e2e/tests/app-buttons.spec.js
- `POST /update/:component` ← backend/feature_suite_test.go
- `GET /xray/versions` ← backend/feature_suite_test.go