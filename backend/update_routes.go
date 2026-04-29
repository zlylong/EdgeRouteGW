package main

import (
	"fmt"
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
	ctl := NewUpdateController()
	api.GET("/xray/versions", ctl.GetXrayVersions)
	api.GET("/mosdns/versions", ctl.GetMosdnsVersions)
	api.POST("/update/:component", ctl.UpdateComponent)
}
