package update

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeBundle(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	must := func(p, content string, perm os.FileMode) {
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), perm); err != nil {
			t.Fatal(err)
		}
	}
	must(filepath.Join(dir, "VERSIONS"), "xray=v26.3.27\nmosdns=v5.3.4\ngeodata=202610192206\narch=amd64\n", 0o644)
	must(filepath.Join(dir, "xray", "xray"), "#!/bin/sh\necho Xray 26.3.27 (bundled)\n", 0o755)
	must(filepath.Join(dir, "mosdns", "mosdns"), "#!/bin/sh\necho v5.3.4\n", 0o755)
	must(filepath.Join(dir, "geodata", "geoip.dat"), "ip", 0o644)
	must(filepath.Join(dir, "geodata", "geosite.dat"), "site", 0o644)
	must(filepath.Join(dir, "geodata", "geodata.ver"), "202610192206\n", 0o644)
	return dir
}

func TestSeedFromBundle(t *testing.T) {
	s, _, _ := newSvc(t)
	ctx := context.Background()
	s.BundleDir = writeBundle(t)

	sts := s.Statuses(ctx, false)
	for _, st := range sts {
		if st.Name == Self {
			continue
		}
		if st.Installed != "" || st.Bundled == nil {
			t.Fatalf("before seeding %+v", st)
		}
	}
	seeded := s.SeedFromBundle(ctx)
	if len(seeded) != 3 {
		t.Fatalf("seeded %v", seeded)
	}
	if v := s.Installed(ctx, Xray); v != "v26.3.27" {
		t.Fatalf("xray %q", v)
	}
	if v := s.Installed(ctx, Mosdns); v != "v5.3.4" {
		t.Fatalf("mosdns %q", v)
	}
	if v := s.Installed(ctx, Geodata); v != "202610192206" {
		t.Fatalf("geodata %q", v)
	}
	// Bundle files are copied, never moved.
	if _, err := os.Stat(filepath.Join(s.BundleDir, "xray", "xray")); err != nil {
		t.Fatal("bundle file vanished")
	}
	// Idempotent: everything is installed now.
	if again := s.SeedFromBundle(ctx); len(again) != 0 {
		t.Fatalf("second seed %v", again)
	}
	// Explicit re-install from the bundle is still possible through Start.
	if _, err := s.Start(ctx, Mosdns, "", "bundled"); err != nil {
		t.Fatal(err)
	}
	if j := waitJob(t, s, Mosdns); j.State != "succeeded" || j.Version != "v5.3.4" {
		t.Fatalf("job %+v", j)
	}
}

func TestSeedFromBundleWithoutBundle(t *testing.T) {
	s, _, _ := newSvc(t)
	s.BundleDir = t.TempDir()
	if seeded := s.SeedFromBundle(context.Background()); len(seeded) != 0 {
		t.Fatalf("seeded %v", seeded)
	}
	if _, err := s.Start(context.Background(), Xray, "", "bundled"); err == nil {
		t.Fatal("bundled install should fail without a bundle")
	}
}

func TestDownloadSettings(t *testing.T) {
	s, _, _ := newSvc(t)
	ctx := context.Background()
	if err := s.SetDownloadSettings(ctx, DownloadSettings{Proxy: "ftp://x"}); err == nil {
		t.Fatal("ftp proxy accepted")
	}
	if err := s.SetDownloadSettings(ctx, DownloadSettings{Mirror: "socks5://x"}); err == nil {
		t.Fatal("socks mirror accepted")
	}
	if err := s.SetDownloadSettings(ctx, DownloadSettings{Proxy: "socks5://127.0.0.1:10808", Mirror: "https://mirror.example"}); err != nil {
		t.Fatal(err)
	}
	d := s.GetDownloadSettings(ctx)
	if d.Proxy != "socks5://127.0.0.1:10808" || d.Mirror != "https://mirror.example/" {
		t.Fatalf("%+v", d)
	}
	if got := s.GitHub.AssetURL("XTLS/Xray-core", "v1", "a.zip"); !strings.HasPrefix(got, "https://mirror.example/") {
		t.Fatalf("asset url %q", got)
	}
	if tr, ok := s.HTTP.Transport.(*http.Transport); !ok || tr.Proxy == nil {
		t.Fatal("proxy not applied to transport")
	}
	// Clearing goes back to direct.
	if err := s.SetDownloadSettings(ctx, DownloadSettings{}); err != nil {
		t.Fatal(err)
	}
	if s.GitHub.Mirror != "" {
		t.Fatal("mirror not cleared")
	}
}

