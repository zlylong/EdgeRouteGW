package observe

import (
	"context"
	"log/slog"
	"os"
	"time"
)

// LogCap keeps the runtime log files Xray and Mosdns append to from filling
// /run (a tmpfs of a few hundred MB): whenever one grows past MaxBytes it is
// truncated in place. Both daemons open their log with O_APPEND, so writes
// simply continue at offset 0, and the connection tracker reopens a file
// it sees shrink. The content is a live view only (connections, Mosdns
// diagnostics); durable data lives in the database.
type LogCap struct {
	Log      *slog.Logger
	Paths    []string
	MaxBytes int64
	Interval time.Duration
}

// NewLogCap caps the given files at 64 MB, checked every minute.
func NewLogCap(log *slog.Logger, paths ...string) *LogCap {
	return &LogCap{Log: log, Paths: paths, MaxBytes: 64 << 20, Interval: time.Minute}
}

// Run checks until ctx ends.
func (c *LogCap) Run(ctx context.Context) error {
	c.Check()
	t := time.NewTicker(c.Interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
			c.Check()
		}
	}
}

// Check truncates every file over the limit once; it returns how many it
// truncated.
func (c *LogCap) Check() int {
	n := 0
	for _, p := range c.Paths {
		st, err := os.Stat(p)
		if err != nil || st.Size() <= c.MaxBytes {
			continue
		}
		if err := os.Truncate(p, 0); err != nil {
			c.Log.Warn("log cap: truncate failed", "file", p, "err", err)
			continue
		}
		n++
		c.Log.Info("log cap: truncated runtime log", "file", p, "was_bytes", st.Size(), "limit_bytes", c.MaxBytes)
	}
	return n
}
