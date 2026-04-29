package main

import (
	"net"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
)

func normalizeProtectedIPValue(raw string) (string, bool) {
	v := strings.TrimSpace(raw)
	if v == "" {
		return "", false
	}
	if strings.Contains(v, "/") {
		ip, ipNet, err := net.ParseCIDR(v)
		if err != nil || ip == nil || ipNet == nil || ip.To4() == nil {
			return "", false
		}
		ones, _ := ipNet.Mask.Size()
		return ip.Mask(ipNet.Mask).String() + "/" + strconv.Itoa(ones), true
	}
	ip := net.ParseIP(v)
	if ip == nil || ip.To4() == nil {
		return "", false
	}
	return ip.To4().String() + "/32", true
}

func registerProtectedIPRoutes(api *gin.RouterGroup) {
	ctl := NewProtectedIPController(NewProtectedIPRepository())
	api.GET("/protected_ips", ctl.List)
	api.POST("/protected_ips", ctl.Create)
	api.DELETE("/protected_ips/:id", ctl.Delete)
}
