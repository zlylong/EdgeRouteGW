package update

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/zlylong/EdgeRouteGW/internal/driver/execx"
	"github.com/zlylong/EdgeRouteGW/internal/driver/systemd"
	"github.com/zlylong/EdgeRouteGW/internal/events"
	"github.com/zlylong/EdgeRouteGW/internal/model"
	"github.com/zlylong/EdgeRouteGW/internal/paths"
	"github.com/zlylong/EdgeRouteGW/internal/store"
)

// Component names.
const (
	Xray    = "xray"
	Mosdns  = "mosdns"
	Geodata = "geodata"
)

// Event types on the bus.
const (
	EventProgress events.Type = "update.progress"
	EventDone     events.Type = "update.done"
)

// Status describes one component for the UI.
type Status struct {
	Name      string `json:"name"`
	Installed string `json:"installed"` // version or "" when absent
	Previous  bool   `json:"has_previous"`
	// Bundled is the version shipped by edgeroutegw-components, if present.
	Bundled   *Bundled `json:"bundled,omitempty"`
	Latest    string   `json:"latest,omitempty"`
	Running   *Job     `json:"job,omitempty"`
	LastError string   `json:"last_error,omitempty"`
	CheckedAt string   `json:"checked_at,omitempty"`
}

// Job is an in-flight or finished update.
type Job struct {
	ID        string    `json:"id"`
	Component string    `json:"component"`
	Version   string    `json:"version"`
	Action    string    `json:"action"` // update | rollback | install
	State     string    `json:"state"`  // running | succeeded | failed
	Step      string    `json:"step"`
	Percent   int       `json:"percent"`
	Error     string    `json:"error,omitempty"`
	StartedAt time.Time `json:"started_at"`
	EndedAt   time.Time `json:"ended_at,omitempty"`
}

// Service runs component updates one at a time.
type Service struct {
	Paths   paths.Paths
	Store   *store.Store
	GitHub  *GitHub
	HTTP    *http.Client
	Run     execx.Runner
	Systemd systemd.Manager
	Bus     *events.Bus
	Log     *slog.Logger
	// Notify wakes the reconciler after geodata changed or a binary was
	// installed (so a down unit gets started).
	Notify func()
	// Units are the systemd units to restart after a binary swap.
	XrayUnit, MosdnsUnit string
	// BundleDir overrides the components bundle location (tests).
	BundleDir string

	mu      sync.Mutex
	jobs    map[string]*Job // by component
	latest  map[string]latestInfo
	lastErr map[string]string
}

type latestInfo struct {
	tag string
	at  time.Time
}

// New wires the production service.
func New(p paths.Paths, st *store.Store, run execx.Runner, sd systemd.Manager, bus *events.Bus, notify func(), log *slog.Logger) *Service {
	return &Service{Paths: p, Store: st, GitHub: NewGitHub(), HTTP: &http.Client{Timeout: 15 * time.Minute}, Run: run, Systemd: sd, Bus: bus, Log: log, Notify: notify,
		XrayUnit: "edgeroutegw-xray.service", MosdnsUnit: "edgeroutegw-mosdns.service", jobs: map[string]*Job{}, latest: map[string]latestInfo{}, lastErr: map[string]string{}}
}

// Installed returns the installed version of a component ("" if absent).
func (s *Service) Installed(ctx context.Context, name string) string {
	switch name {
	case Xray:
		if _, err := os.Stat(s.Paths.XrayBinary()); err != nil {
			return ""
		}
		res, err := s.Run.Run(ctx, s.Paths.XrayBinary(), "version")
		if err != nil {
			return "unknown"
		}
		return parseXrayVersion(string(res.Output))
	case Mosdns:
		if _, err := os.Stat(s.Paths.MosdnsBinary()); err != nil {
			return ""
		}
		res, err := s.Run.Run(ctx, s.Paths.MosdnsBinary(), "version")
		if err != nil {
			return "unknown"
		}
		return parseMosdnsVersion(string(res.Output))
	case Geodata:
		b, err := os.ReadFile(filepath.Join(s.Paths.GeoDir(), "geodata.ver"))
		if err != nil {
			if _, err := os.Stat(s.Paths.GeoSite()); err == nil {
				return "unknown"
			}
			return ""
		}
		return strings.TrimSpace(string(b))
	}
	return ""
}

func parseXrayVersion(out string) string {
	f := strings.Fields(firstLine(out))
	if len(f) >= 2 && f[0] == "Xray" {
		return "v" + strings.TrimPrefix(f[1], "v")
	}
	return strings.TrimSpace(firstLine(out))
}

// parseMosdnsVersion turns mosdns's git-describe output ("v5.3.4-0-gb732318")
// into the release tag when it is exactly a tag.
func parseMosdnsVersion(out string) string {
	v := strings.TrimSpace(firstLine(out))
	if m := mosdnsDescribe.FindStringSubmatch(v); m != nil {
		return m[1]
	}
	return v
}

var mosdnsDescribe = regexp.MustCompile(`^(v?\d+\.\d+\.\d+)-0-g[0-9a-f]+$`)

func firstLine(s string) string {
	l, _, _ := strings.Cut(s, "\n")
	return l
}

func (s *Service) repo(name string) string {
	switch name {
	case Xray:
		return RepoXray
	case Mosdns:
		return RepoMosdns
	case Geodata:
		return RepoGeodata
	case Self:
		return RepoSelf
	}
	return ""
}

// Statuses lists every component. With refresh, the latest version is
// looked up (cached 10 minutes otherwise).
func (s *Service) Statuses(ctx context.Context, refresh bool) []Status {
	out := make([]Status, 0, 4)
	for _, name := range []string{Xray, Mosdns, Geodata} {
		st := Status{Name: name, Installed: s.Installed(ctx, name)}
		prev := s.binaryPath(name) + ".prev"
		if name == Geodata {
			prev = filepath.Join(s.Paths.GeoDir(), "geosite.dat.prev")
		}
		if _, err := os.Stat(prev); err == nil {
			st.Previous = true
		}
		st.Bundled = s.bundled(name)
		s.mu.Lock()
		if j, ok := s.jobs[name]; ok {
			c := *j
			st.Running = &c
		}
		st.LastError = s.lastErr[name]
		li, ok := s.latest[name]
		s.mu.Unlock()
		if refresh || !ok || time.Since(li.at) > 10*time.Minute {
			if rel, err := s.GitHub.Latest(ctx, s.repo(name)); err == nil {
				li = latestInfo{tag: rel.Tag, at: time.Now()}
				s.mu.Lock()
				s.latest[name] = li
				s.mu.Unlock()
			} else if refresh {
				st.LastError = "check latest: " + err.Error()
			}
		}
		if li.tag != "" {
			st.Latest = li.tag
			st.CheckedAt = li.at.UTC().Format(time.RFC3339)
		}
		out = append(out, st)
	}
	out = append(out, s.selfStatus(ctx, refresh))
	return out
}

// Versions lists available release tags for a component.
func (s *Service) Versions(ctx context.Context, name string) ([]Release, error) {
	repo := s.repo(name)
	if repo == "" {
		return nil, fmt.Errorf("unknown component %q", name)
	}
	return s.GitHub.Releases(ctx, repo, 20)
}

func (s *Service) binaryPath(name string) string {
	switch name {
	case Xray:
		return s.Paths.XrayBinary()
	case Mosdns:
		return s.Paths.MosdnsBinary()
	}
	return ""
}

// ErrBusy means a job is already running.
var ErrBusy = errors.New("another update is already running")

