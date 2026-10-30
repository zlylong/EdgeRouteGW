package observe

import (
	"context"
	"log/slog"
	"os"
	"sync/atomic"
	"time"
)

// LogCap keeps the runtime log files Xray and Mosdns append to from filling
// /run (a tmpfs of a few hundred MB): whenever one grows past the limit it is
// truncated in place. Both daemons open their log with O_APPEND, so writes
// simply continue at offset 0, and the connection tracker reopens a file
// it sees shrink. The content is a live view only (connections, Mosdns
// diagnostics); durable data lives in the database.
type LogCap struct {
	Log      *slog.Logger
	Paths    []string
	Interval time.Duration
	maxBytes atomic.Int64
}

// DefaultLogCapBytes is the limit until the retention settings say otherwise.
const DefaultLogCapBytes = 64 << 20

// NewLogCap caps the given files at DefaultLogCapBytes, checked every minute.
func NewLogCap(log *slog.Logger, paths ...string) *LogCap {
	c := &LogCap{Log: log, Paths: paths, Interval: time.Minute}
	c.maxBytes.Store(DefaultLogCapBytes)
	return c
}

// MaxBytes is the current limit.
func (c *LogCap) MaxBytes() int64 { return c.maxBytes.Load() }

// SetMaxBytes changes the limit; the next check applies it.
func (c *LogCap) SetMaxBytes(n int64) {
	if n > 0 {
		c.maxBytes.Store(n)
	}
}

// FileUsage is one capped file and its current size (0 when absent).
type FileUsage struct {
	Path  string `json:"path"`
	Bytes int64  `json:"bytes"`
}

// Usage reports every capped file.
func (c *LogCap) Usage() []FileUsage {
	out := make([]FileUsage, 0, len(c.Paths))
	for _, p := range c.Paths {
		u := FileUsage{Path: p}
		if st, err := os.Stat(p); err == nil {
			u.Bytes = st.Size()
		}
		out = append(out, u)
	}
	return out
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
	limit := c.MaxBytes()
	for _, p := range c.Paths {
		st, err := os.Stat(p)
		if err != nil || st.Size() <= limit {
			continue
		}
		if err := os.Truncate(p, 0); err != nil {
			c.Log.Warn("log cap: truncate failed", "file", p, "err", err)
			continue
		}
		n++
		c.Log.Info("log cap: truncated runtime log", "file", p, "was_bytes", st.Size(), "limit_bytes", limit)
	}
	return n
}

// Clear truncates every file now regardless of size and returns the bytes
// freed. The operator's "clean up now".
func (c *LogCap) Clear() int64 {
	var freed int64
	for _, p := range c.Paths {
		st, err := os.Stat(p)
		if err != nil || st.Size() == 0 {
			continue
		}
		if err := os.Truncate(p, 0); err != nil {
			c.Log.Warn("log cap: truncate failed", "file", p, "err", err)
			continue
		}
		freed += st.Size()
	}
	return freed
}
