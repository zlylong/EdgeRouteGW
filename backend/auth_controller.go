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

func (ctl *AuthController) Login(c *gin.Context) {
	ip := c.ClientIP()
	now := time.Now()
	val, _ := loginAttempts.LoadOrStore(ip, LoginAttempt{Count: 0, LastSeen: now})
	attemptData := val.(LoginAttempt)
	if now.Sub(attemptData.LastSeen) > 30*time.Minute {
		attemptData.Count = 0
	}

	attempts := attemptData.Count
	if attempts > 10 {
		c.AbortWithStatusJSON(http.StatusTooManyRequests, gin.H{"error": "too many attempts"})
		return
	}
	if attempts > 5 {
		time.Sleep(2 * time.Second)
	}
	attemptData.Count = attempts + 1
	attemptData.LastSeen = now
	loginAttempts.Store(ip, attemptData)

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
		c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{"error": "db error"})
		return
	}
	if !ok {
		log.Printf("Login failed for IP %s: incorrect password", ip)
		c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "incorrect password"})
		return
	}
	loginAttempts.Delete(ip)
	token, err := createSession()
	if err != nil {
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
	tx, err := db.Begin()
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
