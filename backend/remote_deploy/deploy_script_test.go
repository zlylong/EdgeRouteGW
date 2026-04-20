package remote_deploy

import (
	"strings"
	"testing"
)

func TestGenerateVlessRealityInstallScript_UsesRetryablePackageAndDownloadSteps(t *testing.T) {
	script := GenerateVlessRealityInstallScript(443, "uuid", "priv", "short", "www.microsoft.com", "www.microsoft.com:443")

	checks := []string{
		"retry_cmd()",
		"retry_cmd apt-get update",
		"retry_cmd apt-get install -y curl unzip coreutils",
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
		"mkdir -p /etc/wireguard",
	}
	for _, want := range checks {
		if !strings.Contains(script, want) {
			t.Fatalf("script missing %q\n%s", want, script)
		}
	}
}
