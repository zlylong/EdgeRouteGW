package update

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Schedule specs, one per component, stored in settings:
//
//	off
//	daily HH:MM
//	weekly <mon..sun|1..7> HH:MM
//	monthly <1..28> HH:MM
//
// Times are local. Geodata keeps its historical key for compatibility.

// ScheduleKey is the geodata key (1.x importer and backups write it).
const ScheduleKey = "geodata.auto_update"

// ScheduleKeyFor returns the settings key of a component's schedule.
func ScheduleKeyFor(name string) string {
	if name == Geodata {
		return ScheduleKey
	}
	return "update.schedule." + name
}

// Schedulable lists the components that can auto-update.
var Schedulable = []string{Xray, Mosdns, Geodata}

// ScheduleSpec is a parsed schedule.
type ScheduleSpec struct {
	Kind    string // off | daily | weekly | monthly
	Weekday time.Weekday
	Day     int
	Offset  time.Duration // time of day
}

var weekdays = map[string]time.Weekday{
	"mon": 1, "tue": 2, "wed": 3, "thu": 4, "fri": 5, "sat": 6, "sun": 0,
	"1": 1, "2": 2, "3": 3, "4": 4, "5": 5, "6": 6, "7": 0,
}

// ParseSchedule validates a spec string.
func ParseSchedule(v string) (ScheduleSpec, error) {
	f := strings.Fields(strings.ToLower(strings.TrimSpace(v)))
	if len(f) == 0 || f[0] == "off" {
		return ScheduleSpec{Kind: "off"}, nil
	}
	tod := func(s string) (time.Duration, error) {
		t, err := time.Parse("15:04", s)
		if err != nil {
			return 0, fmt.Errorf("schedule time must be HH:MM")
		}
		return time.Duration(t.Hour())*time.Hour + time.Duration(t.Minute())*time.Minute, nil
	}
	switch f[0] {
	case "daily":
		if len(f) != 2 {
			return ScheduleSpec{}, fmt.Errorf("schedule must be \"daily HH:MM\"")
		}
		off, err := tod(f[1])
		return ScheduleSpec{Kind: "daily", Offset: off}, err
	case "weekly":
		if len(f) != 3 {
			return ScheduleSpec{}, fmt.Errorf("schedule must be \"weekly <mon..sun> HH:MM\"")
		}
		wd, ok := weekdays[f[1]]
		if !ok {
			return ScheduleSpec{}, fmt.Errorf("weekday must be mon..sun")
		}
		off, err := tod(f[2])
		return ScheduleSpec{Kind: "weekly", Weekday: wd, Offset: off}, err
	case "monthly":
		if len(f) != 3 {
			return ScheduleSpec{}, fmt.Errorf("schedule must be \"monthly <1..28> HH:MM\"")
		}
		day, err := strconv.Atoi(f[1])
		if err != nil || day < 1 || day > 28 {
			return ScheduleSpec{}, fmt.Errorf("day of month must be 1..28")
		}
		off, err := tod(f[2])
		return ScheduleSpec{Kind: "monthly", Day: day, Offset: off}, err
	}
	return ScheduleSpec{}, fmt.Errorf("schedule must start with off, daily, weekly or monthly")
}

// Next returns the first run strictly after now.
func (s ScheduleSpec) Next(now time.Time) (time.Time, bool) {
	if s.Kind == "off" {
		return time.Time{}, false
	}
	day := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	switch s.Kind {
	case "daily":
		n := day.Add(s.Offset)
		if !n.After(now) {
			n = n.AddDate(0, 0, 1)
		}
		return n, true
	case "weekly":
		n := day.AddDate(0, 0, (int(s.Weekday)-int(day.Weekday())+7)%7).Add(s.Offset)
		if !n.After(now) {
			n = n.AddDate(0, 0, 7)
		}
		return n, true
	case "monthly":
		n := time.Date(now.Year(), now.Month(), s.Day, 0, 0, 0, 0, now.Location()).Add(s.Offset)
		if !n.After(now) {
			n = time.Date(now.Year(), now.Month()+1, s.Day, 0, 0, 0, 0, now.Location()).Add(s.Offset)
		}
		return n, true
	}
	return time.Time{}, false
}

// ScheduleOf returns a component's stored schedule ("off" when unset).
func (s *Service) ScheduleOf(ctx context.Context, name string) string {
	v, _ := s.Store.GetSetting(ctx, ScheduleKeyFor(name))
	if strings.TrimSpace(v) == "" {
		return "off"
	}
	return v
}

// SetScheduleOf validates and stores a component's schedule.
func (s *Service) SetScheduleOf(ctx context.Context, name, v string) error {
	ok := false
	for _, n := range Schedulable {
		if n == name {
			ok = true
		}
	}
	if !ok {
		return fmt.Errorf("component %q cannot be scheduled", name)
	}
	spec, err := ParseSchedule(v)
	if err != nil {
		return err
	}
	if spec.Kind == "off" {
		return s.Store.SetSetting(ctx, ScheduleKeyFor(name), "off")
	}
	return s.Store.SetSetting(ctx, ScheduleKeyFor(name), strings.Join(strings.Fields(strings.ToLower(strings.TrimSpace(v))), " "))
}

// Schedule and SetSchedule keep the geodata-only API.
func (s *Service) Schedule(ctx context.Context) string { return s.ScheduleOf(ctx, Geodata) }
func (s *Service) SetSchedule(ctx context.Context, v string) error {
	return s.SetScheduleOf(ctx, Geodata, v)
}

// RunScheduler runs each component's scheduled update until ctx ends. A
// run only starts an update when GitHub has a version other than the
// installed one, so a daily Xray schedule does not restart Xray daily.
func (s *Service) RunScheduler(ctx context.Context) error {
	for {
		now := time.Now()
		wait := time.Hour // re-read schedules at least hourly
		var due []string
		for _, name := range Schedulable {
			spec, err := ParseSchedule(s.ScheduleOf(ctx, name))
			if err != nil || spec.Kind == "off" {
				continue
			}
			// A slot due within the last 2 minutes still counts (timer jitter).
			next, ok := spec.Next(now.Add(-2 * time.Minute))
			if !ok {
				continue
			}
			if !next.After(now) {
				due = append(due, name)
				continue
			}
			if d := time.Until(next); d < wait {
				wait = d
			}
		}
		for _, name := range due {
			s.scheduledRun(ctx, name)
		}
		if len(due) > 0 && wait < 3*time.Minute {
			wait = 3 * time.Minute // never fire the same slot twice
		}
		t := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			t.Stop()
			return nil
		case <-t.C:
		}
	}
}

func (s *Service) scheduledRun(ctx context.Context, name string) {
	installed := s.Installed(ctx, name)
	rel, err := s.GitHub.Latest(ctx, s.repo(name))
	if err != nil {
		s.Log.Warn("scheduled update: cannot check latest", "component", name, "err", err)
		return
	}
	if installed != "" && rel.Tag == installed {
		s.Log.Info("scheduled update: already latest", "component", name, "version", installed)
		return
	}
	if _, err := s.Start(ctx, name, rel.Tag, "update"); err != nil {
		if !errors.Is(err, ErrBusy) {
			s.Log.Warn("scheduled update failed to start", "component", name, "err", err)
		}
		return
	}
	s.Log.Info("scheduled update started", "component", name, "from", installed, "to", rel.Tag)
}
