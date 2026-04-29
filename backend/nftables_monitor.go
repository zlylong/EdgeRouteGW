package main

import (
	"fmt"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
)

type nftCounterStat struct {
	Name    string `json:"name"`
	Packets int64  `json:"packets"`
	Bytes   int64  `json:"bytes"`
}

var (
	nftCountersMu sync.Mutex
	nftCounters   = map[string]nftCounterStat{}
	nftDeltas     = map[string]nftCounterStat{}
	nftUpdatedAt  time.Time
)

func collectNftPreroutingCounters() (map[string]nftCounterStat, error) {
	cmdRes := sysCmd.runCombinedOutput("nft", "-a", "list", "chain", "inet", "proxygw", "prerouting")
	if cmdRes.Err != nil {
		return nil, fmt.Errorf("nft list prerouting failed: %v, out=%s", cmdRes.Err, strings.TrimSpace(string(cmdRes.Output)))
	}
	out := cmdRes.Output

	counterRe := regexp.MustCompile(`counter packets\s+(\d+)\s+bytes\s+(\d+)`)
	commentRe := regexp.MustCompile(`comment\s+"([^"]+)"`)

	res := map[string]nftCounterStat{}
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || !strings.Contains(line, "counter packets") || !strings.Contains(line, "comment") {
			continue
		}
		counterMatch := counterRe.FindStringSubmatch(line)
		commentMatch := commentRe.FindStringSubmatch(line)
		if len(counterMatch) != 3 || len(commentMatch) != 2 {
			continue
		}
		packets, err1 := strconv.ParseInt(counterMatch[1], 10, 64)
		bytes, err2 := strconv.ParseInt(counterMatch[2], 10, 64)
		if err1 != nil || err2 != nil {
			continue
		}
		name := strings.TrimSpace(commentMatch[1])
		res[name] = nftCounterStat{Name: name, Packets: packets, Bytes: bytes}
	}
	return res, nil
}

func counterDelta(cur, prev map[string]nftCounterStat) map[string]nftCounterStat {
	delta := map[string]nftCounterStat{}
	for name, item := range cur {
		p := item.Packets
		b := item.Bytes
		if old, ok := prev[name]; ok {
			p -= old.Packets
			b -= old.Bytes
			if p < 0 {
				p = 0
			}
			if b < 0 {
				b = 0
			}
		}
		delta[name] = nftCounterStat{Name: name, Packets: p, Bytes: b}
	}
	return delta
}

func summarizeByPrefix(m map[string]nftCounterStat, prefixes ...string) nftCounterStat {
	total := nftCounterStat{Name: "summary"}
	for _, item := range m {
		for _, prefix := range prefixes {
			if strings.HasPrefix(item.Name, prefix) {
				total.Packets += item.Packets
				total.Bytes += item.Bytes
				break
			}
		}
	}
	return total
}

func getNftMonitorSnapshot() (map[string]nftCounterStat, map[string]nftCounterStat, time.Time) {
	nftCountersMu.Lock()
	defer nftCountersMu.Unlock()
	countersCopy := make(map[string]nftCounterStat, len(nftCounters))
	deltasCopy := make(map[string]nftCounterStat, len(nftDeltas))
	for k, v := range nftCounters {
		countersCopy[k] = v
	}
	for k, v := range nftDeltas {
		deltasCopy[k] = v
	}
	return countersCopy, deltasCopy, nftUpdatedAt
}

func startNftablesMonitor() {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	var last map[string]nftCounterStat
	var unavailable bool
	noProxyHitWithTraffic := 0

	for {
		current, err := collectNftPreroutingCounters()
		if err != nil {
			logGatewayEventThrottled("nft_monitor_collect_failed", 10*time.Minute, "warn", "nftables", "counter_collect_failed", "collect nft prerouting counters failed", map[string]interface{}{"reason": err.Error()})
			unavailable = true
			<-ticker.C
			continue
		}
		if unavailable {
			unavailable = false
			logGatewayEvent("info", "nftables", "counter_collect_recovered", "nft prerouting counter collection recovered", nil)
		}

		delta := counterDelta(current, last)
		last = current

		nftCountersMu.Lock()
		nftCounters = current
		nftDeltas = delta
		nftUpdatedAt = time.Now()
		nftCountersMu.Unlock()

		proxyDelta := summarizeByPrefix(delta, "proxy_")
		directDelta := summarizeByPrefix(delta, "acl_", "default_direct")

		trafficMutex.Lock()
		speedSum := currentSpeedUp + currentSpeedDown
		trafficMutex.Unlock()

		if speedSum > 256*1024 && proxyDelta.Packets == 0 {
			noProxyHitWithTraffic++
			if noProxyHitWithTraffic >= 3 {
				logGatewayEventThrottled("nft_tproxy_no_hit_with_traffic", 10*time.Minute, "warn", "nftables", "tproxy_no_hit_anomaly", "traffic exists but TProxy hit counter is zero", map[string]interface{}{
					"traffic_bytes_per_sec": speedSum,
					"proxy_delta_packets":   proxyDelta.Packets,
					"proxy_delta_bytes":     proxyDelta.Bytes,
					"direct_delta_packets":  directDelta.Packets,
					"direct_delta_bytes":    directDelta.Bytes,
				})
			}
		} else {
			noProxyHitWithTraffic = 0
		}

		if proxyDelta.Packets > 500000 || proxyDelta.Bytes > 2*1024*1024*1024 {
			logGatewayEventThrottled("nft_tproxy_hit_spike", 5*time.Minute, "warn", "nftables", "tproxy_hit_spike", "TProxy hit counter spike detected", map[string]interface{}{
				"proxy_delta_packets": proxyDelta.Packets,
				"proxy_delta_bytes":   proxyDelta.Bytes,
			})
		}

		<-ticker.C
	}
}

func registerNftablesRoutes(r *gin.RouterGroup) {
	r.GET("/nftables/stats", func(c *gin.Context) {
		counters, deltas, updatedAt := getNftMonitorSnapshot()
		names := make([]string, 0, len(counters))
		for name := range counters {
			names = append(names, name)
		}
		sort.Strings(names)

		counterList := make([]nftCounterStat, 0, len(names))
		deltaList := make([]nftCounterStat, 0, len(names))
		for _, name := range names {
			counterList = append(counterList, counters[name])
			deltaList = append(deltaList, deltas[name])
		}

		totalProxy := summarizeByPrefix(counters, "proxy_")
		totalDirect := summarizeByPrefix(counters, "acl_", "default_direct")
		deltaProxy := summarizeByPrefix(deltas, "proxy_")
		deltaDirect := summarizeByPrefix(deltas, "acl_", "default_direct")

		c.JSON(http.StatusOK, gin.H{
			"success":    true,
			"updated_at": updatedAt.Format(time.RFC3339),
			"counters":   counterList,
			"deltas":     deltaList,
			"summary": gin.H{
				"proxy_total":  totalProxy,
				"direct_total": totalDirect,
				"proxy_delta":  deltaProxy,
				"direct_delta": deltaDirect,
			},
		})
	})
}
