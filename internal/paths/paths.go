// Package paths centralises every filesystem location the service uses so the
// drivers, renderer and packaging agree on them.
package paths

import "path/filepath"

// Paths derives all locations from the data directory and the runtime
// directory. RunDir is tmpfs (systemd RuntimeDirectory) and holds logs the
// connection tracker tails.
type Paths struct {
	DataDir string
	RunDir  string
}

// New returns Paths rooted at dataDir with the default runtime directory.
func New(dataDir string) Paths {
	return Paths{DataDir: dataDir, RunDir: "/run/edgeroutegw"}
}

// Components is where downloaded binaries live, one subdirectory each.
func (p Paths) Components() string      { return filepath.Join(p.DataDir, "components") }
func (p Paths) XrayBinary() string      { return filepath.Join(p.Components(), "xray", "xray") }
func (p Paths) MosdnsBinary() string    { return filepath.Join(p.Components(), "mosdns", "mosdns") }
func (p Paths) GeoDir() string          { return filepath.Join(p.DataDir, "geodata") }
func (p Paths) GeoSite() string         { return filepath.Join(p.GeoDir(), "geosite.dat") }
func (p Paths) GeoIP() string           { return filepath.Join(p.GeoDir(), "geoip.dat") }
func (p Paths) KeysDir() string         { return filepath.Join(p.DataDir, "keys") }
func (p Paths) MasterKey() string       { return filepath.Join(p.KeysDir(), "master.key") }
func (p Paths) XrayDir() string         { return filepath.Join(p.DataDir, "xray") }
func (p Paths) XrayConfig() string      { return filepath.Join(p.XrayDir(), "config.json") }
func (p Paths) XrayAccessLog() string   { return filepath.Join(p.RunDir, "xray_access.log") }
func (p Paths) MosdnsDir() string       { return filepath.Join(p.DataDir, "mosdns") }
func (p Paths) MosdnsConfig() string    { return filepath.Join(p.MosdnsDir(), "config.yaml") }
func (p Paths) MosdnsProxyList() string { return filepath.Join(p.MosdnsDir(), "proxy_domains.txt") }
func (p Paths) MosdnsCNList() string    { return filepath.Join(p.MosdnsDir(), "geosite_cn.txt") }
func (p Paths) MosdnsLog() string       { return filepath.Join(p.RunDir, "mosdns.log") }
func (p Paths) NftDir() string          { return filepath.Join(p.DataDir, "nft") }
func (p Paths) NftRuleset() string      { return filepath.Join(p.NftDir(), "ruleset.nft") }
