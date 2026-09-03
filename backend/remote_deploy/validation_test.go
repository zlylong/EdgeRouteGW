package remote_deploy

import "testing"

type namedCase struct {
	label string
	value string
}

func TestValidateRealityServerNameAcceptsRealHostnames(t *testing.T) {
	valid := []string{
		"www.microsoft.com",
		"dl.google.com",
		"a.co",
		"xn--fiqs8s.example.com",
		"my-cdn-01.edge.example.org",
	}
	for _, name := range valid {
		if err := ValidateRealityServerName(name); err != nil {
			t.Errorf("ValidateRealityServerName(%q) = %v, want nil", name, err)
		}
	}
}

func TestValidateRealityServerNameRejectsUnusableValues(t *testing.T) {
	// Each of these silently breaks REALITY matching: nothing ever equals the
	// configured serverName, so every connection falls through to dest while
	// the deploy still reports success.
	invalid := []namedCase{
		{"empty", ""},
		{"whitespace only", " "},
		{"leading space", " www.microsoft.com"},
		{"trailing space", "www.microsoft.com "},
		{"inner space", "www.micro soft.com"},
		{"bare label", "localhost"},
		{"IPv4 literal", "93.184.216.34"},
		{"IPv6 literal", "2606:2800:220:1:248:1893:25c8:1946"},
		{"rooted", "www.microsoft.com."},
		{"empty label", "www..com"},
		{"leading hyphen", "-www.microsoft.com"},
		{"trailing hyphen", "www-.microsoft.com"},
		{"underscore", "www_cdn.microsoft.com"},
		{"scheme prefixed", "https://www.microsoft.com"},
		{"host:port pair", "www.microsoft.com:443"},
		{"path appended", "www.microsoft.com/index.html"},
		{"comma separated", "www.microsoft.com,www.apple.com"},
		{"newline injected", "www.microsoft.com\nevil.example.com"},
	}
	for _, tc := range invalid {
		if err := ValidateRealityServerName(tc.value); err == nil {
			t.Errorf("ValidateRealityServerName(%q) [%s] = nil, want error", tc.value, tc.label)
		}
	}
}

func TestValidateRealityServerNameRejectsOverlongNames(t *testing.T) {
	long := ""
	for i := 0; i < 26; i++ {
		long += "abcdefghij."
	}
	long += "com" // 289 characters
	if err := ValidateRealityServerName(long); err == nil {
		t.Error("ValidateRealityServerName(<289 chars>) = nil, want error")
	}

	longLabel := ""
	for i := 0; i < 64; i++ {
		longLabel += "a"
	}
	if err := ValidateRealityServerName(longLabel + ".com"); err == nil {
		t.Error("ValidateRealityServerName(<64-char label>) = nil, want error")
	}
}

func TestValidateRealityDestAcceptsDialableTargets(t *testing.T) {
	valid := []string{
		"www.microsoft.com:443",
		"dl.google.com:443",
		"93.184.216.34:443",
		"[2606:2800:220:1:248:1893:25c8:1946]:443",
		"example.com:8443",
	}
	for _, dest := range valid {
		if err := ValidateRealityDest(dest); err != nil {
			t.Errorf("ValidateRealityDest(%q) = %v, want nil", dest, err)
		}
	}
}

func TestValidateRealityDestRejectsMalformedTargets(t *testing.T) {
	invalid := []namedCase{
		{"empty", ""},
		{"no port", "www.microsoft.com"},
		{"empty host", ":443"},
		{"empty port", "www.microsoft.com:"},
		{"non-numeric port", "www.microsoft.com:https"},
		{"port zero", "www.microsoft.com:0"},
		{"port out of range", "www.microsoft.com:65536"},
		{"negative port", "www.microsoft.com:-1"},
		{"scheme prefixed", "https://www.microsoft.com:443"},
		{"unbracketed IPv6", "2606:2800:220:1:248:1893:25c8:1946:443"},
		{"trailing space", "www.microsoft.com:443 "},
		{"underscore host", "www_cdn.microsoft.com:443"},
		{"newline injected", "www.microsoft.com:443\nevil.example.com:443"},
	}
	for _, tc := range invalid {
		if err := ValidateRealityDest(tc.value); err == nil {
			t.Errorf("ValidateRealityDest(%q) [%s] = nil, want error", tc.value, tc.label)
		}
	}
}

func TestValidatePortRange(t *testing.T) {
	for _, p := range []int{1, 443, 65535} {
		if err := ValidatePort(p); err != nil {
			t.Errorf("ValidatePort(%d) = %v, want nil", p, err)
		}
	}
	for _, p := range []int{0, -1, 65536, 1 << 20} {
		if err := ValidatePort(p); err == nil {
			t.Errorf("ValidatePort(%d) = nil, want error", p)
		}
	}
}

func TestValidateRealityParamsRejectsEachBadField(t *testing.T) {
	if err := ValidateRealityParams(DefaultRealityPort, DefaultRealityServerName, DefaultRealityDest); err != nil {
		t.Fatalf("ValidateRealityParams(defaults) = %v, want nil", err)
	}
	if err := ValidateRealityParams(0, DefaultRealityServerName, DefaultRealityDest); err == nil {
		t.Error("ValidateRealityParams with port 0 = nil, want error")
	}
	if err := ValidateRealityParams(DefaultRealityPort, "", DefaultRealityDest); err == nil {
		t.Error("ValidateRealityParams with empty serverName = nil, want error")
	}
	if err := ValidateRealityParams(DefaultRealityPort, DefaultRealityServerName, "www.microsoft.com"); err == nil {
		t.Error("ValidateRealityParams with portless dest = nil, want error")
	}
}

func TestDefaultRealityPortIs443(t *testing.T) {
	// A REALITY inbound on a random high port contradicts its own cover story:
	// no real deployment reverse-proxies a major site outside the TLS ports.
	if DefaultRealityPort != 443 {
		t.Errorf("DefaultRealityPort = %d, want 443", DefaultRealityPort)
	}
}
