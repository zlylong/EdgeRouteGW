package v2

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

// geoTagsResp lists the tags of one installed geodata file.
type geoTagsResp struct {
	Kind      string   `json:"kind"`
	Installed bool     `json:"installed"`
	Version   string   `json:"version,omitempty"`
	Items     []string `json:"items"`
}

// geoTags answers GET /geo/tags?kind=geosite|geoip from the installed
// geosite.dat / geoip.dat. Without geodata the list is empty and
// installed=false, so the UI can fall back to free text.
func (d Deps) geoTags(w http.ResponseWriter, r *http.Request) {
	kind := strings.ToLower(r.URL.Query().Get("kind"))
	resp := geoTagsResp{Kind: kind, Items: []string{}}
	switch kind {
	case "geosite":
		if d.GeoSite != nil {
			if tags, err := d.GeoSite.Tags(); err == nil {
				resp.Installed, resp.Items = true, tags
			}
		}
	case "geoip":
		if d.GeoIP != nil {
			if tags, err := d.GeoIP.Tags(); err == nil {
				resp.Installed, resp.Items = true, tags
			}
		}
	default:
		d.fail(w, r, invalid("kind", "kind must be geosite or geoip"))
		return
	}
	if resp.Installed {
		// The release tag written by the installer, not the reader's own
		// size/mtime fingerprint.
		if b, err := os.ReadFile(filepath.Join(d.Paths.GeoDir(), "geodata.ver")); err == nil {
			resp.Version = strings.TrimSpace(string(b))
		}
	}
	writeJSON(w, http.StatusOK, resp)
}
