package main

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"
)

func buildXrayDownloadURL(version string) (string, error) {
	base := "https://github.com/XTLS/Xray-core/releases/latest/download/Xray-linux-64.zip"
	ver := strings.TrimSpace(version)
	if ver == "" || ver == "latest" {
		return base, nil
	}
	matched, _ := regexp.MatchString(`^v[0-9A-Za-z._-]+$`, ver)
	if !matched {
		return "", fmt.Errorf("invalid version")
	}
	return fmt.Sprintf("https://github.com/XTLS/Xray-core/releases/download/%s/Xray-linux-64.zip", ver), nil
}

func parseXrayVersionOutput(output string) string {
	lines := strings.Split(output, "\n")
	if len(lines) == 0 {
		return "Unknown"
	}
	parts := strings.Fields(lines[0])
	if len(parts) >= 2 {
		return parts[1]
	}
	return "Unknown"
}

func parsePortValue(v interface{}) int {
	portInt := 443
	switch p := v.(type) {
	case float64:
		portInt = int(p)
	case string:
		if parsed, err := strconv.Atoi(p); err == nil {
			portInt = parsed
		}
	}
	return portInt
}

func isValidIPOrCIDR(v string) bool {
	s := strings.TrimSpace(v)
	if s == "" {
		return false
	}
	if strings.Contains(s, "/") {
		_, _, err := net.ParseCIDR(s)
		return err == nil
	}
	return net.ParseIP(s) != nil
}

func sanitizeUpstreamItem(addr string) (string, bool) {
	a := strings.TrimSpace(addr)
	if a == "" {
		return "", false
	}
	if strings.ContainsAny(a, "\"\n\r") {
		return "", false
	}
	return a, true
}

func normalizeUpstreamCSV(raw string) (string, bool) {
	parts := strings.Split(raw, ",")
	var cleaned []string
	for _, p := range parts {
		item, ok := sanitizeUpstreamItem(p)
		if !ok {
			continue
		}
		cleaned = append(cleaned, item)
	}
	if len(cleaned) == 0 {
		return "", false
	}
	return strings.Join(cleaned, ","), true
}

func getRemoteFileContent(urlStr string) (string, error) {
	resp, err := httpClient.Get(urlStr)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return "", fmt.Errorf("status %d", resp.StatusCode)
	}
	b, err := io.ReadAll(resp.Body)
	return string(b), err
}

func verifySHA256(filePath, expectedHash string) error {
	f, err := os.Open(filePath)
	if err != nil {
		return err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return err
	}
	actual := hex.EncodeToString(h.Sum(nil))
	if !strings.EqualFold(actual, expectedHash) {
		return fmt.Errorf("hash mismatch: expected %s, got %s", expectedHash, actual)
	}
	return nil
}

