// Package janitor applies the retention settings: it prunes the history
// tables, keeps the runtime logs capped, and bounds the systemd journal, on
// a schedule and on demand.
package janitor

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync"
	"syscall"
	"time"

	"github.com/zlylong/EdgeRouteGW/internal/driver/execx"
	"github.com/zlylong/EdgeRouteGW/internal/observe"
	"github.com/zlylong/EdgeRouteGW/internal/store"
)

// Settings is what the operator controls. Days and MB are integers so the
// UI stays simple; the defaults suit a gateway with a few hundred MB of
// /run and a few GB of disk.
type Settings struct {
	HistoryDays int `json:"history_days"` // events, apply log, revisions, deploy output
	TrafficDays int `json:"traffic_days"` // per-minute traffic samples
	RunLogMB    int `json:"runlog_mb"`    // cap per runtime log file in /run
	JournalDays int `json:"journal_days"` // systemd journal MaxRetentionSec
	JournalMB   int `json:"journal_mb"`   // systemd journal SystemMaxUse
}

// Defaults are used until the operator saves something.
var Defaults = Settings{HistoryDays: 90, TrafficDays: 90, RunLogMB: 64, JournalDays: 30, JournalMB: 500}

// Validate bounds every field to a sane range.
func (s Settings) Validate() error {
	check := func(name string, v, lo, hi int) error {
		if v < lo || v > hi {
			return fmt.Errorf("%s must be between %d and %d", name, lo, hi)
		}
		return nil
	}
	return errors.Join(
		check("history_days", s.HistoryDays, 1, 3650),
		check("traffic_days", s.TrafficDays, 1, 3650),
		check("runlog_mb", s.RunLogMB, 8, 4096),
		check("journal_days", s.JournalDays, 1, 3650),
		check("journal_mb", s.JournalMB, 50, 65536),
	)
}

const keyPrefix = "retention."

// Usage is the current footprint of everything the janitor manages.
type Usage struct {
	DB             store.HistoryUsage  `json:"db"`
	RuntimeLogs    []observe.FileUsage `json:"runtime_logs"`
	RunFSUsed      int64               `json:"run_fs_used"`
	RunFSTotal     int64               `json:"run_fs_total"`
	JournalBytes   int64               `json:"journal_bytes"`
	JournalManaged bool                `json:"journal_managed"`
	LastPruneAt    *time.Time          `json:"last_prune_at,omitempty"`
}

// Report is what one pass did.
type Report struct {
	At                   time.Time         `json:"at"`
	Pruned               store.PruneReport `json:"pruned"`
	RuntimeLogBytesFreed int64             `json:"runtime_log_bytes_freed"`
	DBBytesBefore        int64             `json:"db_bytes_before"`
	DBBytesAfter         int64             `json:"db_bytes_after"`
	JournalBytesBefore   int64             `json:"journal_bytes_before"`
	JournalBytesAfter    int64             `json:"journal_bytes_after"`
	Warnings             []string          `json:"warnings"`
}

// UnitRestarter is the slice of the systemd driver the janitor needs.
type UnitRestarter interface {
	Restart(ctx context.Context, unit string) error
}

// Janitor runs the retention policy.
type Janitor struct {
	Store  *store.Store
	LogCap *observe.LogCap
	Exec   execx.Runner
	// Systemd restarts journald after the drop-in changes; nil disables
	// journal management (tests, -no-reconcile).
	Systemd UnitRestarter
	Log     *slog.Logger
	// RunDir is the tmpfs the runtime logs live on (for the fs gauge).
	RunDir string
	// DropIn is the journald configuration fragment this service owns.
	DropIn string
	// JournalDirs are summed for the journal size.
	JournalDirs []string
	// Interval between automatic passes.
	Interval time.Duration

	mu        sync.Mutex
	lastPrune time.Time
}

// New wires a janitor with the production paths.
func New(st *store.Store, lc *observe.LogCap, run execx.Runner, sd UnitRestarter, runDir string, log *slog.Logger) *Janitor {
	return &Janitor{
		Store: st, LogCap: lc, Exec: run, Systemd: sd, Log: log, RunDir: runDir,
		DropIn:      "/etc/systemd/journald.conf.d/60-edgeroutegw.conf",
		JournalDirs: []string{"/var/log/journal", "/run/log/journal"},
		Interval:    time.Hour,
	}
}

