package update

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/zlylong/EdgeRouteGW/internal/version"
)

// Self is the component name for EdgeRouteGW itself.
const Self = "edgeroutegw"

// RepoSelf is where 2.x releases live.
const RepoSelf = "zlylong/EdgeRouteGW"

// SelfDebAsset is the .deb asset name for this machine at version tag.
func SelfDebAsset(tag string) (string, error) {
	switch runtime.GOARCH {
	case "amd64", "arm64":
		return fmt.Sprintf("edgeroutegw_%s_%s.deb", strings.TrimPrefix(tag, "v"), runtime.GOARCH), nil
	}
	return "", fmt.Errorf("unsupported architecture %s", runtime.GOARCH)
}

// selfLatest returns the latest 2.x release tag (1.x tags are skipped: the
// repository's "latest" may still point at a v1 release). Pre-releases are
// offered only to a gateway that already runs one.
func (s *Service) selfLatest(ctx context.Context) (string, error) {
	rels, err := s.GitHub.Releases(ctx, RepoSelf, 30)
	if err != nil {
		return "", err
	}
	allowPre := strings.Contains(version.Get().Version, "-")
	for _, r := range rels {
		if r.Prerelease && !allowPre {
			continue
		}
		if strings.HasPrefix(r.Tag, "v2.") {
			return r.Tag, nil
		}
	}
	return "", fmt.Errorf("no 2.x release found")
}

// selfStatus describes the running binary.
func (s *Service) selfStatus(ctx context.Context, refresh bool) Status {
	st := Status{Name: Self, Installed: version.Get().Version}
	s.mu.Lock()
	if j, ok := s.jobs[Self]; ok {
		c := *j
		st.Running = &c
	}
	st.LastError = s.lastErr[Self]
	li, ok := s.latest[Self]
	s.mu.Unlock()
	if refresh || !ok || time.Since(li.at) > 10*time.Minute {
		if tag, err := s.selfLatest(ctx); err == nil {
			li = latestInfo{tag: tag, at: time.Now()}
			s.mu.Lock()
			s.latest[Self] = li
			s.mu.Unlock()
		} else if refresh {
			st.LastError = "check latest: " + err.Error()
		}
	}
	if li.tag != "" {
		st.Latest = li.tag
		st.CheckedAt = li.at.UTC().Format(time.RFC3339)
	}
	return st
}

// installSelf downloads the release .deb, verifies it against SHA256SUMS,
// installs it with dpkg and schedules a detached restart of the unit: the
// process cannot restart itself from inside the request that asked for it.
func (s *Service) installSelf(ctx context.Context, job *Job) error {
	if os.Geteuid() != 0 && !s.SkipRootCheck {
		return fmt.Errorf("self-update needs root (dpkg); run edgeroutegw as the system service")
	}
	tag := job.Version
	asset, err := SelfDebAsset(tag)
	if err != nil {
		return err
	}
	s.progress(job, "fetching checksums", 5)
	sums, err := s.GitHub.Text(ctx, s.GitHub.AssetURL(RepoSelf, tag, "SHA256SUMS"))
	if err != nil {
		return fmt.Errorf("SHA256SUMS: %w", err)
	}
	want := ""
	for _, line := range strings.Split(sums, "\n") {
		f := strings.Fields(line)
		if len(f) == 2 && strings.TrimPrefix(f[1], "*") == asset {
			want = f[0]
		}
	}
	if want == "" {
		return fmt.Errorf("SHA256SUMS has no entry for %s", asset)
	}
	work, err := os.MkdirTemp(s.Paths.DataDir, ".stage-self-*")
	if err != nil {
		return err
	}
	deb := filepath.Join(work, asset)
	cleanup := true
	defer func() {
		if cleanup {
			os.RemoveAll(work)
		}
	}()
	s.progress(job, "downloading "+asset, 10)
	if err := Download(ctx, s.HTTP, s.GitHub.AssetURL(RepoSelf, tag, asset), deb, want, func(done, total int64) {
		if total > 0 {
			s.progress(job, "downloading "+asset, 10+int(done*60/total))
		}
	}); err != nil {
		return err
	}
	s.progress(job, "installing package", 75)
	// dpkg runs in a transient unit: our own unit has ProtectSystem=strict,
	// which makes /var/lib/dpkg read-only. EDGEROUTEGW_SELFUPDATE=1 tells
	// prerm and postinst to leave the running service alone (prerm would
	// otherwise stop us mid-job); its output goes to a file, not a pipe to
	// this process, so nothing breaks if we are restarted early.
	logPath := filepath.Join(work, "dpkg.log")
	if res, err := s.Run.Run(ctx, "systemd-run", "--wait", "--collect", "--quiet",
		"--setenv=EDGEROUTEGW_SELFUPDATE=1", "--setenv=DEBIAN_FRONTEND=noninteractive",
		fmt.Sprintf("--unit=edgeroutegw-selfupdate-%d", time.Now().Unix()),
		"/bin/sh", "-c", "dpkg -i "+shellQuote(deb)+" >"+shellQuote(logPath)+" 2>&1"); err != nil {
		out, _ := os.ReadFile(logPath)
		if len(out) == 0 {
			out = res.Output
		}
		return fmt.Errorf("dpkg -i: %s", strings.TrimSpace(string(out)))
	}
	s.progress(job, "scheduling service restart", 95)
	// Restart in a few seconds from a transient unit so this request can
	// finish and the client sees the job succeed. The stage directory is
	// removed by the same unit once the restart has been issued.
	cleanup = false
	if res, err := s.Run.Run(ctx, "systemd-run", "--on-active=3", "--unit=edgeroutegw-selfrestart", "--collect",
		"/bin/sh", "-c", "systemctl daemon-reload; systemctl restart edgeroutegw.service; rm -rf "+shellQuote(work)); err != nil {
		s.Log.Warn("could not schedule restart; restart edgeroutegw.service manually", "err", err, "out", strings.TrimSpace(string(res.Output)))
	}
	return nil
}

func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }
