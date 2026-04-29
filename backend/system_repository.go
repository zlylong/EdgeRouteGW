package main

import (
	"fmt"
	"strconv"
)

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
