package main

import (
	"database/sql"
	"strconv"
)

type DNSRepository struct{}

func NewDNSRepository() *DNSRepository { return &DNSRepository{} }

func (r *DNSRepository) GetSetting(key string) (string, error) {
	var value string
	err := db.QueryRow("SELECT value FROM settings WHERE key=?", key).Scan(&value)
	return value, err
}

func (r *DNSRepository) InsertIgnoreSetting(key, value string) error {
	_, err := db.Exec("INSERT OR IGNORE INTO settings (key, value) VALUES (?, ?)", key, value)
	return err
}

func (r *DNSRepository) UpdateSetting(key, value string) error {
	_, err := db.Exec("UPDATE settings SET value=? WHERE key=?", value, key)
	return err
}

func (r *DNSRepository) UpsertSetting(key, value string) error {
	_, err := db.Exec("INSERT OR REPLACE INTO settings (key, value) VALUES (?, ?)", key, value)
	return err
}

func (r *DNSRepository) GetDNSSettingsWithDefaults() (local, remote, lazy, mode string, logLevel string, cacheSize int, lazyTTL int, err error) {
	local, err = r.GetSetting("dns_local")
	if err == sql.ErrNoRows {
		local = "119.29.29.29,223.5.5.5"
		_ = r.InsertIgnoreSetting("dns_local", local)
	} else if err != nil {
		return "", "", "", "", "", 0, 0, err
	}

	remote, err = r.GetSetting("dns_remote")
	if err == sql.ErrNoRows {
		remote = "1.1.1.1,8.8.8.8"
		_ = r.InsertIgnoreSetting("dns_remote", remote)
	} else if err != nil {
		return "", "", "", "", "", 0, 0, err
	}

	lazy, err = r.GetSetting("dns_lazy")
	if err == sql.ErrNoRows {
		lazy = "true"
		_ = r.InsertIgnoreSetting("dns_lazy", lazy)
	} else if err != nil {
		return "", "", "", "", "", 0, 0, err
	}

	mode, err = r.GetSetting("dns_mode")
	if err == sql.ErrNoRows {
		mode = "smart"
		_ = r.InsertIgnoreSetting("dns_mode", mode)
		err = nil
	} else if err != nil {
		return "", "", "", "", "", 0, 0, err
	}

	logLevel, err = r.GetSetting("dns_log_level")
	if err == sql.ErrNoRows {
		logLevel = "info"
		_ = r.InsertIgnoreSetting("dns_log_level", logLevel)
	}

	csStr, err := r.GetSetting("dns_cache_size")
	if err == sql.ErrNoRows {
		cacheSize = 10240
		_ = r.InsertIgnoreSetting("dns_cache_size", "10240")
	} else {
		cacheSize, _ = strconv.Atoi(csStr)
	}

	ltStr, err := r.GetSetting("dns_lazy_ttl")
	if err == sql.ErrNoRows {
		lazyTTL = 86400
		_ = r.InsertIgnoreSetting("dns_lazy_ttl", "86400")
	} else {
		lazyTTL, _ = strconv.Atoi(ltStr)
	}

	return local, remote, lazy, mode, logLevel, cacheSize, lazyTTL, nil
}
