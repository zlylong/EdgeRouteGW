// Package netinfo answers small questions about the host's network stack.
package netinfo

import (
	"bufio"
	"fmt"
	"os"
	"strings"
)

// DefaultIface returns the interface carrying the IPv4 default route in the
// main table, read from /proc/net/route so it needs no capabilities.
func DefaultIface() (string, error) {
	return defaultIfaceFrom("/proc/net/route")
}

func defaultIfaceFrom(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		// Iface Destination Gateway Flags ...
		if len(fields) < 4 || fields[1] != "00000000" {
			continue
		}
		return fields[0], nil
	}
	return "", fmt.Errorf("no IPv4 default route")
}
