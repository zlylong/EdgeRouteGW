package update

import (
	"context"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// BundleDir is where the edgeroutegw-components package puts pre-fetched
// binaries and geodata. Nothing here is ever modified by the service.
const BundleDir = "/usr/share/edgeroutegw/components"

// Settings keys for the download path.
const (
	ProxyKey  = "download.proxy"         // e.g. socks5://127.0.0.1:10808 (the gateway's own tunnel)
	MirrorKey = "download.github_mirror" // e.g. https://ghproxy.example/ prefixed before GitHub URLs
)

// Bundled describes what the bundle offers for a component.
type Bundled struct {
	Version string `json:"version"`
	Path    string `json:"-"`
}

// bundleVersions reads VERSIONS from the bundle dir.
func (s *Service) bundleVersions() map[string]string {
	dir := s.BundleDir
	if dir == "" {
		dir = BundleDir
	}
	b, err := os.ReadFile(filepath.Join(dir, "VERSIONS"))
	if err != nil {
		return nil
	}
	out := map[string]string{}
	for _, line := range strings.Split(string(b), "\n") {
		if k, v, ok := strings.Cut(strings.TrimSpace(line), "="); ok {
			out[k] = v
		}
	}
	return out
}

// bundled returns the bundle entry for name, if present.
func (s *Service) bundled(name string) *Bundled {
	vers := s.bundleVersions()
	if vers == nil {
		return nil
	}
	dir := s.BundleDir
	if dir == "" {
		dir = BundleDir
	}
	var path string
	switch name {
	case Xray:
		path = filepath.Join(dir, "xray", "xray")
	case Mosdns:
		path = filepath.Join(dir, "mosdns", "mosdns")
	case Geodata:
		path = filepath.Join(dir, "geodata", "geosite.dat")
	default:
		return nil
	}
	if _, err := os.Stat(path); err != nil {
		return nil
	}
	v := vers[name]
	if v == "" {
		v = "bundled"
	}
	return &Bundled{Version: v, Path: path}
}

// installBundled copies the bundled files for a component into the data
// directory (same swap semantics as a download: .prev kept, unit restarted).
func (s *Service) installBundled(ctx context.Context, job *Job) error {
	b := s.bundled(job.Component)
	if b == nil {
		return fmt.Errorf("no bundled %s (install the edgeroutegw-components package)", job.Component)
	}
	s.setVersion(job, b.Version)
	s.progress(job, "copying bundled files", 30)
	switch job.Component {
	case Geodata:
		dir := filepath.Dir(b.Path)
		if err := os.MkdirAll(s.Paths.GeoDir(), 0o755); err != nil {
			return err
		}
		for _, f := range []string{"geoip.dat", "geosite.dat"} {
			if err := SwapFile(filepath.Join(dir, f), filepath.Join(s.Paths.GeoDir(), f), 0o644); err != nil {
				return err
			}
		}
		ver, _ := os.ReadFile(filepath.Join(dir, "geodata.ver"))
		if len(ver) == 0 {
			ver = []byte(b.Version + "\n")
		}
		return os.WriteFile(filepath.Join(s.Paths.GeoDir(), "geodata.ver"), ver, 0o644)
	default:
		dst := s.binaryPath(job.Component)
		if err := SwapFile(b.Path, dst, 0o755); err != nil {
			return err
		}
		s.progress(job, "restarting service", 80)
		return s.restartOrRollback(ctx, job.Component, dst)
	}
}

// SeedFromBundle installs every bundled component that is not installed
// yet. It runs at startup and on SIGHUP (the components package's postinst
// sends one), so an offline install needs no clicks in the UI.
func (s *Service) SeedFromBundle(ctx context.Context) (seeded []string) {
	for _, name := range []string{Xray, Mosdns, Geodata} {
		if s.Installed(ctx, name) != "" || s.bundled(name) == nil {
			continue
		}
		job := &Job{ID: fmt.Sprintf("%s-seed-%d", name, time.Now().UnixNano()), Component: name, Action: "install", State: "running", StartedAt: time.Now()}
		if err := s.installBundled(ctx, job); err != nil {
			s.Log.Warn("seed from bundle failed", "component", name, "err", err)
			continue
		}
		seeded = append(seeded, name+" "+job.Version)
	}
	if len(seeded) > 0 {
		s.Log.Info("components seeded from bundle", "components", seeded)
		if s.Notify != nil {
			s.Notify()
		}
	}
	return seeded
}

// --- proxy / mirror ---

// DownloadSettings is what the UI edits.
type DownloadSettings struct {
	Proxy  string `json:"proxy"`
	Mirror string `json:"github_mirror"`
}

// GetDownloadSettings reads the stored settings.
func (s *Service) GetDownloadSettings(ctx context.Context) DownloadSettings {
	p, _ := s.Store.GetSetting(ctx, ProxyKey)
	m, _ := s.Store.GetSetting(ctx, MirrorKey)
	return DownloadSettings{Proxy: p, Mirror: m}
}

// SetDownloadSettings validates, stores and applies proxy and mirror.
func (s *Service) SetDownloadSettings(ctx context.Context, d DownloadSettings) error {
	d.Proxy, d.Mirror = strings.TrimSpace(d.Proxy), strings.TrimSpace(d.Mirror)
	if d.Proxy != "" {
		u, err := url.Parse(d.Proxy)
		if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https" && u.Scheme != "socks5" && u.Scheme != "socks5h") {
			return fmt.Errorf("proxy must be http(s)://host:port or socks5://host:port")
		}
	}
	if d.Mirror != "" {
		u, err := url.Parse(d.Mirror)
		if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
			return fmt.Errorf("mirror must be an http(s) URL prefix")
		}
		if !strings.HasSuffix(d.Mirror, "/") {
			d.Mirror += "/"
		}
	}
	if err := s.Store.SetSetting(ctx, ProxyKey, d.Proxy); err != nil {
		return err
	}
	if err := s.Store.SetSetting(ctx, MirrorKey, d.Mirror); err != nil {
		return err
	}
	s.ApplyDownloadSettings(ctx)
	return nil
}

