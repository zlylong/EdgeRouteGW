package main

import (
	"fmt"
	"net"
	"strings"
)

func getPrimaryLANIPAndSubnet() (string, string) {
	serviceIface := ""
	if getDB() != nil {
		_ = getDB().QueryRow("SELECT value FROM settings WHERE key='service_iface'").Scan(&serviceIface)
		serviceIface = strings.TrimSpace(serviceIface)
	}

	ifaces, err := net.Interfaces()
	if err != nil {
		return "", ""
	}

	isPrivateIPv4 := func(ip net.IP) bool {
		return ip.IsPrivate() && ip.To4() != nil
	}

	resolveIface := func(target string) (string, string) {
		for _, iface := range ifaces {
			if target != "" && iface.Name != target {
				continue
			}
			if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
				continue
			}
			addrs, err := iface.Addrs()
			if err != nil {
				continue
			}
			for _, addr := range addrs {
				ipnet, ok := addr.(*net.IPNet)
				if !ok {
					continue
				}
				ip := ipnet.IP.To4()
				if ip == nil || !isPrivateIPv4(ip) {
					continue
				}
				network := ip.Mask(ipnet.Mask)
				maskSize, _ := ipnet.Mask.Size()
				return ip.String(), fmt.Sprintf("%s/%d", network.String(), maskSize)
			}
		}
		return "", ""
	}

	if serviceIface != "" {
		if ip, subnet := resolveIface(serviceIface); ip != "" && subnet != "" {
			return ip, subnet
		}
	}
	return resolveIface("")
}
