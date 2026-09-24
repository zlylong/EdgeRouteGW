package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The API reference promises that 5xx responses never carry internal error
// text (it goes to the journal). This walks the handlers so a future
// `gin.H{"error": err.Error()}` on a 500 fails here instead of leaking again.
func TestNo5xxEchoesInternalError(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	var offenders []string
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for i, line := range strings.Split(string(b), "\n") {
			if !strings.Contains(line, "StatusInternalServerError") {
				continue
			}
			if strings.Contains(line, "err.Error()") || strings.Contains(line, "%v\", err") || strings.Contains(line, "%v\n") {
				offenders = append(offenders, f+":"+itoa(i+1)+": "+strings.TrimSpace(line))
			}
		}
	}
	if len(offenders) > 0 {
		t.Fatalf("5xx responses echo internal error text:\n%s", strings.Join(offenders, "\n"))
	}
}

// systemd/xray.service in the repository and the unit install.sh writes must
// agree on the lines that change behaviour; the repo copy had drifted.
func TestRepoXrayUnitMatchesInstallerTemplate(t *testing.T) {
	unit, err := os.ReadFile("../systemd/xray.service")
	if err != nil {
		t.Fatal(err)
	}
	inst, err := os.ReadFile("../scripts/install.sh")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"RestartPreventExitStatus=23",
		"LimitNPROC=10000",
		"LimitNOFILE=1048576",
		"Environment=XRAY_LOCATION_ASSET=/root/proxygw/core/xray",
		"ExecStart=/root/proxygw/core/xray/xray run -confdir /root/proxygw/core/xray",
	} {
		if !strings.Contains(string(unit), want) {
			t.Errorf("systemd/xray.service lacks %q", want)
		}
		if !strings.Contains(string(inst), want) {
			t.Errorf("install.sh xray unit template lacks %q", want)
		}
	}
}
