import re

with open('/root/proxygw/docs/CHANGELOG.md', 'r') as f:
    content = f.read()

new_changelog = """## [Unreleased]

## [1.7.19] - 2026-05-05
### 🐛 深度修复 (Deep Fixes)
- **真正的架构精简**: 彻底移除后端 OSPF 引擎中残留的 SOCKS5 解析逻辑和 `host` 裸连后门代码。
- **强制委派 127.0.0.1**: 修正了前一版本中代码未正确合入的失误，现在后端解析确保 100% 仅向本地 Mosdns (`127.0.0.1`) 提议，利用 Mosdns 成熟的分流与隧道机制处理所有解析。
- **消除解析污染**: 杜绝了因 SOCKS5-in-SOCKS5 逻辑异常导致的解析回退，确保被墙域名解析结果与客户端完全一致。

## [1.7.18] - 2026-05-05"""

content = content.replace("## [Unreleased]\n\n## [1.7.18] - 2026-05-05", new_changelog)

with open('/root/proxygw/docs/CHANGELOG.md', 'w') as f:
    f.write(content)
