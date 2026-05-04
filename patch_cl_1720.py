import re

with open('/root/proxygw/docs/CHANGELOG.md', 'r') as f:
    content = f.read()

new_changelog = """## [Unreleased]

## [1.7.20] - 2026-05-05
### 🛠️ 核心解析引擎替换 (Engine Swap)
- **引入 dig 作为解析核心**: 彻底废弃 Go 原生解析器，后端 OSPF 引擎改用系统受信任的 `dig` 工具直接向 `127.0.0.1` (Mosdns) 发起请求。
- **消除日志幻象 IP**: 解决了由于 Go 解析器读取 `/etc/resolv.conf` 占位符导致的日志中出现 `119.29.29.29` 的误导性报错。
- **解析行为一致性**: 确保后端程序看到的解析结果与用户在终端手动执行 `dig` 的结果 100% 对齐。

## [1.7.19] - 2026-05-05"""

content = content.replace("## [Unreleased]\n\n## [1.7.19] - 2026-05-05", new_changelog)

with open('/root/proxygw/docs/CHANGELOG.md', 'w') as f:
    f.write(content)
