// Package provision deploys tunnel servers (WireGuard, VLESS-REALITY) onto
// managed remote hosts over SSH and registers the matching outbound node.
package provision

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"net"
	"regexp"
	"strconv"
	"strings"

	"github.com/google/uuid"
	"golang.org/x/crypto/curve25519"
)

func x25519() ([32]byte, [32]byte, error) {
	var priv, pub [32]byte
	if _, err := rand.Read(priv[:]); err != nil {
		return priv, pub, err
	}
	priv[0] &= 248
	priv[31] &= 127
	priv[31] |= 64
	curve25519.ScalarBaseMult(&pub, &priv)
	return priv, pub, nil
}

// WireGuardKeys returns a base64 (std) private/public pair as wg(8) expects.
func WireGuardKeys() (priv, pub string, err error) {
	k, p, err := x25519()
	if err != nil {
		return "", "", err
	}
	return base64.StdEncoding.EncodeToString(k[:]), base64.StdEncoding.EncodeToString(p[:]), nil
}

// RealityKeys returns the raw-URL-base64 pair Xray's REALITY uses.
func RealityKeys() (priv, pub string, err error) {
	k, p, err := x25519()
	if err != nil {
		return "", "", err
	}
	return base64.RawURLEncoding.EncodeToString(k[:]), base64.RawURLEncoding.EncodeToString(p[:]), nil
}

// ShortID returns an 8-hex-char REALITY short id.
func ShortID() (string, error) {
	b := make([]byte, 4)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// NewUUID returns a v4 UUID for VLESS clients.
func NewUUID() string { return uuid.NewString() }

// WGParams are the generated WireGuard tunnel parameters.
type WGParams struct {
	Port       int    `json:"port"`
	ServerPriv string `json:"server_priv"`
	ServerPub  string `json:"server_pub"`
	ClientPriv string `json:"client_priv"`
	ClientPub  string `json:"client_pub"`
	TunnelCIDR string `json:"tunnel_cidr"` // server address, e.g. 10.77.0.1/24
	ClientAddr string `json:"client_addr"` // e.g. 10.77.0.2/32
	MTU        int    `json:"mtu"`
}

// VlessParams are the generated VLESS-REALITY parameters.
type VlessParams struct {
	Port        int    `json:"port"`
	UUID        string `json:"uuid"`
	RealityPriv string `json:"reality_priv"`
	RealityPub  string `json:"reality_pub"`
	ShortID     string `json:"short_id"`
	ServerName  string `json:"server_name"`
	Dest        string `json:"dest"`
}

// DefaultRealityDest is the camouflage target that passes a real REALITY
// handshake (www.microsoft.com passes every pre-check and then returns EOF).
const DefaultRealityDest = "www.apple.com"

// NewWGParams generates a WireGuard parameter set.
func NewWGParams(port int) (WGParams, error) {
	if port <= 0 {
		port = 51820
	}
	sPriv, sPub, err := WireGuardKeys()
	if err != nil {
		return WGParams{}, err
	}
	cPriv, cPub, err := WireGuardKeys()
	if err != nil {
		return WGParams{}, err
	}
	return WGParams{Port: port, ServerPriv: sPriv, ServerPub: sPub, ClientPriv: cPriv, ClientPub: cPub, TunnelCIDR: "10.77.0.1/24", ClientAddr: "10.77.0.2/32", MTU: 1380}, nil
}

// NewVlessParams generates a VLESS-REALITY parameter set.
func NewVlessParams(port int, serverName, dest string) (VlessParams, error) {
	if port <= 0 {
		port = 443
	}
	if serverName == "" {
		serverName = DefaultRealityDest
	}
	if dest == "" {
		dest = serverName + ":443"
	}
	serverName = strings.ToLower(serverName)
	if !hostnameRe.MatchString(serverName) {
		return VlessParams{}, fmt.Errorf("server_name must be a hostname")
	}
	if h, port, err := net.SplitHostPort(dest); err != nil || !(hostnameRe.MatchString(strings.ToLower(h)) || net.ParseIP(h) != nil) || !validPortString(port) {
		return VlessParams{}, fmt.Errorf("dest must be host:port")
	}
	priv, pub, err := RealityKeys()
	if err != nil {
		return VlessParams{}, err
	}
	sid, err := ShortID()
	if err != nil {
		return VlessParams{}, err
	}
	return VlessParams{Port: port, UUID: NewUUID(), RealityPriv: priv, RealityPub: pub, ShortID: sid, ServerName: serverName, Dest: dest}, nil
}

func validPort(p int) error {
	if p < 1 || p > 65535 {
		return fmt.Errorf("port %d out of range", p)
	}
	return nil
}

// hostnameRe is a DNS hostname (lowercase, at least two labels). Values
// matching it are safe to place inside a shell script and a JSON config.
var hostnameRe = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,62}[a-z0-9])?(\.[a-z0-9]([a-z0-9-]{0,62}[a-z0-9])?)+$`)

func validPortString(p string) bool {
	n, err := strconv.Atoi(p)
	return err == nil && n > 0 && n < 65536
}
