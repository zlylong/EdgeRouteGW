package v2

import (
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/zlylong/EdgeRouteGW/internal/geo"
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

// geoLookupResp answers "which tags contain this target".
type geoLookupResp struct {
	Target  string   `json:"target"`
	Kind    string   `json:"kind"` // domain | ip
	GeoSite []string `json:"geosite"`
	GeoIP   []string `json:"geoip"`
}

func (d Deps) geoLookup(w http.ResponseWriter, r *http.Request) {
	target := strings.TrimSpace(r.URL.Query().Get("target"))
	if target == "" {
		d.fail(w, r, invalid("target", "provide a domain or IP"))
		return
	}
	resp := geoLookupResp{Target: target, Kind: "domain", GeoSite: []string{}, GeoIP: []string{}}
	host := strings.Trim(target, "[]")
	if h, _, err := geo.SplitHostPort(target); err == nil {
		if _, perr := netip.ParseAddr(strings.Trim(h, "[]")); perr == nil || strings.Contains(h, ".") {
			host = strings.Trim(h, "[]")
		}
	}
	if addr, err := netip.ParseAddr(host); err == nil {
		resp.Kind = "ip"
		resp.Target = addr.String()
		if d.IPIndex != nil {
			if tags, err := d.IPIndex.Lookup(addr); err == nil {
				resp.GeoIP = tags
			}
		}
	} else {
		resp.Target = strings.ToLower(strings.TrimSuffix(host, "."))
		if d.SiteIndex != nil {
			if tags, err := d.SiteIndex.Lookup(resp.Target); err == nil {
				resp.GeoSite = tags
			}
		}
	}
	writeJSON(w, http.StatusOK, resp)
}

// geoEntriesResp previews one tag's contents.
type geoEntriesResp struct {
	Kind  string   `json:"kind"`
	Tag   string   `json:"tag"`
	Total int      `json:"total"`
	Items []string `json:"items"` // "domain:x", "full:x", "keyword:x", "regexp:x" or CIDRs
}

func (d Deps) geoEntries(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	kind := strings.ToLower(q.Get("kind"))
	tag := strings.ToLower(strings.TrimSpace(q.Get("tag")))
	filter := strings.ToLower(strings.TrimSpace(q.Get("q")))
	limit, _ := strconv.Atoi(q.Get("limit"))
	if limit <= 0 || limit > 2000 {
		limit = 300
	}
	if tag == "" {
		d.fail(w, r, invalid("tag", "tag is required"))
		return
	}
	resp := geoEntriesResp{Kind: kind, Tag: tag, Items: []string{}}
	var all []string
	switch kind {
	case "geosite":
		if d.GeoSite == nil {
			d.fail(w, r, invalid("kind", "geosite.dat is not installed"))
			return
		}
		entries, err := d.GeoSite.Entries(tag)
		if err != nil {
			d.fail(w, r, invalid("tag", "%v", err))
			return
		}
		all = make([]string, 0, len(entries))
		for _, e := range entries {
			all = append(all, e.MosdnsValue())
		}
	case "geoip":
		if d.GeoIP == nil {
			d.fail(w, r, invalid("kind", "geoip.dat is not installed"))
			return
		}
		cidrs, err := d.GeoIP.CIDRs(tag)
		if err != nil {
			d.fail(w, r, invalid("tag", "%v", err))
			return
		}
		all = make([]string, 0, len(cidrs))
		for _, p := range cidrs {
			all = append(all, p.String())
		}
	default:
		d.fail(w, r, invalid("kind", "kind must be geosite or geoip"))
		return
	}
	for _, v := range all {
		if filter != "" && !strings.Contains(strings.ToLower(v), filter) {
			continue
		}
		resp.Total++
		if len(resp.Items) < limit {
			resp.Items = append(resp.Items, v)
		}
	}
	writeJSON(w, http.StatusOK, resp)
}
