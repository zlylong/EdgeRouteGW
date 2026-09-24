package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"

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

	if !geodataTagRe.MatchString(tag) {
		return fmt.Errorf("unexpected geodata release tag %q", tag)
	}
	rulesZip := filepath.Join(tmpDir, "rules.zip")
	downloadURL := githubDownloadBase + "/Loyalsoldier/v2ray-rules-dat/releases/download/" + tag + "/rules.zip"
	err = downloadWithVerification(downloadURL, rulesZip, hashZip)
	if err != nil {
		return fmt.Errorf("geodata validation failed: %v", err)
	}

	if err := sysCmd.run("unzip", "-qo", rulesZip, "direct-list.txt", "geoip.dat", "geosite.dat", "-d", tmpDir); err != nil {
		return fmt.Errorf("extraction failed: %v", err)
	}
	// Xray and mosdns read these files while they run. Copy each next to its
	// destination and rename over it so a reader never sees a half-written
	// file (cp truncated the live file first).
	replacements := [][2]string{
		{filepath.Join(tmpDir, "direct-list.txt"), getPath("core", "mosdns", "geosite_cn.txt")},
		{filepath.Join(tmpDir, "geoip.dat"), getPath("core", "xray", "geoip.dat")},
		{filepath.Join(tmpDir, "geosite.dat"), getPath("core", "xray", "geosite.dat")},
	}
	for _, r := range replacements {
		if err := replaceFileAtomically(r[0], r[1]); err != nil {
			return fmt.Errorf("install %s failed: %v", filepath.Base(r[1]), err)
		}
	}

	// Ensure mosdns has symlinks to xray geodata to save space and maintain consistency
	_ = os.Remove(getPath("core", "mosdns", "geoip.dat"))
	_ = os.Remove(getPath("core", "mosdns", "geosite.dat"))
	_ = os.Symlink("../xray/geoip.dat", getPath("core", "mosdns", "geoip.dat"))
	_ = os.Symlink("../xray/geosite.dat", getPath("core", "mosdns", "geosite.dat"))

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

// replaceFileAtomically copies src to dst+".tmp" in dst's directory and
// renames it into place, so readers of dst see either the old or the new
// content but never a truncated file.
func replaceFileAtomically(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	tmp := dst + ".tmp"
	out, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err := out.Sync(); err != nil {
		_ = out.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err := out.Close(); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, dst)
}

func registerUpdateRoutes(api *gin.RouterGroup) {
	ctl := NewUpdateController()
	api.GET("/xray/versions", ctl.GetXrayVersions)
	api.GET("/mosdns/versions", ctl.GetMosdnsVersions)
	api.POST("/update/:component", ctl.UpdateComponent)
}