// Start launches an update (version "" = latest), a rollback, or
// "bundled" (install from the components package) in the background and
// returns the job.
func (s *Service) Start(ctx context.Context, name, version, action string) (*Job, error) {
	if s.repo(name) == "" {
		return nil, fmt.Errorf("unknown component %q", name)
	}
	if action == "bundled" {
		b := s.bundled(name)
		if b == nil {
			return nil, fmt.Errorf("no bundled %s available", name)
		}
		version = b.Version
	} else if action != "rollback" {
		action = "update"
		if version == "" || version == "latest" {
			if name == Self {
				tag, err := s.selfLatest(ctx)
				if err != nil {
					return nil, err
				}
				version = tag
			} else {
				rel, err := s.GitHub.Latest(ctx, s.repo(name))
				if err != nil {
					return nil, err
				}
				version = rel.Tag
			}
		}
		if !ValidVersion(version) {
			return nil, fmt.Errorf("invalid version %q", version)
		}
	} else if name == Self {
		return nil, fmt.Errorf("edgeroutegw itself has no rollback here; install the previous .deb")
	}
	s.mu.Lock()
	for _, j := range s.jobs {
		if j.State == "running" {
			s.mu.Unlock()
			return nil, ErrBusy
		}
	}
	job := &Job{ID: fmt.Sprintf("%s-%d", name, time.Now().UnixNano()), Component: name, Version: version, Action: action, State: "running", Step: "queued", StartedAt: time.Now()}
	s.jobs[name] = job
	c := *job
	s.mu.Unlock()
	go s.runJob(context.Background(), job)
	return &c, nil
}

func (s *Service) setVersion(job *Job, v string) {
	s.mu.Lock()
	job.Version = v
	s.mu.Unlock()
}

func (s *Service) progress(job *Job, step string, pct int) {
	s.mu.Lock()
	job.Step, job.Percent = step, pct
	c := *job
	s.mu.Unlock()
	if s.Bus != nil {
		s.Bus.Publish(EventProgress, c)
	}
}

func (s *Service) runJob(ctx context.Context, job *Job) {
	var err error
	switch {
	case job.Action == "bundled":
		err = s.installBundled(ctx, job)
	case job.Action == "rollback":
		err = s.rollback(ctx, job)
	case job.Component == Geodata:
		err = s.installGeodata(ctx, job)
	case job.Component == Self:
		err = s.installSelf(ctx, job)
	default:
		err = s.installBinary(ctx, job)
	}
	s.mu.Lock()
	job.EndedAt = time.Now()
	if err != nil {
		job.State, job.Error, job.Step = "failed", err.Error(), "failed"
		s.lastErr[job.Component] = err.Error()
	} else {
		job.State, job.Step, job.Percent = "succeeded", "done", 100
		delete(s.lastErr, job.Component)
		delete(s.latest, job.Component)
	}
	c := *job
	s.mu.Unlock()
	if err != nil {
		s.Log.Error("component update failed", "component", job.Component, "version", job.Version, "err", err)
		_ = s.Store.AddEvent(ctx, model.Event{Level: model.LevelError, Module: "update", Type: job.Action + ".failed", Message: fmt.Sprintf("%s %s %s: %v", job.Component, job.Action, job.Version, err)})
	} else {
		s.Log.Info("component updated", "component", job.Component, "version", job.Version, "action", job.Action)
		_ = s.Store.AddEvent(ctx, model.Event{Level: model.LevelInfo, Module: "update", Type: job.Action + ".succeeded", Message: fmt.Sprintf("%s %s %s", job.Component, job.Action, job.Version)})
		if s.Notify != nil {
			s.Notify()
		}
	}
	if s.Bus != nil {
		s.Bus.Publish(EventDone, c)
	}
}