func getGeoDataVersionAndHash() (string, string, error) {
	resp, err := httpClient.Get("https://api.github.com/repos/Loyalsoldier/v2ray-rules-dat/releases/latest")
	if err != nil {
		return "", "", err
	}
	defer resp.Body.Close()
	var release struct {
		TagName string `json:"tag_name"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&release); err != nil {
		return "", "", err
	}
	tag := release.TagName

	urlStr := "https://github.com/Loyalsoldier/v2ray-rules-dat/releases/download/" + tag + "/rules.zip.sha256sum"
	content, err := getRemoteFileContent(urlStr)
	if err != nil {
		return tag, "", err
	}
	parts := strings.Fields(content)
	if len(parts) > 0 {
		return tag, parts[0], nil
	}
	return tag, "", fmt.Errorf("invalid hash file")
}

func getXrayHash(version string) (string, error) {
	urlStr := ""
	if version == "" || version == "latest" {
		urlStr = "https://github.com/XTLS/Xray-core/releases/latest/download/Xray-linux-64.zip.dgst"
	} else {
		urlStr = fmt.Sprintf("https://github.com/XTLS/Xray-core/releases/download/%s/Xray-linux-64.zip.dgst", version)
	}
	content, err := getRemoteFileContent(urlStr)
	if err != nil {
		return "", err
	}
	lines := strings.Split(content, "\n")
	for _, line := range lines {
		if strings.HasPrefix(line, "SHA256(") || strings.HasPrefix(line, "SHA2-256=") {
			parts := strings.Split(line, "= ")
			if len(parts) == 2 {
				return strings.TrimSpace(parts[1]), nil
			}
		}
	}
	return "", fmt.Errorf("hash not found in dgst")
}

func downloadWithVerification(urlStr, dest, expectedHash string) error {
	resp, err := downloadClient.Get(urlStr)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("download failed: %d", resp.StatusCode)
	}

	tmpPath := dest + ".tmp"
	f, err := os.Create(tmpPath)
	if err != nil {
		return err
	}
	_, err = io.Copy(f, resp.Body)
	f.Close()
	if err != nil {
		os.Remove(tmpPath)
		return err
	}

	if expectedHash != "" {
		if err := verifySHA256(tmpPath, expectedHash); err != nil {
			os.Remove(tmpPath)
			return err
		}
	}

	return os.Rename(tmpPath, dest)
}

var downloadClient = &http.Client{Timeout: 5 * time.Minute}
var httpClient = &http.Client{
	Timeout: 15 * time.Second,
}

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

func extractGeoIPs(filename, targetTag string) []string {
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

			countryCode := ""
			var currentIPs []string

			for idx < endIdx {
				field := data[idx]
				idx++
				if field == 0x0A { // Field 1: country_code
					strLen, newIdx := parseVarint(data, idx)
					idx = newIdx
					countryCode = string(data[idx : idx+strLen])
					idx += strLen
				} else if field == 0x12 { // Field 2: cidr
					cidrLen, newIdx := parseVarint(data, idx)
					idx = newIdx
					cidrEnd := idx + cidrLen
					var ipBytes []byte
					prefix := 0
					for idx < cidrEnd {
						f := data[idx]
						idx++
						if f == 0x0A { // Field 1: ip
							ipLen, nIdx := parseVarint(data, idx)
							idx = nIdx
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

func extractGeoIPsExclude(filename string, excludeTags ...string) []string {
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

			countryCode := ""
			var currentIPs []string

			for idx < endIdx {
				field := data[idx]
				idx++
				if field == 0x0A { // Field 1: country_code
					strLen, nIdx := parseVarint(data, idx)
					idx = nIdx
					countryCode = strings.ToUpper(string(data[idx : idx+strLen]))
					idx += strLen
				} else if field == 0x12 { // Field 2: cidr
					cidrLen, nIdx := parseVarint(data, idx)
					idx = nIdx
					cidrEnd := idx + cidrLen
					var ipBytes []byte
					prefix := 0
					for idx < cidrEnd {
						f := data[idx]
						idx++
						if f == 0x0A { // Field 1: ip
							ipLen, nn := parseVarint(data, idx)
							idx = nn
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

var aesKey []byte

func init() {
	keyPath := getPath("config", "aes.key")
	data, err := os.ReadFile(keyPath)
	if err == nil && len(data) == 32 {
		aesKey = data
		return
	}

	oldKey := []byte("proxygw-secret-key-32-bytes-long")
	dbPath := getPath("config", "proxygw.db")
	if _, err := os.Stat(dbPath); err == nil {
		aesKey = oldKey
	} else {
		aesKey = make([]byte, 32)
		if _, err := io.ReadFull(rand.Reader, aesKey); err != nil {
			aesKey = oldKey
		}
	}
	os.WriteFile(keyPath, aesKey, 0600)
}

func EncryptAES(text string) string {
	block, err := aes.NewCipher(aesKey)
	if err != nil {
		return text
	}
	b := base64.StdEncoding.EncodeToString([]byte(text))
	ciphertext := make([]byte, aes.BlockSize+len(b))
	iv := ciphertext[:aes.BlockSize]
	if _, err := io.ReadFull(rand.Reader, iv); err != nil {
		return text
	}
	cfb := cipher.NewCFBEncrypter(block, iv)
	cfb.XORKeyStream(ciphertext[aes.BlockSize:], []byte(b))
	return "ENC:" + base64.StdEncoding.EncodeToString(ciphertext)
}

func DecryptAES(text string) string {
	if len(text) < 4 || text[:4] != "ENC:" {
		return text
	}
	text = text[4:]
	ciphertext, err := base64.StdEncoding.DecodeString(text)
	if err != nil || len(ciphertext) < aes.BlockSize {
		return text
	}
	block, err := aes.NewCipher(aesKey)
	if err != nil {
		return text
	}
	iv := ciphertext[:aes.BlockSize]
	ciphertext = ciphertext[aes.BlockSize:]
	cfb := cipher.NewCFBDecrypter(block, iv)
	cfb.XORKeyStream(ciphertext, ciphertext)
	data, err := base64.StdEncoding.DecodeString(string(ciphertext))
	if err != nil {
		return text
	}
	return string(data)
}

func buildMosdnsDownloadURL(version string) (string, error) {
	arch := runtime.GOARCH
	if arch != "arm64" && arch != "amd64" {
		return "", fmt.Errorf("unsupported architecture: %s", arch)
	}

	ver := strings.TrimSpace(version)
	if ver == "" || ver == "latest" {
		return "", fmt.Errorf("explicit version required for mosdns")
	}

	return fmt.Sprintf("https://github.com/IrineSistiana/mosdns/releases/download/%s/mosdns-linux-%s.zip", ver, arch), nil
}
