package main

import (
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/gin-gonic/gin"
)

type UpdateController struct{}

func NewUpdateController() *UpdateController { return &UpdateController{} }

func (ctl *UpdateController) GetXrayVersions(c *gin.Context) {
	tags, err := fetchReleaseTags("XTLS/Xray-core")
	if err != nil {
		log.Printf("[WARN] list xray releases: %v", err)
		c.JSON(http.StatusBadGateway, gin.H{"error": "Failed to fetch releases"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"versions": tags})
}

func (ctl *UpdateController) GetMosdnsVersions(c *gin.Context) {
	tags, err := fetchReleaseTags("IrineSistiana/mosdns")
	if err != nil {
		log.Printf("[WARN] list mosdns releases: %v", err)
		c.JSON(http.StatusBadGateway, gin.H{"error": "Failed to fetch releases"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"versions": tags})
}

// smokeTestBinary runs "<bin> version" on a freshly extracted binary before
// it replaces the live one, so a corrupt or wrong-architecture download is
// caught before systemctl restart takes the service down.
var smokeTestBinary = func(path string) error {
	if err := os.Chmod(path, 0o755); err != nil {
		return err
	}
	out, err := sysCmd.output(path, "version")
	if err != nil {
		return fmt.Errorf("%v: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

func (ctl *UpdateController) UpdateComponent(c *gin.Context) {
	comp := c.Param("component")
	// Two overlapping updates of the same or different components race on
	// the .bak copies and the service restarts; the cron geodata update and
	// the self-heal path share the same files. Serialise them.
	release, ok := tryAcquireHighRiskMutationLock(c, "component_update")
	if !ok {
		return
	}
	defer release()
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
			tag, err := fetchLatestReleaseTag("IrineSistiana/mosdns")
			if err != nil {
				log.Printf("[WARN] resolve latest mosdns release: %v", err)
				c.JSON(http.StatusBadGateway, gin.H{"success": false, "error": "Failed to fetch latest mosdns version"})
				return
			}
			req.Version = tag
		}
		downloadURL, err := buildMosdnsDownloadURL(req.Version)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"success": false, "error": "invalid version"})
			return
		}
		mosdnsHash, err := getMosdnsHash(req.Version)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"success": false, "error": "failed to fetch hash"})
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

		if err := downloadWithVerification(downloadURL, mosdnsZip, mosdnsHash); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"success": false, "error": fmt.Sprintf("mosdns download failed: %v", err)})
			return
		}
		if err := sysCmd.run("unzip", "-qo", mosdnsZip, "-d", tmpDir); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"success": false, "error": "unzip failed"})
			return
		}
		if err := smokeTestBinary(filepath.Join(tmpDir, "mosdns")); err != nil {
			log.Printf("[WARN] mosdns %s failed smoke test: %v", req.Version, err)
			c.JSON(http.StatusInternalServerError, gin.H{"success": false, "error": "downloaded mosdns binary does not run on this host"})
			return
		}
		if err := sysCmd.run("install", "-m", "755", filepath.Join(tmpDir, "mosdns"), getPath("core", "mosdns", "mosdns")); err != nil {
			_ = sysCmd.run("cp", getPath("core", "mosdns", "mosdns.bak"), getPath("core", "mosdns", "mosdns"))
			c.JSON(http.StatusInternalServerError, gin.H{"success": false, "error": "install failed"})
			return
		}
		invalidateStatusStaticInfo()
		if err := sysCmd.run("systemctl", "restart", "mosdns"); err != nil {
			_ = sysCmd.run("cp", getPath("core", "mosdns", "mosdns.bak"), getPath("core", "mosdns", "mosdns"))
			_ = sysCmd.run("systemctl", "restart", "mosdns")
			c.JSON(http.StatusInternalServerError, gin.H{"success": false, "error": "restart failed, rolled back"})
			return
		}
	case "rollback_mosdns":
		invalidateStatusStaticInfo()
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
		if err := smokeTestBinary(filepath.Join(tmpDir, "xray")); err != nil {
			log.Printf("[WARN] xray %s failed smoke test: %v", req.Version, err)
			c.JSON(http.StatusInternalServerError, gin.H{"success": false, "error": "downloaded xray binary does not run on this host"})
			return
		}
		if err := sysCmd.run("install", "-m", "755", filepath.Join(tmpDir, "xray"), getPath("core", "xray", "xray")); err != nil {
			_ = sysCmd.run("cp", getPath("core", "xray", "xray.bak"), getPath("core", "xray", "xray"))
			c.JSON(http.StatusInternalServerError, gin.H{"success": false, "error": "install failed"})
			return
		}
		invalidateStatusStaticInfo()
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
	// A binary or geodata file was just replaced; drop the cached versions so
	// the next status poll shows the new one instead of waiting out the TTL.
	invalidateStatusStaticInfo()
	c.JSON(http.StatusOK, gin.H{"success": true})
}
