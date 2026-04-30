package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/gin-gonic/gin"
)

type UpdateController struct{}

func NewUpdateController() *UpdateController { return &UpdateController{} }

func (ctl *UpdateController) GetXrayVersions(c *gin.Context) {
	resp, err := httpClient.Get("https://api.github.com/repos/XTLS/Xray-core/releases")
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch releases"})
		return
	}
	defer resp.Body.Close()

	var releases []struct {
		TagName string `json:"tag_name"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&releases); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to parse releases"})
		return
	}

	var tags []string
	for _, r := range releases {
		if strings.TrimSpace(r.TagName) != "" {
			tags = append(tags, r.TagName)
		}
	}
	c.JSON(http.StatusOK, gin.H{"versions": tags})
}

func (ctl *UpdateController) GetMosdnsVersions(c *gin.Context) {
	resp, err := httpClient.Get("https://api.github.com/repos/IrineSistiana/mosdns/releases")
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch releases"})
		return
	}
	defer resp.Body.Close()

	var releases []struct {
		TagName string `json:"tag_name"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&releases); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to parse releases"})
		return
	}

	var tags []string
	for _, r := range releases {
		if strings.TrimSpace(r.TagName) != "" {
			tags = append(tags, r.TagName)
		}
	}
	c.JSON(http.StatusOK, gin.H{"versions": tags})
}

func (ctl *UpdateController) UpdateComponent(c *gin.Context) {
	comp := c.Param("component")
	switch comp {
	case "geodata":
		if err := updateGeodata(); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"success": false, "error": err.Error()})
			return
		}

	case "mosdns":
		var req struct {
			Version string `json:"version"`
		}
		if err := decodeStrictJSON(c, &req, true); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"success": false, "error": "invalid request payload"})
			return
		}
		if strings.TrimSpace(req.Version) == "" || req.Version == "latest" {
			resp, err := httpClient.Get("https://api.github.com/repos/IrineSistiana/mosdns/releases/latest")
			if err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"success": false, "error": "Failed to fetch latest mosdns version"})
				return
			}
			defer resp.Body.Close()
			var latestRelease struct {
				TagName string `json:"tag_name"`
			}
			if err := json.NewDecoder(resp.Body).Decode(&latestRelease); err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"success": false, "error": "Failed to parse latest mosdns release"})
				return
			}
			req.Version = latestRelease.TagName
		}
		downloadURL, err := buildMosdnsDownloadURL(req.Version)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"success": false, "error": "invalid version"})
			return
		}

		if err := sysCmd.run("cp", getPath("core", "mosdns", "mosdns"), getPath("core", "mosdns", "mosdns.bak")); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"success": false, "error": "backup failed"})
			return
		}

		tmpDir, err := os.MkdirTemp("", "proxygw-mosdns-*")
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"success": false, "error": "create temp dir failed"})
			return
		}
		defer os.RemoveAll(tmpDir)
		mosdnsZip := filepath.Join(tmpDir, "mosdns.zip")

		if err := downloadWithVerification(downloadURL, mosdnsZip, ""); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"success": false, "error": fmt.Sprintf("mosdns download failed: %v", err)})
			return
		}
		if err := sysCmd.run("unzip", "-qo", mosdnsZip, "-d", tmpDir); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"success": false, "error": "unzip failed"})
			return
		}
		if err := sysCmd.run("install", "-m", "755", filepath.Join(tmpDir, "mosdns"), getPath("core", "mosdns", "mosdns")); err != nil {
			_ = sysCmd.run("cp", getPath("core", "mosdns", "mosdns.bak"), getPath("core", "mosdns", "mosdns"))
			c.JSON(http.StatusInternalServerError, gin.H{"success": false, "error": "install failed"})
			return
		}
		if err := sysCmd.run("systemctl", "restart", "mosdns"); err != nil {
			_ = sysCmd.run("cp", getPath("core", "mosdns", "mosdns.bak"), getPath("core", "mosdns", "mosdns"))
			_ = sysCmd.run("systemctl", "restart", "mosdns")
			c.JSON(http.StatusInternalServerError, gin.H{"success": false, "error": "restart failed, rolled back"})
			return
		}
	case "rollback_mosdns":
		if err := sysCmd.run("cp", getPath("core", "mosdns", "mosdns.bak"), getPath("core", "mosdns", "mosdns")); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"success": false, "error": "rollback copy failed"})
			return
		}
		if err := sysCmd.run("systemctl", "restart", "mosdns"); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"success": false, "error": "rollback restart failed"})
			return
		}
	case "xray":
		var req struct {
			Version string `json:"version"`
		}
		if err := decodeStrictJSON(c, &req, true); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"success": false, "error": "invalid request payload"})
			return
		}
		if strings.TrimSpace(req.Version) == "" {
			req.Version = "latest"
		}
		downloadURL, err := buildXrayDownloadURL(req.Version)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"success": false, "error": "invalid version"})
			return
		}
		hash, err := getXrayHash(req.Version)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"success": false, "error": "failed to fetch hash"})
			return
		}

		if err := sysCmd.run("cp", getPath("core", "xray", "xray"), getPath("core", "xray", "xray.bak")); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"success": false, "error": "backup failed"})
			return
		}

		tmpDir, err := os.MkdirTemp("", "proxygw-xray-*")
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"success": false, "error": "create temp dir failed"})
			return
		}
		defer os.RemoveAll(tmpDir)
		xrayZip := filepath.Join(tmpDir, "xray.zip")

		if err := downloadWithVerification(downloadURL, xrayZip, hash); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"success": false, "error": fmt.Sprintf("xray validation failed: %v", err)})
			return
		}
		if err := sysCmd.run("unzip", "-qo", xrayZip, "-d", tmpDir); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"success": false, "error": "unzip failed"})
			return
		}
		if err := sysCmd.run("install", "-m", "755", filepath.Join(tmpDir, "xray"), getPath("core", "xray", "xray")); err != nil {
			_ = sysCmd.run("cp", getPath("core", "xray", "xray.bak"), getPath("core", "xray", "xray"))
			c.JSON(http.StatusInternalServerError, gin.H{"success": false, "error": "install failed"})
			return
		}
		if err := sysCmd.run("systemctl", "restart", "xray"); err != nil {
			_ = sysCmd.run("cp", getPath("core", "xray", "xray.bak"), getPath("core", "xray", "xray"))
			_ = sysCmd.run("systemctl", "restart", "xray")
			c.JSON(http.StatusInternalServerError, gin.H{"success": false, "error": "restart failed, rolled back"})
			return
		}
	case "rollback_xray":
		if err := sysCmd.run("cp", getPath("core", "xray", "xray.bak"), getPath("core", "xray", "xray")); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"success": false, "error": "rollback copy failed"})
			return
		}
		if err := sysCmd.run("systemctl", "restart", "xray"); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"success": false, "error": "rollback restart failed"})
			return
		}
	default:
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "error": "unsupported component"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true})
}
