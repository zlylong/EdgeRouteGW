package main

import (
	"fmt"
	"net"
	"os"
	"strings"
)

func parseVarint(data []byte, idx int) (int, int) {
	val := 0
	shift := 0
	for {
		if idx >= len(data) {
			break
		}
		b := data[idx]
		idx++
		val |= (int(b&0x7F) << shift)
		if (b & 0x80) == 0 {
			break
		}
		shift += 7
	}
	return val, idx
}

// extractGeoIPs returns the IPv4 CIDRs of targetTag. It answers from the
// version-cached matcher (which already holds every CIDR) and only falls
// back to a full parse of the file when the matcher cannot be built.
func extractGeoIPs(filename, targetTag string) []string {
	if m := loadGeoIPMatcher(filename); m != nil {
		return m.cidrsForTag(targetTag)
	}
	return extractGeoIPsScan(filename, targetTag)
}

// extractGeoIPsExclude returns the IPv4 CIDRs of every tag except the
// excluded ones, in file order.
func extractGeoIPsExclude(filename string, excludeTags ...string) []string {
	if m := loadGeoIPMatcher(filename); m != nil {
		return m.cidrsExcluding(excludeTags...)
	}
	return extractGeoIPsExcludeScan(filename, excludeTags...)
}

// sliceWithin reports whether data[idx:idx+n] is in bounds. The legacy
// parsers indexed without checking and panicked on a truncated file.
func sliceWithin(data []byte, idx, n int) bool {
	return idx >= 0 && n >= 0 && idx+n <= len(data)
}

func extractGeoIPsScan(filename, targetTag string) []string {
	data, err := os.ReadFile(filename)
	if err != nil {
		return nil
	}
	var res []string
	idx := 0
	targetTag = strings.ToUpper(targetTag)

	for idx < len(data) {
		if data[idx] == 0x0A { // Field 1: entry
			idx++
			msgLen, newIdx := parseVarint(data, idx)
			idx = newIdx
			endIdx := idx + msgLen
			if endIdx > len(data) {
				endIdx = len(data)
			}

			countryCode := ""
			var currentIPs []string

			for idx < endIdx {
				field := data[idx]
				idx++
				if field == 0x0A { // Field 1: country_code
					strLen, newIdx := parseVarint(data, idx)
					idx = newIdx
					if !sliceWithin(data, idx, strLen) {
						return res
					}
					countryCode = string(data[idx : idx+strLen])
					idx += strLen
				} else if field == 0x12 { // Field 2: cidr
					cidrLen, newIdx := parseVarint(data, idx)
					idx = newIdx
					cidrEnd := idx + cidrLen
					if cidrEnd > endIdx {
						cidrEnd = endIdx
					}
					var ipBytes []byte
					prefix := 0
					for idx < cidrEnd {
						f := data[idx]
						idx++
						if f == 0x0A { // Field 1: ip
							ipLen, nIdx := parseVarint(data, idx)
							idx = nIdx
							if !sliceWithin(data, idx, ipLen) {
								return res
							}
							ipBytes = data[idx : idx+ipLen]
							idx += ipLen
						} else if f == 0x10 { // Field 2: prefix
							p, nIdx := parseVarint(data, idx)
							idx = nIdx
							prefix = p
						} else {
							wireType := f & 0x07
							if wireType == 2 {
								l, nIdx := parseVarint(data, idx)
								idx = nIdx + l
							} else if wireType == 0 {
								_, nIdx := parseVarint(data, idx)
								idx = nIdx
							} else {
								break
							}
						}
					}
					if len(ipBytes) > 0 {
						ipStr := net.IP(ipBytes).String()
						if !strings.Contains(ipStr, ":") {
							resStr := fmt.Sprintf("%s/%d", ipStr, prefix)
							currentIPs = append(currentIPs, resStr)
						}
					}
				} else {
					wireType := field & 0x07
					if wireType == 2 {
						l, newIdx := parseVarint(data, idx)
						idx = newIdx + l
					} else if wireType == 0 {
						_, newIdx := parseVarint(data, idx)
						idx = newIdx
					} else {
						break
					}
				}
			}
			if strings.ToUpper(countryCode) == targetTag {
				res = append(res, currentIPs...)
			}
			idx = endIdx
		} else {
			wireType := data[idx] & 0x07
			idx++
			if wireType == 2 {
				l, newIdx := parseVarint(data, idx)
				idx = newIdx + l
			} else if wireType == 0 {
				_, newIdx := parseVarint(data, idx)
				idx = newIdx
			}
		}
	}
	return res
}

