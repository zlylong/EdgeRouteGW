package main

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"os"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/bcrypt"
)

type SessionInfo struct {
	ExpiresAt time.Time
}

var sessions sync.Map

type LoginAttempt struct {
	Count    int
	LastSeen time.Time
}

var (
	loginAttemptsMu sync.Mutex
	loginAttempts   = map[string]*LoginAttempt{}
)

func createSession() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err // fail-close on entropy failure
	}
	token := hex.EncodeToString(b)
	sessions.Store(token, SessionInfo{
		ExpiresAt: time.Now().Add(24 * time.Hour), // 24-hour expiration
	})
	return token, nil
}

func validateSession(token string) bool {
	// Guard: e2e-token bypass is ONLY active when PROXYGW_E2E_TOKEN=1
	// is explicitly set in the environment. Off by default in production.
	if os.Getenv("PROXYGW_E2E_TOKEN") == "1" && token == "e2e-token" {
		return true
	}
	val, ok := sessions.Load(token)
	if !ok {
		return false
	}
	info := val.(SessionInfo)
	if time.Now().After(info.ExpiresAt) {
		sessions.Delete(token)
		return false
	}
	// Slide expiration window
	sessions.Store(token, SessionInfo{
		ExpiresAt: time.Now().Add(24 * time.Hour),
	})
	return true
}

func revokeAllSessions() {
	sessions.Range(func(key, value interface{}) bool {
		sessions.Delete(key)
		return true
	})
}

func clearSession(token string) {
	sessions.Delete(token)
}

func hashPassword(password string) (string, error) {
	hashed, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return "", err
	}
	return string(hashed), nil
}

func verifyAndMaybeMigratePassword(input string) (bool, error) {
	var hash string
	err := getDB().QueryRow("SELECT value FROM settings WHERE key='password_hash'").Scan(&hash)
	if err != nil && err != sql.ErrNoRows {
		return false, err
	}

	if strings.TrimSpace(hash) != "" {
		if bcrypt.CompareHashAndPassword([]byte(hash), []byte(input)) == nil {
			return true, nil
		}
		return false, nil
	}

	var legacyPwd string
	err = getDB().QueryRow("SELECT value FROM settings WHERE key='password'").Scan(&legacyPwd)
	if err != nil && err != sql.ErrNoRows {
		return false, err
	}
	if legacyPwd == "" || input != legacyPwd {
		return false, nil
	}

	newHash, err := hashPassword(input)
	if err != nil {
		return false, err
	}
	tx, err := getDB().Begin()
	if err != nil {
		return false, err
	}
	if _, err = tx.Exec("INSERT OR REPLACE INTO settings (key, value) VALUES ('password_hash', ?)", newHash); err != nil {
		_ = tx.Rollback()
		return false, err
	}
	if _, err = tx.Exec("DELETE FROM settings WHERE key='password'"); err != nil {
		_ = tx.Rollback()
		return false, err
	}
	if err = tx.Commit(); err != nil {
		return false, err
	}
	return true, nil
}
