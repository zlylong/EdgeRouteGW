package main

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
)

type gatewayEvent struct {
	ID        int64  `json:"id"`
	TS        string `json:"ts"`
	Level     string `json:"level"`
	Module    string `json:"module"`
	EventType string `json:"event_type"`
	Message   string `json:"message"`
	TraceID   string `json:"trace_id,omitempty"`
	SourceIP  string `json:"source_ip,omitempty"`
	Method    string `json:"method,omitempty"`
	Path      string `json:"path,omitempty"`
	Status    int    `json:"status,omitempty"`
	Duration  int64  `json:"duration_ms,omitempty"`
	Details   string `json:"details,omitempty"`
}

var (
	gatewayEventThrottleMu sync.Mutex
	gatewayEventLastAt     = map[string]time.Time{}
)

func ensureGatewayEventTable() {
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS gateway_events (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		ts DATETIME DEFAULT CURRENT_TIMESTAMP,
		level TEXT NOT NULL,
		module TEXT NOT NULL,
		event_type TEXT NOT NULL,
		message TEXT NOT NULL,
		trace_id TEXT DEFAULT '',
		source_ip TEXT DEFAULT '',
		method TEXT DEFAULT '',
		path TEXT DEFAULT '',
		status INTEGER DEFAULT 0,
		duration_ms INTEGER DEFAULT 0,
		details_json TEXT DEFAULT ''
	)`); err != nil {
		log.Printf("[WARN] create gateway_events failed: %v", err)
		return
	}
	if _, err := db.Exec(`DELETE FROM gateway_events WHERE ts < datetime('now', '-30 days')`); err != nil {
		log.Printf("[WARN] prune gateway_events failed: %v", err)
	}
}

func logGatewayEvent(level, module, eventType, message string, fields map[string]interface{}) {
	if db == nil {
		return
	}
	traceID := ""
	sourceIP := ""
	method := ""
	path := ""
	status := 0
	durationMs := int64(0)

	details := map[string]interface{}{}
	for k, v := range fields {
		switch k {
		case "trace_id":
			traceID = fmt.Sprintf("%v", v)
		case "source_ip":
			sourceIP = fmt.Sprintf("%v", v)
		case "method":
			method = fmt.Sprintf("%v", v)
		case "path":
			path = fmt.Sprintf("%v", v)
		case "status":
			switch vv := v.(type) {
			case int:
				status = vv
			case int64:
				status = int(vv)
			case float64:
				status = int(vv)
			case string:
				if n, err := strconv.Atoi(vv); err == nil {
					status = n
				}
			}
		case "duration_ms":
			switch vv := v.(type) {
			case int:
				durationMs = int64(vv)
			case int64:
				durationMs = vv
			case float64:
				durationMs = int64(vv)
			case string:
				if n, err := strconv.ParseInt(vv, 10, 64); err == nil {
					durationMs = n
				}
			}
		default:
			details[k] = v
		}
	}

	detailsJSON := ""
	if len(details) > 0 {
		if buf, err := json.Marshal(details); err == nil {
			detailsJSON = string(buf)
		}
	}

	if _, err := db.Exec(`INSERT INTO gateway_events (level, module, event_type, message, trace_id, source_ip, method, path, status, duration_ms, details_json)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		strings.ToLower(strings.TrimSpace(level)),
		strings.TrimSpace(module),
		strings.TrimSpace(eventType),
		strings.TrimSpace(message),
		traceID,
		sourceIP,
		method,
		path,
		status,
		durationMs,
		detailsJSON,
	); err != nil {
		log.Printf("[WARN] insert gateway_events failed: %v", err)
	}
}

