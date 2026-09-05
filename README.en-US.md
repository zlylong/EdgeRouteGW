

# EdgeRouteGW - A Modern Transparent Proxy Gateway

EdgeRouteGW is a high-performance, user-friendly transparent proxy gateway system. It provides a beautiful web management interface, allowing you to easily take over home or office network traffic and achieve intelligent traffic splitting.

![EdgeRouteGW Dashboard](docs/assets/dashboard.jpg)

## ✨ Core Features

- **Ready-to-use (Zero-Compile Speed Edition)**: Provides a complete web management backend. The installation process downloads pre-compiled packages, so you **don't need to set up any Go/Node compilation environment**. Deploy at lightning speed with full graphical management of nodes, rules, and devices.
- **Intelligent Traffic Splitting**: Built with a powerful domain and IP rule library. Domestic websites connect directly, while specific traffic goes through proxies, completely eliminating lag and DNS pollution.
- **Seamless Takeover**: Supports full gateway takeover (Mode A), pure Fake-IP bypass (Mode B), or pure OSPF dynamic routing (Mode C). LAN devices can freely access the internet without any manual configuration.
- **Remote Node Deployment**: Features a unique one-click remote node deployment system. Supports registering multiple overseas Linux hosts, with the gateway controller automatically pushing, configuring, and monitoring WireGuard/VLESS tunnel protocols via SSH.
- **Ultimate Security**: The system randomly generates high-strength initial passwords, and the frontend includes built-in anti-brute-force delays. SSH credentials stored in SQLite are dynamically encrypted/decrypted in memory using AES-256-GCM (authenticated; tampering is rejected), and Web UI resources are completely localized.
- **Kernel-Level Optimization**: Includes fully automatic Debian/Linux kernel parameter and firewall tuning. It drastically boosts the Conntrack connection tracking limit to the millions, globally enables BBR congestion control and fq_codel queuing, automatically blocks unshielded IPv6 traffic and kernel routing dead loops, and squeezes every drop of performance out of the device.

## 🚀 Quick Installation (Zero-Compile Speed Deployment)

Thanks to the pre-compiled architecture, gateway installation is extremely lightweight. It **requires no Go or NodeJS environment** and supports `x86_64` and `ARM64` architectures.
It is recommended to run the one-click deployment as the `root` user on a clean Debian 13 or Ubuntu 24.04 server:

```bash
bash <(curl -s -4 -L https://raw.githubusercontent.com/zlylong/EdgeRouteGW/main/scripts/install.sh)
```
*(Note: Due to the underlying mandatory Nftables anti-loopback policies, it is not recommended to run on a host with complex firewall rules. It is recommended to allocate a separate LXC container or lightweight VM.)*

> Note: The installation/upgrade script will automatically execute a low-risk database optimization (`scripts/db_optimize.sh --index-only`) after the service starts, which supplements critical indexes and statistics. A full `VACUUM` is still recommended to be executed manually during maintenance windows.

## 🔑 Initial Login

For system security, EdgeRouteGW **does not have a default fixed password**. After the first installation, please check the randomly generated initial password created by the system on the server terminal:

```bash
cat /root/proxygw/config/bootstrap_password.txt
```

Enter the gateway server's IP address in your browser (e.g., `http://192.168.x.x/`) and use this initial password to log in.
**⚠ Strong Recommendation: Please go to system settings immediately after your first login to change your password.** After modification, this txt file will automatically become invalid.

## 🕹️ Routing Modes & Usage Guide

EdgeRouteGW is designed with three physically isolated network takeover modes to adapt to different levels of home/office network topologies.

### 🟢 Mode A: Global Gateway Hijacking (Recommended for Beginners)
In this mode, EdgeRouteGW acts as a "side router" in the LAN, forcefully taking over all device traffic.
**Use Case**: The main router is a standard home router (e.g., Xiaomi, TP-Link, ASUS, etc.). No advanced networking knowledge is required.

**Example Usage:**
1. **Whole House Takeover**: Log into your home main router's admin panel, find **DHCP Server Settings**. Change the **Default Gateway** and **DNS Server** to the LAN IP address of the EdgeRouteGW server (e.g., `192.168.1.100`). Save and restart the router. Now, all devices connecting to the WiFi will automatically route through the proxy.
2. **Single Device Exclusive (On-Demand Proxy)**: If you don't want to affect your family and only want your own phone or computer to use the proxy. Simply change the IP acquisition method from "Automatic (DHCP)" to "Manual/Static" in your device's WiFi settings, and set the **Gateway** and **DNS** to EdgeRouteGW's IP.

### 🔵 Mode B: Hybrid Fake-IP Mode (Zero Latency / Anti-Loop Configuration Free)
This is an extremely pure and high-performance mode. For domain traffic, Mosdns will enable Fake-IP, and OSPF will announce a virtual IP pool (`198.18.0.0/16`) to the main router. For specific IP rules (e.g., custom IP ranges), the system will also push them via OSPF. This balances the pollution-free nature of Fake-IP with the need to intercept specific IPs.
**Use Case**: The main router supports OSPF, such as ROS or OpenWrt. Recommended for advanced users who don't want to hassle with "anti-loop" configurations.

**Example Usage:**
1. Your phones, TVs, and computers will still have their gateways pointing to the main router. **⚠ However, the DNS server pushed by DHCP must point to EdgeRouteGW's IP address.**
2. Configure OSPF on the main router, setting EdgeRouteGW as a neighbor.
3. **Loop Immunity**: Since your overseas node's real IP can never be `198.18.x.x`, the main router will never route packets destined for the nodes back to EdgeRouteGW, natively immune to OSPF loops.

