package main

import (
	"encoding/json"
	"log"
	"strconv"
	"strings"
	"sync"
	"time"
)

type XrayStat struct {
	Stat []struct {
		Name  string `json:"name"`
		Value int64  `json:"value"`
	} `json:"stat"`
}

type trafficBytes struct {
	up   int64
	down int64
}

var (
	trafficMutex         sync.Mutex
	currentSpeedUp       int64
	currentSpeedDown     int64
	lastTotalUp          int64
	lastTotalDown        int64
	xrayStatsUnavailable bool
)

func initTrafficDB() {
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS traffic_history (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		ts DATETIME DEFAULT CURRENT_TIMESTAMP,
		up_bytes INTEGER,
		down_bytes INTEGER
	)`); err != nil {
		log.Printf("[WARN] Failed to create traffic_history table: %v", err)
	}
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS node_traffic_history (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		ts DATETIME DEFAULT CURRENT_TIMESTAMP,
		node_id INTEGER NOT NULL,
		up_bytes INTEGER,
		down_bytes INTEGER
	)`); err != nil {
		log.Printf("[WARN] Failed to create node_traffic_history table: %v", err)
	}
	if _, err := db.Exec(`DELETE FROM traffic_history WHERE ts < datetime('now', '-180 days')`); err != nil {
		log.Printf("[WARN] prune traffic_history failed: %v", err)
	}
	if _, err := db.Exec(`DELETE FROM node_traffic_history WHERE ts < datetime('now', '-180 days')`); err != nil {
		log.Printf("[WARN] prune node_traffic_history failed: %v", err)
	}
}

func startTrafficMonitor() {
	initTrafficDB()

	ticker := time.NewTicker(2 * time.Second)
	saveTicker := time.NewTicker(5 * time.Minute)

	var accumUp, accumDown int64
	lastNodeTotals := map[int]trafficBytes{}
	accumNodeTotals := map[int]trafficBytes{}

	for {
		select {
		case <-ticker.C:
			out, err := sysCmd.output(getPath("core", "xray", "xray"), "api", "statsquery", "-server=127.0.0.1:10085", "-pattern=")
			if err != nil {
				trafficMutex.Lock()
				currentSpeedUp = 0
				currentSpeedDown = 0
				lastTotalUp = 0
				lastTotalDown = 0
				lastNodeTotals = map[int]trafficBytes{}
				if !xrayStatsUnavailable {
					xrayStatsUnavailable = true
					logGatewayEvent("warn", "xray", "stats_unavailable", "Xray statsquery failed", map[string]interface{}{"reason": err.Error()})
				}
				trafficMutex.Unlock()
				continue
			}

			trafficMutex.Lock()
			if xrayStatsUnavailable {
				xrayStatsUnavailable = false
				logGatewayEvent("info", "xray", "stats_recovered", "Xray statsquery recovered", nil)
			}
			trafficMutex.Unlock()

			var stats XrayStat
			if err := json.Unmarshal(out, &stats); err != nil {
				logGatewayEvent("warn", "xray", "stats_decode_failed", "decode Xray stats failed", map[string]interface{}{"reason": err.Error()})
				continue
			}

			var totalUp, totalDown int64
			nodeTotals := map[int]trafficBytes{}
			for _, s := range stats.Stat {
				if strings.Contains(s.Name, ">>>uplink") && !strings.Contains(s.Name, "api_inbound") {
					totalUp += s.Value
				}
				if strings.Contains(s.Name, ">>>downlink") && !strings.Contains(s.Name, "api_inbound") {
					totalDown += s.Value
				}
				if nodeID, direction, ok := parseNodeTrafficStat(s.Name); ok {
					item := nodeTotals[nodeID]
					if direction == "uplink" {
						item.up += s.Value
					} else if direction == "downlink" {
						item.down += s.Value
					}
					nodeTotals[nodeID] = item
				}
			}

			trafficMutex.Lock()
			deltaUp := totalUp - lastTotalUp
			deltaDown := totalDown - lastTotalDown
			if lastTotalUp == 0 || deltaUp < 0 {
				deltaUp = 0
			}
			if lastTotalDown == 0 || deltaDown < 0 {
				deltaDown = 0
			}
			currentSpeedUp = deltaUp / 2
			currentSpeedDown = deltaDown / 2
			lastTotalUp = totalUp
			lastTotalDown = totalDown
			accumUp += deltaUp
			accumDown += deltaDown

			for nodeID, current := range nodeTotals {
				prev, ok := lastNodeTotals[nodeID]
				deltaNodeUp := current.up - prev.up
				deltaNodeDown := current.down - prev.down
				if !ok || deltaNodeUp < 0 {
					deltaNodeUp = 0
				}
				if !ok || deltaNodeDown < 0 {
					deltaNodeDown = 0
				}
				acc := accumNodeTotals[nodeID]
				acc.up += deltaNodeUp
				acc.down += deltaNodeDown
				accumNodeTotals[nodeID] = acc
			}
			lastNodeTotals = nodeTotals
			trafficMutex.Unlock()

		case <-saveTicker.C:
			trafficMutex.Lock()
			saveUp := accumUp
			saveDown := accumDown
			accumUp = 0
			accumDown = 0
			saveNodeTotals := accumNodeTotals
			accumNodeTotals = map[int]trafficBytes{}
			trafficMutex.Unlock()

			if saveUp > 0 || saveDown > 0 {
				if _, err := db.Exec(`INSERT INTO traffic_history (up_bytes, down_bytes) VALUES (?, ?)`, saveUp, saveDown); err != nil {
					log.Printf("[WARN] insert traffic_history failed: %v", err)
				}
			}
			for nodeID, stat := range saveNodeTotals {
				if stat.up <= 0 && stat.down <= 0 {
					continue
				}
				if _, err := db.Exec(`INSERT INTO node_traffic_history (node_id, up_bytes, down_bytes) VALUES (?, ?, ?)`, nodeID, stat.up, stat.down); err != nil {
					log.Printf("[WARN] insert node_traffic_history failed node=%d err=%v", nodeID, err)
				}
			}
			if _, err := db.Exec(`DELETE FROM traffic_history WHERE ts < datetime('now', '-180 days')`); err != nil {
				log.Printf("[WARN] prune traffic_history failed: %v", err)
			}
			if _, err := db.Exec(`DELETE FROM node_traffic_history WHERE ts < datetime('now', '-180 days')`); err != nil {
				log.Printf("[WARN] prune node_traffic_history failed: %v", err)
			}
		}
	}
}

func parseNodeTrafficStat(name string) (nodeID int, direction string, ok bool) {
	const prefix = "outbound>>>proxy-"
	const marker = "-out>>>traffic>>>"
	if !strings.HasPrefix(name, prefix) {
		return 0, "", false
	}
	rest := strings.TrimPrefix(name, prefix)
	parts := strings.SplitN(rest, marker, 2)
	if len(parts) != 2 {
		return 0, "", false
	}
	id, err := strconv.Atoi(parts[0])
	if err != nil || id <= 0 {
		return 0, "", false
	}
	dir := strings.TrimSpace(parts[1])
	if dir != "uplink" && dir != "downlink" {
		return 0, "", false
	}
	return id, dir, true
}
