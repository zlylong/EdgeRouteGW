package update

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/zlylong/EdgeRouteGW/internal/driver/execx"
	"github.com/zlylong/EdgeRouteGW/internal/driver/systemd"
	"github.com/zlylong/EdgeRouteGW/internal/events"
	"github.com/zlylong/EdgeRouteGW/internal/paths"
	"github.com/zlylong/EdgeRouteGW/internal/store"
)

func zipOf(files map[string]string) []byte {
	var buf bytes.Buffer
	w := zip.NewWriter(&buf)
	for name, content := range files {
		f, _ := w.Create(name)
		_, _ = f.Write([]byte(content))
	}
	_ = w.Close()
	return buf.Bytes()
}

func sum(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }

// fakeUpstream serves a GitHub-like API and downloads for all three repos.
func fakeUpstream(t *testing.T) (*httptest.Server, map[string][]byte) {
	t.Helper()
	xrayAsset, _ := XrayAsset()
	mosAsset, _ := MosdnsAsset()
	blobs := map[string][]byte{
		xrayAsset:   zipOf(map[string]string{"xray": "#!/bin/sh\necho Xray 25.1.0 (fake)\n", "geoip.dat": "ip", "geosite.dat": "site"}),
		mosAsset:    zipOf(map[string]string{"mosdns": "#!/bin/sh\necho v5.3.4\n"}),
		"rules.zip": zipOf(map[string]string{"geoip.dat": "newip", "geosite.dat": "newsite"}),
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/XTLS/Xray-core/releases/latest", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"tag_name": "v25.1.0"})
	})
	mux.HandleFunc("/repos/XTLS/Xray-core/releases", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode([]map[string]any{{"tag_name": "v25.1.0"}, {"tag_name": "v24.12.31", "prerelease": true}, {"tag_name": "../evil"}})
	})
	mux.HandleFunc("/repos/IrineSistiana/mosdns/releases/latest", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"tag_name": "v5.3.4"})
	})
	mux.HandleFunc("/repos/IrineSistiana/mosdns/releases/tags/v5.3.4", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"tag_name": "v5.3.4", "assets": []map[string]any{{"name": mosAsset, "digest": "sha256:" + sum(blobs[mosAsset])}}})
	})
	mux.HandleFunc("/repos/Loyalsoldier/v2ray-rules-dat/releases/latest", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"tag_name": "202610200000"})
	})
	mux.HandleFunc("/XTLS/Xray-core/releases/download/v25.1.0/"+xrayAsset+".dgst", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "MD5= x\nSHA2-256= %s\n", sum(blobs[xrayAsset]))
	})
	mux.HandleFunc("/Loyalsoldier/v2ray-rules-dat/releases/download/202610200000/rules.zip.sha256sum", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "%s  rules.zip\n", sum(blobs["rules.zip"]))
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		name := filepath.Base(r.URL.Path)
		if b, ok := blobs[name]; ok {
			_, _ = w.Write(b)
			return
		}
		http.NotFound(w, r)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, blobs
}