// Settings reads the saved settings, falling back to Defaults per field.
func (j *Janitor) Settings(ctx context.Context) Settings {
	s := Defaults
	read := func(key string, dst *int) {
		v, err := j.Store.GetSetting(ctx, keyPrefix+key)
		if err != nil || v == "" {
			return
		}
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			*dst = n
		}
	}
	read("history_days", &s.HistoryDays)
	read("traffic_days", &s.TrafficDays)
	read("runlog_mb", &s.RunLogMB)
	read("journal_days", &s.JournalDays)
	read("journal_mb", &s.JournalMB)
	return s
}

// SetSettings validates and saves, then applies: the log cap changes at
// once, the journald drop-in is rewritten and journald restarted when its
// values changed, and a prune pass runs so the new limits take effect now.
func (j *Janitor) SetSettings(ctx context.Context, s Settings) (Report, error) {
	if err := s.Validate(); err != nil {
		return Report{}, err
	}
	prev := j.Settings(ctx)
	for k, v := range map[string]int{
		"history_days": s.HistoryDays, "traffic_days": s.TrafficDays, "runlog_mb": s.RunLogMB,
		"journal_days": s.JournalDays, "journal_mb": s.JournalMB,
	} {
		if err := j.Store.SetSetting(ctx, keyPrefix+k, strconv.Itoa(v)); err != nil {
			return Report{}, err
		}
	}
	j.LogCap.SetMaxBytes(int64(s.RunLogMB) << 20)
	var warnings []string
	if prev.JournalDays != s.JournalDays || prev.JournalMB != s.JournalMB {
		if err := j.applyJournal(ctx, s, true); err != nil {
			warnings = append(warnings, err.Error())
		}
	}
	rep, err := j.pass(ctx, s, false)
	rep.Warnings = append(warnings, rep.Warnings...)
	return rep, err
}

// Usage gathers the current footprint.
func (j *Janitor) Usage(ctx context.Context) (Usage, error) {
	u := Usage{RuntimeLogs: j.LogCap.Usage(), JournalManaged: j.journalAvailable()}
	var err error
	if u.DB, err = j.Store.HistoryUsage(ctx); err != nil {
		return u, err
	}
	if j.RunDir != "" {
		var st syscall.Statfs_t
		if e := syscall.Statfs(j.RunDir, &st); e == nil {
			bs := int64(st.Bsize)
			u.RunFSTotal = int64(st.Blocks) * bs
			u.RunFSUsed = (int64(st.Blocks) - int64(st.Bfree)) * bs
		}
	}
	u.JournalBytes = j.journalBytes()
	j.mu.Lock()
	if !j.lastPrune.IsZero() {
		t := j.lastPrune
		u.LastPruneAt = &t
	}
	j.mu.Unlock()
	return u, nil
}

// Prune is the scheduled pass: delete expired history, cap the runtime
// logs, vacuum the journal to its limits.
func (j *Janitor) Prune(ctx context.Context) (Report, error) {
	return j.pass(ctx, j.Settings(ctx), false)
}

// Cleanup is the operator's "clean up now": Prune plus truncating every
// runtime log and compacting the database file.
func (j *Janitor) Cleanup(ctx context.Context) (Report, error) {
	return j.pass(ctx, j.Settings(ctx), true)
}

func (j *Janitor) pass(ctx context.Context, s Settings, full bool) (Report, error) {
	rep := Report{At: time.Now(), Warnings: []string{}}
	if u, err := j.Store.HistoryUsage(ctx); err == nil {
		rep.DBBytesBefore = u.DBBytes + u.WALBytes
	}
	rep.JournalBytesBefore = j.journalBytes()

	pruned, err := j.Store.PruneHistory(ctx, time.Duration(s.HistoryDays)*24*time.Hour, time.Duration(s.TrafficDays)*24*time.Hour)
	rep.Pruned = pruned
	if err != nil {
		return rep, fmt.Errorf("prune history: %w", err)
	}
	if full {
		rep.RuntimeLogBytesFreed = j.LogCap.Clear()
		if err := j.Store.Compact(ctx); err != nil {
			rep.Warnings = append(rep.Warnings, "compact database: "+err.Error())
		}
	} else {
		j.LogCap.Check()
	}
	if err := j.vacuumJournal(ctx, s); err != nil {
		rep.Warnings = append(rep.Warnings, err.Error())
	}
	if u, err := j.Store.HistoryUsage(ctx); err == nil {
		rep.DBBytesAfter = u.DBBytes + u.WALBytes
	}
	rep.JournalBytesAfter = j.journalBytes()

	j.mu.Lock()
	j.lastPrune = rep.At
	j.mu.Unlock()
	if n := pruned.Total(); n > 0 || full {
		j.Log.Info("retention pass", "rows_pruned", n, "runtime_log_freed", rep.RuntimeLogBytesFreed, "journal_before", rep.JournalBytesBefore, "journal_after", rep.JournalBytesAfter, "full", full)
	}
	return rep, nil
}

