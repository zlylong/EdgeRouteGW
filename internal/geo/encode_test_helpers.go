package geo

import "google.golang.org/protobuf/encoding/protowire"

// EncodeSiteList builds a geosite.dat body from tag -> entries. It exists so
// tests and the importer can construct fixtures without a real file.
func EncodeSiteList(sites map[string][]Entry) []byte {
	var out []byte
	for tag, entries := range sites {
		var site []byte
		site = protowire.AppendTag(site, 1, protowire.BytesType)
		site = protowire.AppendString(site, tag)
		for _, e := range entries {
			var dm []byte
			dm = protowire.AppendTag(dm, 1, protowire.VarintType)
			dm = protowire.AppendVarint(dm, uint64(e.Kind))
			dm = protowire.AppendTag(dm, 2, protowire.BytesType)
			dm = protowire.AppendString(dm, e.Value)
			for _, a := range e.Attrs {
				var attr []byte
				attr = protowire.AppendTag(attr, 1, protowire.BytesType)
				attr = protowire.AppendString(attr, a)
				dm = protowire.AppendTag(dm, 3, protowire.BytesType)
				dm = protowire.AppendBytes(dm, attr)
			}
			site = protowire.AppendTag(site, 2, protowire.BytesType)
			site = protowire.AppendBytes(site, dm)
		}
		out = protowire.AppendTag(out, 1, protowire.BytesType)
		out = protowire.AppendBytes(out, site)
	}
	return out
}

// EncodeIPList builds a geoip.dat body from tag -> (ip bytes, prefix bits).
func EncodeIPList(sets map[string][]IPEntry) []byte {
	var out []byte
	for tag, entries := range sets {
		var g []byte
		g = protowire.AppendTag(g, 1, protowire.BytesType)
		g = protowire.AppendString(g, tag)
		for _, e := range entries {
			var c []byte
			c = protowire.AppendTag(c, 1, protowire.BytesType)
			c = protowire.AppendBytes(c, e.IP)
			c = protowire.AppendTag(c, 2, protowire.VarintType)
			c = protowire.AppendVarint(c, uint64(e.Bits))
			g = protowire.AppendTag(g, 2, protowire.BytesType)
			g = protowire.AppendBytes(g, c)
		}
		out = protowire.AppendTag(out, 1, protowire.BytesType)
		out = protowire.AppendBytes(out, g)
	}
	return out
}

// IPEntry is a raw CIDR for EncodeIPList.
type IPEntry struct {
	IP   []byte
	Bits int
}