// ApplyDownloadSettings rebuilds the HTTP clients from the stored settings.
func (s *Service) ApplyDownloadSettings(ctx context.Context) {
	d := s.GetDownloadSettings(ctx)
	transport := http.DefaultTransport.(*http.Transport).Clone()
	if d.Proxy != "" {
		if u, err := url.Parse(d.Proxy); err == nil {
			transport.Proxy = http.ProxyURL(u)
		}
	} else {
		transport.Proxy = nil // never inherit the service's own environment proxy by accident
	}
	s.mu.Lock()
	s.GitHub.Client = &http.Client{Timeout: 20 * time.Second, Transport: transport}
	s.HTTP = &http.Client{Timeout: 15 * time.Minute, Transport: transport}
	s.GitHub.Mirror = d.Mirror
	s.mu.Unlock()
}

// --- local upload ---

// InstallUpload installs a component from an uploaded official release
// archive (the Xray/Mosdns zip or the geodata rules.zip). sha256 is the
// operator-supplied expected digest; when empty the install is marked
// unverified in the event log. The caller holds the admin session.
func (s *Service) InstallUpload(ctx context.Context, name string, file multipart.File, size int64, sha256hex string) (*Job, error) {
	if s.repo(name) == "" || name == Self {
		return nil, fmt.Errorf("unknown component %q", name)
	}
	if size <= 0 || size > 512<<20 {
		return nil, fmt.Errorf("archive size out of range")
	}
	work, err := os.MkdirTemp(s.Paths.DataDir, ".upload-"+name+"-*")
	if err != nil {
		return nil, err
	}
	zipPath := filepath.Join(work, "upload.zip")
	out, err := os.Create(zipPath)
	if err != nil {
		os.RemoveAll(work)
		return nil, err
	}
	if _, err := io.Copy(out, io.LimitReader(file, 512<<20)); err != nil {
		out.Close()
		os.RemoveAll(work)
		return nil, err
	}
	out.Close()
	got, err := fileSHA256(zipPath)
	if err != nil {
		os.RemoveAll(work)
		return nil, err
	}
	if sha256hex != "" && !strings.EqualFold(sha256hex, got) {
		os.RemoveAll(work)
		return nil, fmt.Errorf("checksum mismatch: archive is %s", got)
	}
	s.mu.Lock()
	for _, j := range s.jobs {
		if j.State == "running" {
			s.mu.Unlock()
			os.RemoveAll(work)
			return nil, ErrBusy
		}
	}
	job := &Job{ID: fmt.Sprintf("%s-upload-%d", name, time.Now().UnixNano()), Component: name, Version: "upload:" + got[:12], Action: "install", State: "running", Step: "queued", StartedAt: time.Now()}
	s.jobs[name] = job
	c := *job
	s.mu.Unlock()
	go func() {
		defer os.RemoveAll(work)
		s.runUploadJob(context.Background(), job, zipPath, sha256hex == "")
	}()
	return &c, nil
}