// installBinary downloads, verifies, smoke-tests and swaps in Xray or
// Mosdns, restarting its unit and rolling back if it does not come up.
func (s *Service) installBinary(ctx context.Context, job *Job) error {
	name, tag := job.Component, job.Version
	var asset, sum string
	var err error
	s.progress(job, "resolving release", 5)
	switch name {
	case Xray:
		if asset, err = XrayAsset(); err != nil {
			return err
		}
		if sum, err = s.GitHub.XraySHA256(ctx, tag, asset); err != nil {
			return fmt.Errorf("fetch digest: %w", err)
		}
	case Mosdns:
		if asset, err = MosdnsAsset(); err != nil {
			return err
		}
		rel, err := s.GitHub.ReleaseByTag(ctx, RepoMosdns, tag)
		if err != nil {
			return err
		}
		if sum, err = AssetDigest(rel, asset); err != nil {
			return err
		}
	}
	if err := os.MkdirAll(s.Paths.Components(), 0o755); err != nil {
		return err
	}
	work, err := os.MkdirTemp(s.Paths.Components(), ".stage-"+name+"-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(work)
	zipPath := filepath.Join(work, asset)
	s.progress(job, "downloading "+asset, 10)
	if err := Download(ctx, s.HTTP, s.GitHub.AssetURL(s.repo(name), tag, asset), zipPath, sum, func(done, total int64) {
		if total > 0 {
			s.progress(job, "downloading "+asset, 10+int(done*50/total))
		}
	}); err != nil {
		return err
	}
	s.progress(job, "verified, extracting", 65)
	files, err := Unzip(zipPath, work, []string{name})
	if err != nil {
		return err
	}
	bin := files[name]
	if err := os.Chmod(bin, 0o755); err != nil {
		return err
	}
	s.progress(job, "smoke test", 75)
	if res, err := s.Run.Run(ctx, bin, "version"); err != nil {
		return fmt.Errorf("new binary failed smoke test: %s", strings.TrimSpace(string(res.Output)))
	}
	s.progress(job, "installing", 85)
	dst := s.binaryPath(name)
	if err := SwapFile(bin, dst, 0o755); err != nil {
		return fmt.Errorf("install: %w", err)
	}
	if name == Xray {
		// Keep the geodata the archive ships only when none is installed.
		if _, err := os.Stat(s.Paths.GeoSite()); err != nil {
			if geo, err := Unzip(zipPath, work, []string{"geoip.dat", "geosite.dat"}); err == nil {
				_ = os.MkdirAll(s.Paths.GeoDir(), 0o755)
				_ = copyFile(geo["geoip.dat"], s.Paths.GeoIP(), 0o644)
				_ = copyFile(geo["geosite.dat"], s.Paths.GeoSite(), 0o644)
			}
		}
	}
	s.progress(job, "restarting service", 90)
	return s.restartOrRollback(ctx, name, dst)
}

// restartOrRollback restarts the component's unit when it was running and
// restores the previous binary if the new one does not stay up.
func (s *Service) restartOrRollback(ctx context.Context, name, dst string) error {
	unit := s.XrayUnit
	if name == Mosdns {
		unit = s.MosdnsUnit
	}
	st, err := s.Systemd.State(ctx, unit)
	if err != nil || !st.Active() {
		return nil // reconciler starts it when the config is ready
	}
	if err := s.Systemd.Restart(ctx, unit); err == nil {
		time.Sleep(2 * time.Second)
		if st, err := s.Systemd.State(ctx, unit); err == nil && st.Active() {
			return nil
		}
	}
	if rerr := RestorePrev(dst); rerr != nil {
		return fmt.Errorf("new %s did not stay up and rollback failed: %v", name, rerr)
	}
	_ = s.Systemd.Restart(ctx, unit)
	return fmt.Errorf("new %s did not stay up; previous version restored", name)
}

func (s *Service) rollback(ctx context.Context, job *Job) error {
	s.progress(job, "restoring previous version", 20)
	if job.Component == Geodata {
		for _, f := range []string{"geoip.dat", "geosite.dat"} {
			if err := RestorePrev(filepath.Join(s.Paths.GeoDir(), f)); err != nil {
				return err
			}
		}
		_ = RestorePrev(filepath.Join(s.Paths.GeoDir(), "geodata.ver"))
		s.setVersion(job, s.Installed(ctx, Geodata))
		return nil
	}
	dst := s.binaryPath(job.Component)
	if err := RestorePrev(dst); err != nil {
		return err
	}
	s.setVersion(job, s.Installed(ctx, job.Component))
	s.progress(job, "restarting service", 80)
	unit := s.XrayUnit
	if job.Component == Mosdns {
		unit = s.MosdnsUnit
	}
	if st, err := s.Systemd.State(ctx, unit); err == nil && st.Active() {
		return s.Systemd.Restart(ctx, unit)
	}
	return nil
}

// installGeodata fetches rules.zip, verifies it and swaps geoip.dat /
// geosite.dat in place. Xray and Mosdns pick the files up on their next
// restart; the reconciler is notified so DNS lists and routes re-render.
func (s *Service) installGeodata(ctx context.Context, job *Job) error {
	tag := job.Version
	s.progress(job, "fetching checksum", 5)
	sum, err := s.GitHub.GeodataSHA256(ctx, tag)
	if err != nil {
		return err
	}
	work, err := os.MkdirTemp(s.Paths.DataDir, ".stage-geodata-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(work)
	zipPath := filepath.Join(work, "rules.zip")
	s.progress(job, "downloading rules.zip", 10)
	if err := Download(ctx, s.HTTP, s.GitHub.AssetURL(RepoGeodata, tag, "rules.zip"), zipPath, sum, func(done, total int64) {
		if total > 0 {
			s.progress(job, "downloading rules.zip", 10+int(done*60/total))
		}
	}); err != nil {
		return err
	}
	s.progress(job, "verified, extracting", 75)
	files, err := Unzip(zipPath, work, []string{"geoip.dat", "geosite.dat"})
	if err != nil {
		return err
	}
	s.progress(job, "installing", 90)
	if err := os.MkdirAll(s.Paths.GeoDir(), 0o755); err != nil {
		return err
	}
	for _, f := range []string{"geoip.dat", "geosite.dat"} {
		if err := SwapFile(files[f], filepath.Join(s.Paths.GeoDir(), f), 0o644); err != nil {
			return err
		}
	}
	verPath := filepath.Join(s.Paths.GeoDir(), "geodata.ver")
	if old, err := os.ReadFile(verPath); err == nil {
		_ = os.WriteFile(verPath+".prev", old, 0o644)
	}
	return os.WriteFile(verPath, []byte(tag+"\n"), 0o644)
}

// --- geodata schedule ---

// ScheduleKey is the settings key: "" / "off" disables; "daily HH:MM" runs
// every day at that local time.
const ScheduleKey = "geodata.auto_update"

// Schedule returns the stored schedule.
func (s *Service) Schedule(ctx context.Context) string {
	v, _ := s.Store.GetSetting(ctx, ScheduleKey)
	if v == "" {
		return "off"
	}
	return v
}

// SetSchedule validates and stores a schedule.
func (s *Service) SetSchedule(ctx context.Context, v string) error {
	v = strings.TrimSpace(v)
	if v == "off" || v == "" {
		return s.Store.SetSetting(ctx, ScheduleKey, "off")
	}
	if _, err := parseDaily(v); err != nil {
		return err
	}
	return s.Store.SetSetting(ctx, ScheduleKey, v)
}

func parseDaily(v string) (time.Duration, error) {
	hhmm, ok := strings.CutPrefix(v, "daily ")
	if !ok {
		return 0, fmt.Errorf("schedule must be \"off\" or \"daily HH:MM\"")
	}
	t, err := time.Parse("15:04", strings.TrimSpace(hhmm))
	if err != nil {
		return 0, fmt.Errorf("schedule time must be HH:MM")
	}
	return time.Duration(t.Hour())*time.Hour + time.Duration(t.Minute())*time.Minute, nil
}

// RunScheduler updates geodata at the configured time until ctx ends.
func (s *Service) RunScheduler(ctx context.Context) error {
	for {
		sched := s.Schedule(ctx)
		wait := time.Hour
		if sched != "off" {
			if offset, err := parseDaily(sched); err == nil {
				now := time.Now()
				next := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location()).Add(offset)
				if !next.After(now) {
					next = next.Add(24 * time.Hour)
				}
				wait = time.Until(next)
			}
		}
		if wait > time.Hour {
			wait = time.Hour // re-read the schedule at least hourly
		}
		t := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			t.Stop()
			return nil
		case <-t.C:
		}
		if sched == "off" {
			continue
		}
		offset, _ := parseDaily(sched)
		now := time.Now()
		due := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location()).Add(offset)
		if now.Sub(due).Abs() > 2*time.Minute {
			continue // woke up for the hourly re-read, not for the run
		}
		if _, err := s.Start(ctx, Geodata, "", "update"); err != nil && !errors.Is(err, ErrBusy) {
			s.Log.Warn("scheduled geodata update failed to start", "err", err)
		}
	}
}
