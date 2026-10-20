// Package version exposes build metadata injected via -ldflags, with a
// fallback to the Go module build info when the binary was built without them.
package version

import (
	"runtime"
	"runtime/debug"
)

// Set at build time via -ldflags "-X ...". Empty when built with plain go build.
var (
	Version   = ""
	Commit    = ""
	BuildTime = ""
)

// Info is the resolved build metadata.
type Info struct {
	Version   string `json:"version"`
	Commit    string `json:"commit"`
	BuildTime string `json:"build_time"`
	Go        string `json:"go"`
	OS        string `json:"os"`
	Arch      string `json:"arch"`
}

// Get resolves the build metadata once per call. It never returns empty
// strings for Version or Commit: unknown values are reported as "dev" and
// "unknown" so callers and UIs do not have to special-case them.
func Get() Info {
	info := Info{
		Version:   Version,
		Commit:    Commit,
		BuildTime: BuildTime,
		Go:        runtime.Version(),
		OS:        runtime.GOOS,
		Arch:      runtime.GOARCH,
	}
	if bi, ok := debug.ReadBuildInfo(); ok {
		if info.Version == "" && bi.Main.Version != "" && bi.Main.Version != "(devel)" {
			info.Version = bi.Main.Version
		}
		for _, s := range bi.Settings {
			switch s.Key {
			case "vcs.revision":
				if info.Commit == "" {
					info.Commit = s.Value
				}
			case "vcs.time":
				if info.BuildTime == "" {
					info.BuildTime = s.Value
				}
			case "vcs.modified":
				if s.Value == "true" && info.Commit != "" && info.Commit == commitFrom(bi) {
					info.Commit += "-dirty"
				}
			}
		}
	}
	if info.Version == "" {
		info.Version = "dev"
	}
	if info.Commit == "" {
		info.Commit = "unknown"
	}
	if len(info.Commit) > 12 && !hasSuffix(info.Commit, "-dirty") {
		info.Commit = info.Commit[:12]
	}
	return info
}

func commitFrom(bi *debug.BuildInfo) string {
	for _, s := range bi.Settings {
		if s.Key == "vcs.revision" {
			return s.Value
		}
	}
	return ""
}

func hasSuffix(s, suf string) bool {
	return len(s) >= len(suf) && s[len(s)-len(suf):] == suf
}