// Run applies the saved settings at start (log cap, journald drop-in) and
// then prunes every Interval until ctx ends.
func (j *Janitor) Run(ctx context.Context) error {
	s := j.Settings(ctx)
	j.LogCap.SetMaxBytes(int64(s.RunLogMB) << 20)
	if err := j.applyJournal(ctx, s, false); err != nil {
		j.Log.Warn("journald retention not applied", "err", err)
	}
	if _, err := j.Prune(ctx); err != nil {
		j.Log.Warn("retention pass failed", "err", err)
	}
	t := time.NewTicker(j.Interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
			if _, err := j.Prune(ctx); err != nil {
				j.Log.Warn("retention pass failed", "err", err)
			}
		}
	}
}

// --- journald ---

func (j *Janitor) journalAvailable() bool {
	if j.Systemd == nil || j.DropIn == "" {
		return false
	}
	if _, err := os.Stat("/run/systemd/system"); err != nil {
		return false
	}
	_, err := exec.LookPath("journalctl")
	return err == nil
}

func dropInContent(s Settings) string {
	return fmt.Sprintf("# Managed by EdgeRouteGW (system > 日志与保留); edits here are overwritten.\n[Journal]\nSystemMaxUse=%dM\nMaxRetentionSec=%dday\n", s.JournalMB, s.JournalDays)
}

// applyJournal writes the drop-in when its content differs and restarts
// journald so it takes effect; force restarts even when the file already
// matched (the operator just changed the settings).
func (j *Janitor) applyJournal(ctx context.Context, s Settings, force bool) error {
	if !j.journalAvailable() {
		return nil
	}
	want := dropInContent(s)
	have, _ := os.ReadFile(j.DropIn)
	changed := string(have) != want
	if changed {
		tmp := j.DropIn + ".new"
		if err := os.WriteFile(tmp, []byte(want), 0o644); err != nil {
			return fmt.Errorf("write %s: %w", j.DropIn, err)
		}
		if err := os.Rename(tmp, j.DropIn); err != nil {
			_ = os.Remove(tmp)
			return fmt.Errorf("install %s: %w", j.DropIn, err)
		}
	}
	if changed || force {
		if err := j.Systemd.Restart(ctx, "systemd-journald.service"); err != nil {
			return fmt.Errorf("restart systemd-journald: %w", err)
		}
		j.Log.Info("journald retention configured", "max_use_mb", s.JournalMB, "retention_days", s.JournalDays)
	}
	return nil
}

// vacuumJournal removes archived journal files beyond the limits. journald
// enforces them itself over time; this makes a new, lower limit take effect
// at once (and clears what a previous installation left behind).
func (j *Janitor) vacuumJournal(ctx context.Context, s Settings) error {
	if !j.journalAvailable() || j.Exec == nil {
		return nil
	}
	_, err := j.Exec.Run(ctx, "journalctl", "--rotate", fmt.Sprintf("--vacuum-time=%dd", s.JournalDays), fmt.Sprintf("--vacuum-size=%dM", s.JournalMB))
	if err != nil {
		return fmt.Errorf("journal vacuum: %w", err)
	}
	return nil
}

func (j *Janitor) journalBytes() int64 {
	var total int64
	for _, dir := range j.JournalDirs {
		_ = filepath.WalkDir(dir, func(_ string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return nil
			}
			if info, err := d.Info(); err == nil {
				total += info.Size()
			}
			return nil
		})
	}
	return total
}