func newSvc(t *testing.T) (*Service, *systemd.Fake, *execx.Fake) {
	t.Helper()
	srv, _ := fakeUpstream(t)
	dir := t.TempDir()
	st, err := store.Open(context.Background(), filepath.Join(dir, "u.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	_, _ = st.Migrate(context.Background())
	p := paths.New(dir)
	sd := systemd.NewFake("edgeroutegw-xray.service")
	run := &execx.Fake{Handler: func(name string, args []string) (execx.Result, error) {
		// "version" smoke tests echo the script's content line.
		if len(args) == 1 && args[0] == "version" {
			b, err := os.ReadFile(name)
			if err != nil {
				return execx.Result{}, err
			}
			lines := strings.Split(string(b), "\n")
			if len(lines) > 1 {
				return execx.Result{Output: []byte(strings.TrimPrefix(lines[1], "echo ") + "\n")}, nil
			}
		}
		return execx.Result{}, nil
	}}
	s := New(p, st, run, sd, events.New(32), func() {}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	s.GitHub = &GitHub{APIBase: srv.URL, DownloadBase: srv.URL, Client: srv.Client()}
	s.HTTP = srv.Client()
	return s, sd, run
}

func waitJob(t *testing.T, s *Service, name string) Job {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		s.mu.Lock()
		j := s.jobs[name]
		var c Job
		if j != nil {
			c = *j
		}
		s.mu.Unlock()
		if j != nil && c.State != "running" {
			return c
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("job did not finish")
	return Job{}
}

func TestInstallXrayAndRollback(t *testing.T) {
	s, sd, _ := newSvc(t)
	ctx := context.Background()
	if v := s.Installed(ctx, Xray); v != "" {
		t.Fatalf("installed before: %q", v)
	}
	job, err := s.Start(ctx, Xray, "", "update")
	if err != nil || job.Version != "v25.1.0" {
		t.Fatalf("start %+v %v", job, err)
	}
	if _, err := s.Start(ctx, Mosdns, "", "update"); err != ErrBusy {
		t.Fatalf("expected busy, got %v", err)
	}
	j := waitJob(t, s, Xray)
	if j.State != "succeeded" {
		t.Fatalf("job %+v", j)
	}
	if v := s.Installed(ctx, Xray); v != "v25.1.0" {
		t.Fatalf("installed %q", v)
	}
	if _, err := os.Stat(s.Paths.GeoSite()); err != nil {
		t.Fatal("geodata from the xray archive should seed an empty geodata dir")
	}
	if sd.Restarts["edgeroutegw-xray.service"] != 1 {
		t.Fatalf("restarts %v", sd.Restarts)
	}
	// Second install keeps a .prev; rollback restores it.
	_ = os.WriteFile(s.Paths.XrayBinary(), []byte("#!/bin/sh\necho Xray 1.0.0 (old)\n"), 0o755)
	job, _ = s.Start(ctx, Xray, "v25.1.0", "update")
	if waitJob(t, s, Xray).State != "succeeded" {
		t.Fatal("reinstall failed")
	}
	sts := s.Statuses(ctx, false)
	if !sts[0].Previous || sts[0].Installed != "v25.1.0" || sts[0].Latest != "v25.1.0" {
		t.Fatalf("status %+v", sts[0])
	}
	if _, err := s.Start(ctx, Xray, "", "rollback"); err != nil {
		t.Fatal(err)
	}
	if j := waitJob(t, s, Xray); j.State != "succeeded" || j.Version != "v1.0.0" {
		t.Fatalf("rollback %+v", j)
	}
}

func TestChecksumMismatchRefused(t *testing.T) {
	s, _, _ := newSvc(t)
	// Corrupt the digest the service will fetch by pointing at a wrong asset
	// size: easier to wrap the client to tamper the download body.
	tamper := s.HTTP.Transport
	s.HTTP = &http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
		res, err := tamper.RoundTrip(r)
		if err == nil && strings.HasSuffix(r.URL.Path, ".zip") {
			res.Body = io.NopCloser(strings.NewReader("tampered"))
			res.ContentLength = 8
		}
		return res, err
	})}
	if _, err := s.Start(context.Background(), Xray, "", "update"); err != nil {
		t.Fatal(err)
	}
	j := waitJob(t, s, Xray)
	if j.State != "failed" || !strings.Contains(j.Error, "checksum mismatch") {
		t.Fatalf("job %+v", j)
	}
	if _, err := os.Stat(s.Paths.XrayBinary()); err == nil {
		t.Fatal("nothing may be installed after a checksum failure")
	}
}

type roundTrip func(*http.Request) (*http.Response, error)

func (f roundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestInstallMosdnsAndGeodata(t *testing.T) {
	s, sd, _ := newSvc(t)
	ctx := context.Background()
	sd.SetActive("edgeroutegw-mosdns.service", false)
	if _, err := s.Start(ctx, Mosdns, "v5.3.4", "update"); err != nil {
		t.Fatal(err)
	}
	if j := waitJob(t, s, Mosdns); j.State != "succeeded" {
		t.Fatalf("mosdns %+v", j)
	}
	if v := s.Installed(ctx, Mosdns); v != "v5.3.4" {
		t.Fatalf("mosdns installed %q", v)
	}
	if sd.Restarts["edgeroutegw-mosdns.service"] != 0 {
		t.Fatal("inactive unit must not be restarted by the updater")
	}
	if _, err := s.Start(ctx, Geodata, "", "update"); err != nil {
		t.Fatal(err)
	}
	if j := waitJob(t, s, Geodata); j.State != "succeeded" || j.Version != "202610200000" {
		t.Fatalf("geodata %+v", j)
	}
	b, _ := os.ReadFile(s.Paths.GeoSite())
	if string(b) != "newsite" || s.Installed(ctx, Geodata) != "202610200000" {
		t.Fatalf("geodata content %q ver %q", b, s.Installed(ctx, Geodata))
	}
	rels, err := s.Versions(ctx, Xray)
	if err != nil || len(rels) != 2 || rels[0].Tag != "v25.1.0" {
		t.Fatalf("versions %v %v (path-traversal tag must be dropped)", rels, err)
	}
}

func TestSchedule(t *testing.T) {
	s, _, _ := newSvc(t)
	ctx := context.Background()
	if s.Schedule(ctx) != "off" {
		t.Fatal("default schedule")
	}
	if err := s.SetSchedule(ctx, "daily 25:00"); err == nil {
		t.Fatal("bad time accepted")
	}
	if err := s.SetSchedule(ctx, "daily 04:30"); err != nil || s.Schedule(ctx) != "daily 04:30" {
		t.Fatalf("set schedule %v %q", err, s.Schedule(ctx))
	}
	if d, _ := parseDaily("daily 04:30"); d != 4*time.Hour+30*time.Minute {
		t.Fatalf("offset %v", d)
	}
}
