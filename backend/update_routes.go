package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/gin-gonic/gin"
)

func updateGeodata() error {
	tag, hashZip, err := getGeoDataVersionAndHash()
	if err != nil {
		return fmt.Errorf("failed to fetch geodata hash: %v", err)
	}

	tmpDir, err := os.MkdirTemp("", "proxygw-geodata-*")
	if err != nil {
		return fmt.Errorf("create temp dir failed: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	rulesZip := filepath.Join(tmpDir, "rules.zip")
	downloadURL := "https://github.com/Loyalsoldier/v2ray-rules-dat/releases/download/" + tag + "/rules.zip"
	err = downloadWithVerification(downloadURL, rulesZip, hashZip)
	if err != nil {
		return fmt.Errorf("geodata validation failed: %v", err)
	}

	cmds := [][]string{
		{"unzip", "-qo", rulesZip, "direct-list.txt", "geoip.dat", "geosite.dat", "-d", tmpDir},
		{"cp", filepath.Join(tmpDir, "direct-list.txt"), getPath("core", "mosdns", "geosite_cn.txt")},
		{"cp", filepath.Join(tmpDir, "geoip.dat"), getPath("core", "mosdns", "geoip.dat")},
		{"cp", filepath.Join(tmpDir, "geosite.dat"), getPath("core", "mosdns", "geosite.dat")},
	}
	for _, c := range cmds {
		if err := sysCmd.run(c[0], c[1:]...); err != nil {
			return fmt.Errorf("extraction/copy failed: %v", err)
		}
	}
	if err := os.WriteFile(getPath("core", "mosdns", "geodata.ver"), []byte(tag), 0644); err != nil {
		return fmt.Errorf("write geodata version failed: %v", err)
	}
	if err := sysCmd.run("systemctl", "restart", "mosdns", "xray"); err != nil {
		return fmt.Errorf("service restart failed: %v", err)
	}
	cacheMutex.Lock()
	cachedGeosite = nil
	cachedGeoip = nil
	cacheMutex.Unlock()
	return nil
}

func registerUpdateRoutes(api *gin.RouterGroup) {
	api.GET("/xray/versions", func(c *gin.Context) {
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
	})

	api.GET("/mosdns/versions", func(c *gin.Context) {
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
	})

	api.POST("/update/:component", func(c *gin.Context) {
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
			if err := c.ShouldBindJSON(&req); err != nil {
				if !errors.Is(err, io.EOF) {
					c.JSON(http.StatusBadRequest, gin.H{"success": false, "error": "invalid request payload"})
					return
				}
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
			if err := c.ShouldBindJSON(&req); err != nil {
				if !errors.Is(err, io.EOF) {
					c.JSON(http.StatusBadRequest, gin.H{"success": false, "error": "invalid request payload"})
					return
				}
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
	})
}
