# EdgeRouteGW 2.0 — Transparent Proxy Gateway

[中文](README.md)

EdgeRouteGW is a Linux transparent proxy gateway for a LAN. Devices point their gateway or DNS at it (Mode A), or the main router steers only the traffic that needs proxying to it over OSPF (Mode B / C); it then routes each flow to direct, a node, an outbound group or a block according to your rules. 2.0 is a ground-up rewrite: a single binary with SQLite holding the desired state, and a reconciler that renders and applies the Xray / Mosdns / nftables / FRR / kernel WireGuard configuration, rolling back on failure.

## Features

- **Three takeover modes**: A (global TPROXY gateway), B (Fake-IP + OSPF bypass, only proxied domains come through), C (real-IP + OSPF bypass, domain rules are resolved periodically and announced to the main router).
- **Routing rules**: ordered rule sets matching `domain / geosite / geoip / ip` with actions direct / default proxy / block / node / group; a built-in default set (private and CN direct) pinned first; the fallback policy and node-failure policy visible at the end; an in-page Trace shows where any target goes.
- **Nodes and groups**: VLESS / VMess / Trojan / Shadowsocks / SOCKS / HTTP / WireGuard, share-link import, failover and load-balancing groups (leastPing / leastLoad / random / roundRobin) probed every 30 s; WireGuard nodes run in the kernel (twice the throughput of user space) with automatic fallback for WARP-style peers.
- **DNS**: Mosdns split resolution, CN domains via local upstreams, everything else via remote upstreams through the proxy, DoH/DoT by default; all LAN port-53 queries hijacked and DoT refused to stop leaks; EDNS Client Subnet, internal-domain forwarding, AAAA filtering; answers are recorded so the observe page can label IP targets with domains.
- **Managed hosts**: give it SSH to a VPS and it deploys a WireGuard or VLESS-REALITY server there, creates the matching node and tunes the host; host keys are pinned on first use.
- **Component updates**: Xray / Mosdns / GeoData downloaded with checksum verification, smoke-tested, one-click rollback, daily/weekly/monthly schedules; bundled offline package, local upload, mirror prefix or proxy when GitHub is unreachable; the gateway updates itself the same way.
- **Observability**: host metrics (CPU / memory / conntrack / live rates), component status and traffic on the dashboard; live connections with geo tags and resolved domains, node latency and events on the observe page; every generated config viewable.
- **Security**: argon2id passwords, sessions + CSRF, read-only / read-write API tokens, AES-GCM encrypted credentials, systemd sandboxing, verified downloads; a security audit preceded 2.0 (see [docs/SECURITY-AUDIT.md](docs/SECURITY-AUDIT.md)).
- **Runs on** Debian 12/13 and Ubuntu 22.04/24.04, amd64 / arm64, installed from a .deb; no Go or Node needed.

## Install

With GitHub reachable, as root:

```bash
bash <(curl -fsSL https://raw.githubusercontent.com/zlylong/EdgeRouteGW/main/scripts/install.sh)
```

Without GitHub access: download `edgeroutegw-full_<version>_<arch>.tar.gz` from [Releases](https://github.com/zlylong/EdgeRouteGW/releases) on any machine that can (it bundles verified Xray, Mosdns and GeoData), copy it to the gateway and run:

```bash
tar xzf edgeroutegw-full_*.tar.gz && cd edgeroutegw-full_* && bash install-offline.sh
```

Give the gateway its own VM or LXC (it owns nftables and policy routing); 2 cores and 2 GB are plenty. Upgrading from 1.x: [docs/UPGRADE-FROM-1.x.md](docs/UPGRADE-FROM-1.x.md).

## First login

There is no fixed default password. Read the generated one on the gateway:

```bash
cat /var/lib/edgeroutegw/bootstrap_password.txt
```

Open `http://<gateway-ip>/`, log in, and change the password: nothing else is available until you do, and the file is deleted afterwards.

## Modes

| Mode | Devices | Main router | Use when |
|---|---|---|---|
| **A · global gateway** | gateway and DNS point at the box | nothing | home or small office, simplest |
| **B · Fake-IP bypass** | unchanged | learns `198.18.0.0/16` over OSPF, only proxied domains come through | you cannot change device gateways |
| **C · real-IP bypass** | unchanged | learns the real IPs of the rules over OSPF (kernel table 200) | classic real-IP split routing |

Router configuration for B / C: [docs/NETWORK_SETUP.md](docs/NETWORK_SETUP.md).

## Documentation

- [Routing rules guide](docs/ROUTING_RULES_GUIDE.md) · [Network setup](docs/NETWORK_SETUP.md) · [Operations](docs/OPERATIONS.md) · [API](docs/API.md) · [Developer guide](docs/DEVELOPER.md) · [Design](docs/DESIGN.md) · [Upgrade from 1.x](docs/UPGRADE-FROM-1.x.md) · [Security audit](docs/SECURITY-AUDIT.md) · [Changelog](docs/CHANGELOG.md)

The documents are written in Chinese.

## Acknowledgements

[Xray-core](https://github.com/XTLS/Xray-core), [Mosdns](https://github.com/IrineSistiana/mosdns), [Loyalsoldier/v2ray-rules-dat](https://github.com/Loyalsoldier/v2ray-rules-dat), [FRRouting](https://frrouting.org/), [nftables](https://netfilter.org/projects/nftables/), [WireGuard](https://www.wireguard.com/).

## License

See [LICENSE](LICENSE).