func TestInstallUpload(t *testing.T) {
	s, _, _ := newSvc(t)
	ctx := context.Background()
	zb := zipOf(map[string]string{"mosdns": "#!/bin/sh\necho v5.3.5-local\n"})
	path := filepath.Join(t.TempDir(), "mosdns.zip")
	if err := os.WriteFile(path, zb, 0o644); err != nil {
		t.Fatal(err)
	}
	f, _ := os.Open(path)
	defer f.Close()
	if _, err := s.InstallUpload(ctx, Mosdns, f, int64(len(zb)), strings.Repeat("0", 64)); err == nil || !strings.Contains(err.Error(), "checksum mismatch") {
		t.Fatalf("mismatch not detected: %v", err)
	}
	f.Seek(0, 0)
	job, err := s.InstallUpload(ctx, Mosdns, f, int64(len(zb)), sum(zb))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(job.Version, "upload:") {
		t.Fatalf("version %q", job.Version)
	}
	if j := waitJob(t, s, Mosdns); j.State != "succeeded" {
		t.Fatalf("job %+v", j)
	}
	if v := s.Installed(ctx, Mosdns); v != "v5.3.5-local" {
		t.Fatalf("installed %q", v)
	}
	// Unknown component and self are refused.
	f.Seek(0, 0)
	if _, err := s.InstallUpload(ctx, Self, f, int64(len(zb)), ""); err == nil {
		t.Fatal("self upload accepted")
	}
}

func TestParseMosdnsVersion(t *testing.T) {
	for in, want := range map[string]string{
		"v5.3.4-0-gb732318\n": "v5.3.4",
		"v5.3.4-3-gabcdef0":   "v5.3.4-3-gabcdef0",
		"v5.3.4\n":            "v5.3.4",
	} {
		if got := parseMosdnsVersion(in); got != want {
			t.Errorf("%q -> %q, want %q", in, got, want)
		}
	}
}

func TestInstallSelfRunsDpkgInTransientUnit(t *testing.T) {
	s, _, run := newSvc(t)
	s.SkipRootCheck = true
	ctx := context.Background()
	asset, err := SelfDebAsset("v2.0.0-rc.9")
	if err != nil {
		t.Fatal(err)
	}
	deb := []byte("not really a deb")
	// Serve the release assets from a dedicated server (fakeUpstream keys blobs by basename).
	mux := http.NewServeMux()
	mux.HandleFunc("/zlylong/EdgeRouteGW/releases/download/v2.0.0-rc.9/SHA256SUMS", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "%s  %s\n", sum(deb), asset)
	})
	mux.HandleFunc("/zlylong/EdgeRouteGW/releases/download/v2.0.0-rc.9/"+asset, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(deb)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	s.GitHub = &GitHub{APIBase: srv.URL, DownloadBase: srv.URL, Client: srv.Client()}
	s.HTTP = srv.Client()

	if _, err := s.Start(ctx, Self, "v2.0.0-rc.9", "update"); err != nil {
		t.Fatal(err)
	}
	j := waitJob(t, s, Self)
	if j.State != "succeeded" {
		t.Fatalf("job %+v", j)
	}
	var sawDpkg, sawRestart bool
	for _, c := range run.Calls {
		if c.Name != "systemd-run" {
			if c.Name == "dpkg" {
				t.Fatalf("dpkg must not run inside the service: %+v", c)
			}
			continue
		}
		joined := strings.Join(c.Args, " ")
		if strings.Contains(joined, "--wait") && strings.Contains(joined, "--setenv=EDGEROUTEGW_SELFUPDATE=1") && strings.Contains(joined, "dpkg -i ") {
			sawDpkg = true
		}
		if strings.Contains(joined, "--on-active=3") && strings.Contains(joined, "systemctl restart edgeroutegw.service") && strings.Contains(joined, "rm -rf") {
			sawRestart = true
		}
	}
	if !sawDpkg || !sawRestart {
		t.Fatalf("dpkg=%v restart=%v calls=%+v", sawDpkg, sawRestart, run.Calls)
	}
}
