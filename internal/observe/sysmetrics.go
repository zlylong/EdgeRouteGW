package observe

import (
	"bufio"
	"context"
	"log/slog"
	"os"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

// SysSample is a point-in-time view of the host the gateway runs on: CPU,
// memory, load, conntrack table and uptime. Everything comes from /proc, so
// it works inside the service's sandbox without extra tools.
type SysSample struct {
	At        time.Time `json:"at"`
	Available bool      `json:"available"`
	// CPUPercent is overall utilisation across all CPUs since the previous
	// sample (0..100); CPUs is the online CPU count.
	CPUPercent float64 `json:"cpu_percent"`
	CPUs       int     `json:"cpus"`
	Load1      float64 `json:"load1"`
	Load5      float64 `json:"load5"`
	Load15     float64 `json:"load15"`
	// Memory in bytes. MemUsed = MemTotal - MemAvailable (what the kernel
	// could not hand out without reclaiming).
	MemTotal     uint64 `json:"mem_total"`
	MemUsed      uint64 `json:"mem_used"`
	MemAvailable uint64 `json:"mem_available"`
	SwapTotal    uint64 `json:"swap_total"`
	SwapUsed     uint64 `json:"swap_used"`
	// Conntrack entries: every flow the kernel tracks through the gateway
	// (NAT, DNS hijack, forwarded flows). Max is the table limit the tuner
	// sets; -1 when the module is not loaded.
	ConntrackCount int64 `json:"conntrack_count"`
	ConntrackMax   int64 `json:"conntrack_max"`
	UptimeS        int64 `json:"uptime_s"`
}

// SysMetrics samples the host periodically; Current returns the latest.
type SysMetrics struct {
	Log      *slog.Logger
	Interval time.Duration
	// ProcRoot lets tests point at a fake /proc.
	ProcRoot string

	mu        sync.RWMutex
	last      SysSample
	prevIdle  uint64
	prevTotal uint64
}

// NewSysMetrics returns a sampler reading the real /proc every 5 seconds.
func NewSysMetrics(log *slog.Logger) *SysMetrics {
	return &SysMetrics{Log: log, Interval: 5 * time.Second, ProcRoot: "/proc"}
}

// Current returns the latest sample (Available=false before the first one).
func (m *SysMetrics) Current() SysSample {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.last
}

// Run samples until ctx ends. The first CPU figure needs two readings, so
// it takes one interval to become meaningful.
func (m *SysMetrics) Run(ctx context.Context) error {
	m.Sample()
	tick := time.NewTicker(m.Interval)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-tick.C:
			m.Sample()
		}
	}
}

// Sample takes one reading now.
func (m *SysMetrics) Sample() SysSample {
	s := SysSample{At: time.Now().UTC(), CPUs: runtime.NumCPU(), ConntrackMax: -1, ConntrackCount: -1}
	m.mu.Lock()
	defer m.mu.Unlock()
	if idle, total, ok := m.readCPU(); ok {
		if m.prevTotal > 0 && total > m.prevTotal {
			dIdle, dTotal := float64(idle-m.prevIdle), float64(total-m.prevTotal)
			s.CPUPercent = clamp(100*(1-dIdle/dTotal), 0, 100)
		} else {
			s.CPUPercent = m.last.CPUPercent
		}
		m.prevIdle, m.prevTotal = idle, total
		s.Available = true
	}
	if f := fields(m.read("loadavg")); len(f) >= 3 {
		s.Load1, _ = strconv.ParseFloat(f[0], 64)
		s.Load5, _ = strconv.ParseFloat(f[1], 64)
		s.Load15, _ = strconv.ParseFloat(f[2], 64)
	}
	mem := m.readMeminfo()
	s.MemTotal, s.MemAvailable = mem["MemTotal"], mem["MemAvailable"]
	if s.MemTotal >= s.MemAvailable {
		s.MemUsed = s.MemTotal - s.MemAvailable
	}
	s.SwapTotal = mem["SwapTotal"]
	if free := mem["SwapFree"]; s.SwapTotal >= free {
		s.SwapUsed = s.SwapTotal - free
	}
	if v, err := strconv.ParseInt(strings.TrimSpace(m.read("sys/net/netfilter/nf_conntrack_count")), 10, 64); err == nil {
		s.ConntrackCount = v
		if mx, err := strconv.ParseInt(strings.TrimSpace(m.read("sys/net/netfilter/nf_conntrack_max")), 10, 64); err == nil {
			s.ConntrackMax = mx
		}
	}
	if f := fields(m.read("uptime")); len(f) >= 1 {
		if up, err := strconv.ParseFloat(f[0], 64); err == nil {
			s.UptimeS = int64(up)
		}
	}
	m.last = s
	return s
}

func (m *SysMetrics) read(rel string) string {
	b, err := os.ReadFile(m.ProcRoot + "/" + rel)
	if err != nil {
		return ""
	}
	return string(b)
}

// readCPU returns the idle and total jiffies of the "cpu" summary line.
func (m *SysMetrics) readCPU() (idle, total uint64, ok bool) {
	f, err := os.Open(m.ProcRoot + "/stat")
	if err != nil {
		return 0, 0, false
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		parts := strings.Fields(sc.Text())
		if len(parts) < 5 || parts[0] != "cpu" {
			continue
		}
		for i, p := range parts[1:] {
			v, err := strconv.ParseUint(p, 10, 64)
			if err != nil {
				return 0, 0, false
			}
			total += v
			if i == 3 || i == 4 { // idle, iowait
				idle += v
			}
		}
		return idle, total, true
	}
	return 0, 0, false
}

// readMeminfo returns the fields of /proc/meminfo in bytes.
func (m *SysMetrics) readMeminfo() map[string]uint64 {
	out := map[string]uint64{}
	for _, line := range strings.Split(m.read("meminfo"), "\n") {
		k, rest, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		f := strings.Fields(rest)
		if len(f) == 0 {
			continue
		}
		v, err := strconv.ParseUint(f[0], 10, 64)
		if err != nil {
			continue
		}
		if len(f) > 1 && f[1] == "kB" {
			v *= 1024
		}
		out[k] = v
	}
	return out
}

func fields(s string) []string { return strings.Fields(s) }

func clamp(v, lo, hi float64) float64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
