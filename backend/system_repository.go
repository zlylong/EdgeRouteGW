package main

import (
	"fmt"
	"strconv"
)

type NodeTrafficRanking struct {
	NodeID     int
	NodeName   string
	UpBytes    int64
	DownBytes  int64
	TotalBytes int64
}

type SystemRepository struct{}

func NewSystemRepository() *SystemRepository { return &SystemRepository{} }

func (r *SystemRepository) SaveNetworkRoleSettings(managementIface, serviceIface string) error {
	if _, err := db.Exec("INSERT OR REPLACE INTO settings (key, value) VALUES ('management_iface', ?)", managementIface); err != nil {
		return err
	}
	if _, err := db.Exec("INSERT OR REPLACE INTO settings (key, value) VALUES ('service_iface', ?)", serviceIface); err != nil {
		return err
	}
	return nil
}

func (r *SystemRepository) SaveMode(mode string) error {
	_, err := db.Exec("INSERT OR REPLACE INTO settings (key, value) VALUES ('mode', ?)", mode)
	return err
}

func (r *SystemRepository) SaveCronDefaults(cronTime, scheduleType string, weekday, monthday int) {
	_, _ = db.Exec("INSERT OR REPLACE INTO settings (key, value) VALUES ('cron_time', ?)", cronTime)
	_, _ = db.Exec("INSERT OR REPLACE INTO settings (key, value) VALUES ('cron_schedule_type', ?)", scheduleType)
	_, _ = db.Exec("INSERT OR REPLACE INTO settings (key, value) VALUES ('cron_weekday', ?)", strconv.Itoa(weekday))
	_, _ = db.Exec("INSERT OR REPLACE INTO settings (key, value) VALUES ('cron_monthday', ?)", strconv.Itoa(monthday))
}

func (r *SystemRepository) SaveCronSettings(enabled bool, cronTime, scheduleType string, weekday, monthday int) error {
	if _, err := db.Exec("INSERT OR REPLACE INTO settings (key, value) VALUES ('cron_enabled', ?)", fmt.Sprintf("%t", enabled)); err != nil {
		return err
	}
	if _, err := db.Exec("INSERT OR REPLACE INTO settings (key, value) VALUES ('cron_time', ?)", cronTime); err != nil {
		return err
	}
	if _, err := db.Exec("INSERT OR REPLACE INTO settings (key, value) VALUES ('cron_schedule_type', ?)", scheduleType); err != nil {
		return err
	}
	if _, err := db.Exec("INSERT OR REPLACE INTO settings (key, value) VALUES ('cron_weekday', ?)", strconv.Itoa(weekday)); err != nil {
		return err
	}
	if _, err := db.Exec("INSERT OR REPLACE INTO settings (key, value) VALUES ('cron_monthday', ?)", strconv.Itoa(monthday)); err != nil {
		return err
	}
	return nil
}

func (r *SystemRepository) SaveOspfSettings(batchLimit, intervalSeconds, resolveWorkers int, allowlist string) error {
	if _, err := db.Exec("INSERT OR REPLACE INTO settings (key, value) VALUES ('ospf_push_batch_limit', ?)", strconv.Itoa(batchLimit)); err != nil {
		return err
	}
	if _, err := db.Exec("INSERT OR REPLACE INTO settings (key, value) VALUES ('ospf_push_interval_seconds', ?)", strconv.Itoa(intervalSeconds)); err != nil {
		return err
	}
	if _, err := db.Exec("INSERT OR REPLACE INTO settings (key, value) VALUES ('ospf_resolve_workers', ?)", strconv.Itoa(resolveWorkers)); err != nil {
		return err
	}
	if _, err := db.Exec("INSERT OR REPLACE INTO settings (key, value) VALUES ('ospf_publish_allowlist', ?)", allowlist); err != nil {
		return err
	}
	return nil
}

func (r *SystemRepository) GetMode() (string, error) {
	var mode string
	err := db.QueryRow("SELECT value FROM settings WHERE key='mode'").Scan(&mode)
	return mode, err
}

func (r *SystemRepository) GetMonthlyTrafficTotal() (int64, int64, error) {
	var totalMonthUp, totalMonthDown int64
	err := db.QueryRow(`
		SELECT COALESCE(SUM(up_bytes), 0), COALESCE(SUM(down_bytes), 0)
		FROM traffic_history
		WHERE datetime(ts, 'localtime') >= datetime('now', 'localtime', 'start of month')
	`).Scan(&totalMonthUp, &totalMonthDown)
	return totalMonthUp, totalMonthDown, err
}

func (r *SystemRepository) GetMonthlyNodeTrafficRanking(limit int) ([]NodeTrafficRanking, error) {
	rows, err := db.Query(`
		SELECT n.id,
		       n.name,
		       COALESCE(SUM(h.up_bytes), 0)   AS up_bytes,
		       COALESCE(SUM(h.down_bytes), 0) AS down_bytes,
		       COALESCE(SUM(h.up_bytes), 0) + COALESCE(SUM(h.down_bytes), 0) AS total_bytes
		FROM nodes n
		LEFT JOIN node_traffic_history h
		       ON h.node_id = n.id
		      AND datetime(h.ts, 'localtime') >= datetime('now', 'localtime', 'start of month')
		GROUP BY n.id, n.name
		HAVING total_bytes > 0
		ORDER BY total_bytes DESC
		LIMIT ?
	`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	ranking := make([]NodeTrafficRanking, 0)
	for rows.Next() {
		var item NodeTrafficRanking
		if scanErr := rows.Scan(&item.NodeID, &item.NodeName, &item.UpBytes, &item.DownBytes, &item.TotalBytes); scanErr != nil {
			continue
		}
		ranking = append(ranking, item)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return ranking, nil
}

func (r *SystemRepository) GetOspfRouteCounts() (published int, candidate int, err error) {
	if err = db.QueryRow("SELECT count(*) FROM routes_table WHERE status='published'").Scan(&published); err != nil {
		return 0, 0, err
	}
	if err = db.QueryRow("SELECT count(*) FROM routes_table WHERE status='candidate'").Scan(&candidate); err != nil {
		return 0, 0, err
	}
	return published, candidate, nil
}

func (r *SystemRepository) GetOspfPublishAllowlist() (string, error) {
	var allowlist string
	err := db.QueryRow("SELECT value FROM settings WHERE key='ospf_publish_allowlist'").Scan(&allowlist)
	return allowlist, err
}

func (r *SystemRepository) ResetOspfPendingStaticRoutes() (int64, error) {
	res, err := db.Exec("DELETE FROM routes_table WHERE status='candidate' AND source='static'")
	if err != nil {
		return 0, err
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return 0, err
	}
	return affected, nil
}
