package main

import (
	"os"
	"strings"
	"testing"
)

// The backend binary runs as root. install.sh and update.sh fetch it from
// GitHub and execute it; until now nothing checked that what arrived is what
// the release workflow built. These pin the three parts that have to agree:
// the workflow publishes SHA256SUMS, and both scripts verify against it and
// abort on a mismatch.
func TestReleaseWorkflowPublishesChecksums(t *testing.T) {
	b, err := os.ReadFile("../.github/workflows/release.yml")
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	for _, want := range []string{
		"sha256sum proxygw-backend-linux-amd64 proxygw-backend-linux-arm64 > SHA256SUMS",
		"backend/SHA256SUMS",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("release.yml missing %q; install.sh/update.sh would find nothing to verify against", want)
		}
	}
}

func TestBackendBinaryDownloadsAreChecksumVerified(t *testing.T) {
	for _, f := range []string{"../scripts/install.sh", "../scripts/update.sh"} {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("%s: %v", f, err)
		}
		s := string(b)
		if !strings.Contains(s, "verify_backend_checksum()") {
			t.Errorf("%s does not define verify_backend_checksum", f)
		}
		// Defined is not enough; it has to run on the downloaded file and a
		// failure has to stop the script before the binary is installed.
		if !strings.Contains(s, "verify_backend_checksum \"") || !strings.Contains(s, "\"$PROXYGW_LATEST\" || exit 1") {
			t.Errorf("%s does not call verify_backend_checksum fail-closed on the downloaded binary", f)
		}
		if !strings.Contains(s, "releases/download/${tag}/SHA256SUMS") {
			t.Errorf("%s does not fetch SHA256SUMS from the release", f)
		}
	}
}