func logGatewayEventThrottled(throttleKey string, minInterval time.Duration, level, module, eventType, message string, fields map[string]interface{}) bool {
	now := time.Now()
	gatewayEventThrottleMu.Lock()
	last, ok := gatewayEventLastAt[throttleKey]
	if ok && now.Sub(last) < minInterval {
		gatewayEventThrottleMu.Unlock()
		return false
	}
	gatewayEventLastAt[throttleKey] = now
	gatewayEventThrottleMu.Unlock()
	logGatewayEvent(level, module, eventType, message, fields)
	return true
}

func newTraceID() string {
	buf := make([]byte, 8)
	if _, err := rand.Read(buf); err != nil {
		return fmt.Sprintf("t-%d", time.Now().UnixNano())
	}
	return "t-" + hex.EncodeToString(buf)
}

func requestTraceMiddleware(c *gin.Context) {
	traceID := strings.TrimSpace(c.GetHeader("X-Trace-ID"))
	if traceID == "" {
		traceID = newTraceID()
	}
	c.Set("trace_id", traceID)
	c.Header("X-Trace-ID", traceID)
	c.Next()
}

func auditEventMiddleware(c *gin.Context) {
	start := time.Now()
	c.Next()
	if c.Request == nil || c.Request.URL == nil {
		return
	}
	method := strings.ToUpper(strings.TrimSpace(c.Request.Method))
	if method != http.MethodPost && method != http.MethodPut && method != http.MethodPatch && method != http.MethodDelete {
		return
	}
	path := c.Request.URL.Path
	if strings.HasPrefix(path, "/api/logs") || strings.HasPrefix(path, "/api/events") {
		return
	}
	traceID, _ := c.Get("trace_id")
	status := c.Writer.Status()
	level := "info"
	if status >= 400 {
		level = "warn"
	}
	logGatewayEvent(level, "api", "config_change", "API config mutation", map[string]interface{}{
		"trace_id":    fmt.Sprintf("%v", traceID),
		"source_ip":   c.ClientIP(),
		"method":      method,
		"path":        path,
		"status":      status,
		"duration_ms": time.Since(start).Milliseconds(),
	})
}

func registerEventRoutes(r *gin.RouterGroup) {
	r.GET("/events", func(c *gin.Context) {
		limit := 200
		if raw := strings.TrimSpace(c.Query("limit")); raw != "" {
			if n, err := strconv.Atoi(raw); err == nil && n > 0 {
				if n > 1000 {
					n = 1000
				}
				limit = n
			}
		}
		module := strings.TrimSpace(c.Query("module"))
		level := strings.ToLower(strings.TrimSpace(c.Query("level")))

		baseSQL := `SELECT id, datetime(ts, 'localtime'), level, module, event_type, message, trace_id, source_ip, method, path, status, duration_ms, details_json
			FROM gateway_events`
		conds := make([]string, 0, 2)
		args := make([]interface{}, 0, 3)
		if module != "" {
			conds = append(conds, "module = ?")
			args = append(args, module)
		}
		if level != "" {
			conds = append(conds, "level = ?")
			args = append(args, level)
		}
		if len(conds) > 0 {
			baseSQL += " WHERE " + strings.Join(conds, " AND ")
		}
		baseSQL += " ORDER BY id DESC LIMIT ?"
		args = append(args, limit)

		rows, err := db.Query(baseSQL, args...)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "query gateway_events failed"})
			return
		}
		defer rows.Close()

		events := make([]gatewayEvent, 0, limit)
		for rows.Next() {
			var ev gatewayEvent
			var status sql.NullInt64
			var duration sql.NullInt64
			if err := rows.Scan(&ev.ID, &ev.TS, &ev.Level, &ev.Module, &ev.EventType, &ev.Message, &ev.TraceID, &ev.SourceIP, &ev.Method, &ev.Path, &status, &duration, &ev.Details); err != nil {
				continue
			}
			if status.Valid {
				ev.Status = int(status.Int64)
			}
			if duration.Valid {
				ev.Duration = duration.Int64
			}
			events = append(events, ev)
		}
		c.JSON(http.StatusOK, gin.H{"success": true, "events": events})
	})
}
