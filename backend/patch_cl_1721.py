import re

with open('/root/proxygw/docs/CHANGELOG.md', 'r') as f:
    content = f.read()

new_changelog = """## [Unreleased]

## [1.7.21] - 2026-05-05
### 🛡️ 强制解析隔离 (Hardened Resolution)
- **物理截断解析回退**: 彻底移除了 `resolveDomainIPv4WithTTL` 中残留的 `host` 系统调用和 GeoIP 回退逻辑。
- **强制使用 dig 通道**: 所有的解析入口（包括兜底解析）现在都强制定向到 `127.0.0.1` 且仅通过 `dig` 命令行执行，彻底杜绝了 Go 原生解析器在解析失败时自动读取 `/etc/resolv.conf` 造成的污染残留。
- **零泄漏承诺**: 后端引擎现在不再具备任何绕过 Mosdns 直连公网 DNS 的代码路径。

## [1.7.20] - 2026-05-05"""

content = content.replace("## [Unreleased]\n\n## [1.7.20] - 2026-05-05", new_changelog)

with open('/root/proxygw/docs/CHANGELOG.md', 'w') as f:
    f.write(content)
