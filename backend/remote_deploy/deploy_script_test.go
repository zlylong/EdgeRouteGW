package remote_deploy

import (
	"encoding/base64"
	"strings"
	"testing"
)

func TestGenerateVlessRealityInstallScript_UsesRetryablePackageAndDownloadSteps(t *testing.T) {
	script := GenerateVlessRealityInstallScript(443, "uuid", "priv", "pub", "short", "www.microsoft.com", "www.microsoft.com:443")

	checks := []string{
		"retry_cmd()",
		"retry_cmd apt-get update",
		"retry_cmd apt-get install -y curl unzip coreutils",
		"sysctl net.ipv4.tcp_available_congestion_control | grep -qw bbr",
		"net.ipv4.tcp_congestion_control=bbr",
		"curl -4 --fail --location",
		"--retry 5",
	}
	for _, want := range checks {
		if !strings.Contains(script, want) {
			t.Fatalf("script missing %q\n%s", want, script)
		}
	}
}

func TestGenerateWGInstallScript_UsesRetryablePackageInstall(t *testing.T) {
	script := GenerateWGInstallScript(51820, "server-priv", "client-pub", "10.0.0.1/24")

	checks := []string{
		"retry_cmd()",
		"retry_cmd apt-get update",
		"retry_cmd apt-get install -y wireguard iptables iproute2 curl",
		"sysctl net.ipv4.tcp_available_congestion_control | grep -qw bbr",
		"net.ipv4.tcp_congestion_control=bbr",
		"mkdir -p /etc/wireguard",
	}
	for _, want := range checks {
		if !strings.Contains(script, want) {
			t.Fatalf("script missing %q\n%s", want, script)
		}
	}
}

// A node whose REALITY handshake does not work is worse than one that fails to
// install: the deploy reports success, so the outage is only visible by
// inspecting traffic. The generated script must therefore prove the tunnel
// works and exit non-zero when it does not, which is what turns the node's
// status into Failed instead of Online.
func TestGenerateVlessRealityInstallScript_ProbesTheTunnelAndFailsClosed(t *testing.T) {
	script := GenerateVlessRealityInstallScript(443, "the-uuid", "priv", "the-pubkey", "the-shortid", "example.com", "example.com:443")

	checks := []string{
		"REALITY smoke test",
		"--socks5-hostname 127.0.0.1:45789",
		`"https://example.com/"`,
		"trap cleanup_smoke EXIT",
		"exit 1",
	}
	for _, want := range checks {
		if !strings.Contains(script, want) {
			t.Fatalf("script missing %q\n%s", want, script)
		}
	}

	// The probe must carry the same credentials as the inbound it dials,
	// otherwise it would fail for reasons unrelated to the camouflage target
	// and every deploy would break.
	probe := decodeProbeConfig(t, script)
	if !strings.Contains(probe, `"the-pubkey"`) {
		t.Fatalf("probe config does not use the deployed REALITY public key: %s", probe)
	}
	if !strings.Contains(probe, `"the-uuid"`) {
		t.Fatalf("probe config does not use the deployed client id: %s", probe)
	}
	if !strings.Contains(probe, `"the-shortid"`) {
		t.Fatalf("probe config does not use the deployed shortId: %s", probe)
	}

	// The failure message has to name dest: that is the field an operator has
	// to change, and it is not otherwise deducible from a handshake error.
	if !strings.Contains(script, "example.com:443 does not work as a REALITY camouflage target") {
		t.Fatalf("script does not point at dest on failure\n%s", script)
	}
}

// decodeProbeConfig extracts the base64 payload of the probe client config,
// which is the second one the script writes.
func decodeProbeConfig(t *testing.T, script string) string {
	t.Helper()
	const marker = `echo "`
	idx := strings.Index(script, "probe.json")
	if idx < 0 {
		t.Fatalf("script never writes a probe config\n%s", script)
	}
	head := script[:idx]
	start := strings.LastIndex(head, marker)
	if start < 0 {
		t.Fatalf("no base64 payload before the probe config\n%s", script)
	}
	start += len(marker)
	end := strings.Index(script[start:], `"`)
	if end < 0 {
		t.Fatalf("unterminated base64 payload\n%s", script)
	}
	raw, err := base64.StdEncoding.DecodeString(script[start : start+end])
	if err != nil {
		t.Fatalf("probe config is not valid base64: %v", err)
	}
	return string(raw)
}
