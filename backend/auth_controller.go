package main

import (
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"log"
)

type AuthController struct{}

func NewAuthController() *AuthController { return &AuthController{} }

// loginAttemptWindow is how long a failed-attempt counter survives without
// further attempts from the same address.
const loginAttemptWindow = 30 * time.Minute

// pruneLoginAttemptsLocked drops counters that have aged out of the window.
// Entries were previously removed only on a successful login, so every distinct
// source address left one behind permanently. Callers must hold
// loginAttemptsMu.
func pruneLoginAttemptsLocked(now time.Time) {
	for addr, data := range loginAttempts {
		if now.Sub(data.LastSeen) > loginAttemptWindow {
			delete(loginAttempts, addr)
		}
	}
}

// loginSlowdownDelay is the pause applied to attempts 7 through 10 from one
// peer. It is a variable so tests can zero it.
var loginSlowdownDelay = 2 * time.Second

func (ctl *AuthController) Login(c *gin.Context) {
	// Deliberately RemoteIP(), not ClientIP(): the brute-force counter must key
	// on the address the packets actually came from. ClientIP() consults
	// X-Forwarded-For, so a caller could hand itself an unused bucket on every
	// request and never reach the delay or the lockout below. SetTrustedProxies
	// in BuildRouter already makes the two equivalent today; this keeps the
	// limiter correct even if a proxy is configured later.
	ip := c.RemoteIP()
	now := time.Now()

	loginAttemptsMu.Lock()
	pruneLoginAttemptsLocked(now)
	attemptData, ok := loginAttempts[ip]
	if !ok {
		attemptData = &LoginAttempt{Count: 0, LastSeen: now}
		loginAttempts[ip] = attemptData
	}
	if now.Sub(attemptData.LastSeen) > loginAttemptWindow {
		attemptData.Count = 0
	}

	if attemptData.Count > 10 {
		loginAttemptsMu.Unlock()
		logGatewayEventThrottled("login_locked_out:"+ip, 10*time.Second, "warn", "auth", "login_locked_out", "login locked out after repeated failures", map[string]interface{}{"source_ip": ip})
		c.AbortWithStatusJSON(http.StatusTooManyRequests, gin.H{"error": "too many attempts"})
		return
	}
	// Count the attempt before releasing the lock. The delay used to run
	// between the check and the increment with the lock dropped, so a burst
	// of concurrent requests all saw the same count and none of them was
	// ever locked out.
	attemptData.Count++
	attemptData.LastSeen = now
	slowDown := attemptData.Count > 6
	loginAttemptsMu.Unlock()
	if slowDown && loginSlowdownDelay > 0 {
		time.Sleep(loginSlowdownDelay)
	}

	var req struct{ Password string }
	if c.BindJSON(&req) != nil {
		c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"error": "bad request"})
		return
	}
	if strings.TrimSpace(req.Password) == "" {
		c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"error": "password required"})
		return
	}

	ok, err := verifyAndMaybeMigratePassword(req.Password)
	if err != nil {
		log.Printf("[WARN] verifyAndMaybeMigratePassword error: %v", err)
		c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{"error": "db error"})
		return
	}
	if !ok {
		log.Printf("Login failed for IP %s: incorrect password", ip)
		// /api/login sits outside auditEventMiddleware, so without this the
		// events view never showed failed logins.
		logGatewayEventThrottled("login_failed:"+ip, 10*time.Second, "warn", "auth", "login_failed", "login failed: incorrect password", map[string]interface{}{"source_ip": ip, "path": c.FullPath(), "method": http.MethodPost, "status": http.StatusUnauthorized})
		c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "incorrect password"})
		return
	}
	loginAttemptsMu.Lock()
	delete(loginAttempts, ip)
	loginAttemptsMu.Unlock()
	token, err := createSession()
	if err != nil {
		log.Printf("[WARN] createSession error: %v", err)
		c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{"error": "failed to generate session token"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"token": token})
}

func (ctl *AuthController) ChangePassword(c *gin.Context) {
	var req struct{ Old, New string }
	if c.BindJSON(&req) != nil {
		c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"error": "bad request"})
		return
	}
	if len(strings.TrimSpace(req.New)) < 8 {
		c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"error": "new password too short (min 8)"})
		return
	}

	ok, err := verifyAndMaybeMigratePassword(req.Old)
	if err != nil {
		c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{"error": "db error"})
		return
	}
	if !ok {
		c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "old password mismatch"})
		return
	}

	hash, err := hashPassword(req.New)
	if err != nil {
		c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{"error": "hash error"})
		return
	}
	tx, err := getDB().Begin()
	if err != nil {
		c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{"error": "db error"})
		return
	}
	if _, err = tx.Exec("INSERT OR REPLACE INTO settings (key, value) VALUES ('password_hash', ?)", hash); err != nil {
		_ = tx.Rollback()
		c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{"error": "db error"})
		return
	}
	if _, err = tx.Exec("DELETE FROM settings WHERE key='password'"); err != nil {
		_ = tx.Rollback()
		c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{"error": "db error"})
		return
	}
	if err = tx.Commit(); err != nil {
		c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{"error": "db error"})
		return
	}
	revokeAllSessions()
	c.JSON(http.StatusOK, gin.H{"success": true})
}

func (ctl *AuthController) Logout(c *gin.Context) {
	token := strings.TrimPrefix(c.GetHeader("Authorization"), "Bearer ")
	clearSession(token)
	c.JSON(http.StatusOK, gin.H{"success": true})
}
