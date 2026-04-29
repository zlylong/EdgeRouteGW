package main

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
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

var loginAttempts sync.Map

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
	err := db.QueryRow("SELECT value FROM settings WHERE key='password_hash'").Scan(&hash)
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
	err = db.QueryRow("SELECT value FROM settings WHERE key='password'").Scan(&legacyPwd)
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
	tx, err := db.Begin()
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

func registerAuthRoutes(public *gin.RouterGroup, authed *gin.RouterGroup) {
	ctl := NewAuthController()
	public.POST("/login", ctl.Login)
	authed.POST("/password", ctl.ChangePassword)
	authed.POST("/logout", ctl.Logout)

}