### 🟣 Mode C: Pure OSPF Dynamic Bypass Mode (Traditional Real-IP Splitting)
Disables Fake-IP functionality. Mosdns returns the real overseas target IP, and EdgeRouteGW will dynamically push static IPs and real GeoIPs (such as Netflix, Telegram subnets) to the main router.
**Use Case**: Advanced users sensitive to the Fake-IP mechanism (e.g., P2P games that strictly validate target IPs or app errors).

**💡 Mode C Exclusive Feature: Dynamic Domain Tracking & Self-Healing**:
In pure OSPF mode, physical router splitting can only rely on **real IPs**. However, thanks to EdgeRouteGW's underlying background daemon, when you add a standard `domain` rule in the management panel, the system periodically performs real DNS resolution in the background and dynamically pushes the resolved latest IPs to the main router's OSPF routing table.
**Advantage**: Perfectly solves the problem that traditional static routing cannot handle frequent changes in modern cloud service CDNs and DNS round-robin IPs, achieving seamless tracking and self-healing for domain routing.

**Note on Anti-Looping**: Since OSPF announces real overseas subnets, if your proxy node's IP happens to fall within that subnet, it will create a dead loop and cause network outage! You must configure **Policy-Based Routing (PBR)** on the main router: Force traffic originating from EdgeRouteGW's IP to route directly out the WAN, ignoring OSPF routes.
*(👉 ROS v7 Example: Create a new `bypass_proxy` routing table pointing to the public WAN interface, then execute `/routing rule add src-address=<EdgeRouteGW_IP>/32 action=lookup-only-in-table table=bypass_proxy`. See [Operations Documentation](./docs/OPERATIONS.md) for details.)*

### 🧩 Domain Rule Semantics (Consistent with Official Xray Semantics)

- `c.com`: Matches only the root domain (equivalent to `full:c.com`), **does not match** `www.c.com`.
- `**.c.com`: Matches the root domain + any level of subdomains (equivalent to `domain:c.com`).
- `*.c.com`: Matches the root domain + zero or one level of subdomains (equivalent to regex).

**Mode Restrictions (Current Implementation)**:
- Wildcard domain rules (`*.` / `**.`) are only allowed to be added in **Mode A**.
- Mode B / Mode C only allow standard domains (e.g., `c.com`), because OSPF static routing cannot safely express wildcard semantics.
- **Protected IP List**: In Mode A, you can add "Protected IPs". These IPs will bypass Xray and access directly, used to prevent network outages caused by rule errors.

## 📚 Documentation Guide
- [Main Router Companion Configuration Guide (ROS/OpenWrt)](./docs/NETWORK_SETUP.md) - OSPF/DNS/PBR setup tutorial
- [Routing Splitting Rules Introduction](./docs/ROUTING_RULES_GUIDE.md) - Analysis of GeoSite/GeoIP working principles
- [Operations & Troubleshooting](./docs/OPERATIONS.md) - Service management, upgrade, and system uninstallation
- [Developer & Architecture Guide](./docs/DEVELOPER.md) - Underlying architecture, source code structure, and API reference
- [API Interface Documentation](./docs/API.md) - RESTful API documentation

## 🧪 Test Guide

EdgeRouteGW provides a complete multi-layered test script system, with all test scripts located in the `scripts/` directory:

| Script | Purpose |
|---|---|
| `test_all.sh` | **Main Orchestrator** — Executes all 6 stages sequentially (Backend → Race Detection → Coverage → Frontend E2E → Build → Git Status) |
| `test_backend.sh` | Backend Go test runner (supports parameters like `--race`, `--verbose`, `--short`) |
| `test_coverage.sh` | Backend coverage report generator (outputs text summary + HTML visualization report to `coverage/`) |
| `test_benchmark.sh` | Benchmark runner (supports `--bench=Pattern` filtering, `--count=N` repetitions) |
| `test_frontend.sh` | Frontend Playwright E2E button tests |

```bash
# Run all tests
./scripts/test_all.sh

# Run only backend tests (with race detection)
./scripts/test_backend.sh --race

# Run benchmark tests
./scripts/test_benchmark.sh --bench=GeoQuery

# Generate coverage report
./scripts/test_coverage.sh
```

### Pre-commit Check

The project has a Git pre-commit hook installed, which automatically runs before each `git commit`:
1. Backend code compilation check
2. `-short` mode testing for affected packages

To bypass, use `git commit --no-verify`.

## 🙏 Acknowledgments

The core network proxy and DNS resolution splitting capabilities underlying this project rely on the following excellent open-source projects, for which we extend our thanks:

- [XTLS/Xray-core](https://github.com/XTLS/Xray-core) - Provides extremely powerful and high-performance transparent proxy and protocol offloading capabilities.
- [IrineSistiana/mosdns](https://github.com/IrineSistiana/mosdns) - Provides a flexible and efficient DNS forwarder, GeoIP splitter, and leak-prevention resolution engine.
- [Loyalsoldier/v2ray-rules-dat](https://github.com/loyalsoldier/v2ray-rules-dat) - Provides extremely comprehensive and frequently updated geodata packages (GeoSite/GeoIP), forming the foundation for precise routing and traffic splitting.

Thanks to all open-source contributors for their selfless dedication to building a freer and more open network environment!
