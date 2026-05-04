import re

with open('/root/proxygw/docs/CHANGELOG.md', 'r') as f:
    content = f.read()

new_changelog = """## [Unreleased]

## [1.7.16] - 2026-05-04
### ✨ 核心重构 (Core Refactoring)
- **彻底抛弃底层 UDP 裸解析**: 后端 OSPF 解析引擎直接集成 SOCKS5 DNS-over-TCP 客户端拨号逻辑。现在对 `geosite/domain` 的展开解析，将强制使用您面板上设置的 `dns_remote`，并将其自动包裹进 TCP 流经由本地 10808 端口（Xray）加密送出，彻底绕过 Mosdns 的 fallback 分流误判，达到 100% 免疫 GFW 投毒。

## [1.7.15] - 2026-05-04"""

content = content.replace("## [Unreleased]\n\n## [1.7.15] - 2026-05-04", new_changelog)

with open('/root/proxygw/docs/CHANGELOG.md', 'w') as f:
    f.write(content)
