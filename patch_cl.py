import re

with open('/root/proxygw/docs/CHANGELOG.md', 'r') as f:
    content = f.read()

new_changelog = """## [Unreleased]

## [1.7.15] - 2026-05-04
### 🐛 修复 (Bug Fixes)
- **防止 GFW DNS 投毒**: 将后端 OSPF 展开引擎的所有 geosite/domain DNS 解析请求统一路由至本地 Mosdns 实例 (127.0.0.1:53)，利用其内置的 SOCKS5 代理安全解析海外被墙域名，彻底斩断 OSPF 规则被投毒导致本地出现如 119.29.x.x 脏路由的问题。
- **防止缓存污染遗留**: 在系统的自动更新脚本 (`scripts/update.sh`) 与缓存清洗脚本中增加对 `domain_resolve_cache`, `routes_table`, `geosite_expand_cache` 的底层清洗动作。

## [1.7.14] - 2026-05-04"""

content = content.replace("## [Unreleased]\n\n## [1.7.14] - 2026-05-04", new_changelog)

with open('/root/proxygw/docs/CHANGELOG.md', 'w') as f:
    f.write(content)