func extractGeoIPsExcludeScan(filename string, excludeTags ...string) []string {
	data, err := os.ReadFile(filename)
	if err != nil {
		return nil
	}

	excluded := make(map[string]struct{}, len(excludeTags))
	for _, tag := range excludeTags {
		tag = strings.ToUpper(strings.TrimSpace(strings.TrimPrefix(tag, "!")))
		if tag != "" {
			excluded[tag] = struct{}{}
		}
	}

	var res []string
	idx := 0
	for idx < len(data) {
		if data[idx] == 0x0A { // Field 1: entry
			idx++
			msgLen, newIdx := parseVarint(data, idx)
			idx = newIdx
			endIdx := idx + msgLen
			if endIdx > len(data) {
				endIdx = len(data)
			}

			countryCode := ""
			var currentIPs []string

			for idx < endIdx {
				field := data[idx]
				idx++
				if field == 0x0A { // Field 1: country_code
					strLen, nIdx := parseVarint(data, idx)
					idx = nIdx
					if !sliceWithin(data, idx, strLen) {
						return res
					}
					countryCode = strings.ToUpper(string(data[idx : idx+strLen]))
					idx += strLen
				} else if field == 0x12 { // Field 2: cidr
					cidrLen, nIdx := parseVarint(data, idx)
					idx = nIdx
					cidrEnd := idx + cidrLen
					if cidrEnd > endIdx {
						cidrEnd = endIdx
					}
					var ipBytes []byte
					prefix := 0
					for idx < cidrEnd {
						f := data[idx]
						idx++
						if f == 0x0A { // Field 1: ip
							ipLen, nn := parseVarint(data, idx)
							idx = nn
							if !sliceWithin(data, idx, ipLen) {
								return res
							}
							ipBytes = data[idx : idx+ipLen]
							idx += ipLen
						} else if f == 0x10 { // Field 2: prefix
							p, nn := parseVarint(data, idx)
							idx = nn
							prefix = p
						} else {
							wireType := f & 0x07
							if wireType == 2 {
								l, nn := parseVarint(data, idx)
								idx = nn + l
							} else if wireType == 0 {
								_, nn := parseVarint(data, idx)
								idx = nn
							} else {
								break
							}
						}
					}
					if len(ipBytes) > 0 {
						ipStr := net.IP(ipBytes).String()
						if !strings.Contains(ipStr, ":") {
							currentIPs = append(currentIPs, fmt.Sprintf("%s/%d", ipStr, prefix))
						}
					}
				} else {
					wireType := field & 0x07
					if wireType == 2 {
						l, nn := parseVarint(data, idx)
						idx = nn + l
					} else if wireType == 0 {
						_, nn := parseVarint(data, idx)
						idx = nn
					} else {
						break
					}
				}
			}

			if countryCode != "" {
				if _, skip := excluded[countryCode]; !skip {
					res = append(res, currentIPs...)
				}
			}
			idx = endIdx
		} else {
			wireType := data[idx] & 0x07
			idx++
			if wireType == 2 {
				l, nIdx := parseVarint(data, idx)
				idx = nIdx + l
			} else if wireType == 0 {
				_, nIdx := parseVarint(data, idx)
				idx = nIdx
			}
		}
	}
	return res
}