func (s *Service) runUploadJob(ctx context.Context, job *Job, zipPath string, unverified bool) {
	err := s.installArchive(ctx, job, zipPath)
	s.mu.Lock()
	job.EndedAt = time.Now()
	if err != nil {
		job.State, job.Error, job.Step = "failed", err.Error(), "failed"
		s.lastErr[job.Component] = err.Error()
	} else {
		job.State, job.Step, job.Percent = "succeeded", "done", 100
		delete(s.lastErr, job.Component)
	}
	c := *job
	s.mu.Unlock()
	msg := fmt.Sprintf("%s installed from uploaded archive %s", job.Component, job.Version)
	if unverified {
		msg += " (checksum not verified by the operator)"
	}
	if err != nil {
		s.Log.Error("upload install failed", "component", job.Component, "err", err)
	} else {
		s.Log.Warn(msg)
		if s.Notify != nil {
			s.Notify()
		}
	}
	if s.Bus != nil {
		s.Bus.Publish(EventDone, c)
	}
}

// installArchive unpacks a verified-or-acknowledged archive the same way a
// download is installed.
func (s *Service) installArchive(ctx context.Context, job *Job, zipPath string) error {
	work := filepath.Dir(zipPath)
	switch job.Component {
	case Geodata:
		files, err := Unzip(zipPath, work, []string{"geoip.dat", "geosite.dat"})
		if err != nil {
			return err
		}
		if err := os.MkdirAll(s.Paths.GeoDir(), 0o755); err != nil {
			return err
		}
		for _, f := range []string{"geoip.dat", "geosite.dat"} {
			if err := SwapFile(files[f], filepath.Join(s.Paths.GeoDir(), f), 0o644); err != nil {
				return err
			}
		}
		return os.WriteFile(filepath.Join(s.Paths.GeoDir(), "geodata.ver"), []byte(job.Version+"\n"), 0o644)
	default:
		files, err := Unzip(zipPath, work, []string{job.Component})
		if err != nil {
			return err
		}
		bin := files[job.Component]
		if err := os.Chmod(bin, 0o755); err != nil {
			return err
		}
		s.progress(job, "smoke test", 60)
		if res, err := s.Run.Run(ctx, bin, "version"); err != nil {
			return fmt.Errorf("binary failed smoke test: %s", strings.TrimSpace(string(res.Output)))
		}
		dst := s.binaryPath(job.Component)
		if err := SwapFile(bin, dst, 0o755); err != nil {
			return err
		}
		return s.restartOrRollback(ctx, job.Component, dst)
	}
}
