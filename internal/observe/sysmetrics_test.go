package observe

import (
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
)

func TestSysMetricsFromFakeProc(t *testing.T) {
	dir := t.TempDir()
	write := func(rel, content string) {
		p := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("stat", "cpu  100 0 100 800 0 0 0 0 0 0\ncpu0 1 2 3 4 5 6 7 8 9 10\n")
	write("loadavg", "0.50 0.25 0.10 1/200 4321\n")
	write("meminfo", "MemTotal:       4000000 kB\nMemFree:         500000 kB\nMemAvailable:   3000000 kB\nSwapTotal:      1000000 kB\nSwapFree:        900000 kB\n")
	write("sys/net/netfilter/nf_conntrack_count", "1234\n")
	write("sys/net/netfilter/nf_conntrack_max", "1048576\n")
	write("uptime", "12345.67 99999.00\n")
	m := NewSysMetrics(slog.New(slog.NewTextHandler(io.Discard, nil)))
	m.ProcRoot = dir
	s := m.Sample()
	if !s.Available || s.CPUPercent != 0 || s.Load1 != 0.5 || s.MemTotal != 4000000*1024 || s.MemUsed != 1000000*1024 || s.SwapUsed != 100000*1024 || s.ConntrackCount != 1234 || s.ConntrackMax != 1048576 || s.UptimeS != 12345 {
		t.Fatalf("first sample %+v", s)
	}
	// 200 more jiffies, 50 of them idle: 75% busy.
	write("stat", "cpu  200 0 150 850 0 0 0 0 0 0\n")
	s = m.Sample()
	if s.CPUPercent < 74.9 || s.CPUPercent > 75.1 {
		t.Fatalf("cpu percent %v", s.CPUPercent)
	}
	// Missing conntrack files report -1, not an error.
	os.RemoveAll(filepath.Join(dir, "sys"))
	if s = m.Sample(); s.ConntrackCount != -1 || s.ConntrackMax != -1 {
		t.Fatalf("conntrack without module %+v", s)
	}
}
