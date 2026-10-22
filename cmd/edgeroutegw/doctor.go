package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"github.com/zlylong/EdgeRouteGW/internal/driver/execx"
	"github.com/zlylong/EdgeRouteGW/internal/netinfo"
	"github.com/zlylong/EdgeRouteGW/internal/tune"
	"io"
	"net"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/zlylong/EdgeRouteGW/internal/config"
	"github.com/zlylong/EdgeRouteGW/internal/driver/systemd"
	"github.com/zlylong/EdgeRouteGW/internal/paths"
)

// cmdDoctor checks the host for everything the gateway needs and prints a
// pass/warn/fail list. Exit status is 1 when anything fails.
func cmdDoctor(args []string, stdout, stderr io.Writer, env config.LookupEnv) int {
	fs := flag.NewFlagSet("doctor", flag.ContinueOnError)
	fs.SetOutput(stderr)
	cfg, err := config.Parse(fs, args, env)
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		fmt.Fprintln(stderr, "edgeroutegw doctor:", err)
		return 2
	}
	p := paths.New(cfg.DataDir)
	failed := false
	check := func(ok bool, warn bool, what, detail string) {
		mark := "ok  "
		if !ok {
			if warn {
				mark = "warn"
			} else {
				mark = "FAIL"
				failed = true
			}
		}
		fmt.Fprintf(stdout, "[%s] %s", mark, what)
		if detail != "" {
			fmt.Fprintf(stdout, " — %s", detail)
		}
		fmt.Fprintln(stdout)
	}

	check(os.Geteuid() == 0, true, "running as root", "needed for nftables, netlink and systemd control")
	if st, err := os.Stat(cfg.DataDir); err == nil && st.IsDir() {
		check(true, false, "data directory "+cfg.DataDir, "")
	} else {
		check(false, false, "data directory "+cfg.DataDir, "missing")
	}
	for _, c := range []struct{ name, path string }{{"xray", p.XrayBinary()}, {"mosdns", p.MosdnsBinary()}, {"geosite.dat", p.GeoSite()}, {"geoip.dat", p.GeoIP()}} {
		_, err := os.Stat(c.path)
		check(err == nil, true, "component "+c.name, map[bool]string{true: c.path, false: "not installed (System → Components)"}[err == nil])
	}
	for _, bin := range []string{"nft", "ip", "vtysh"} {
		_, err := exec.LookPath(bin)
		check(err == nil, bin == "vtysh", "binary "+bin, map[bool]string{true: "", false: "not found" + map[bool]string{true: " (frr; only needed for Mode B/C)", false: ""}[bin == "vtysh"]}[err == nil])
	}
	if b, err := os.ReadFile("/proc/sys/net/ipv4/ip_forward"); err == nil {
		v := strings.TrimSpace(string(b))
		check(v == "1", false, "net.ipv4.ip_forward", "= "+v)
	}
	// Environment tuning report (inspection only; the service applies).
	tuner := tune.New(execx.Real{Timeout: 10 * time.Second}, func() string { di, _ := netinfo.DefaultIface(); return di })
	for _, it := range tuner.Run(context.Background(), false).Items {
		ok := it.Status == tune.StatusOK || it.Status == tune.StatusApplied || it.Status == tune.StatusInfo
		detail := it.Detail
		if it.Hint != "" {
			detail += " (" + it.Hint + ")"
		}
		check(ok, true, "tuning: "+it.Title, detail)
	}
	for _, port := range []string{"53", "80"} {
		ln, err := net.Listen("tcp", ":"+port)
		if err == nil {
			ln.Close()
			check(true, false, "port "+port+" free", "")
		} else {
			check(false, true, "port "+port+" in use", "expected while the gateway (or systemd-resolved on 53) runs")
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	sd := systemd.NewDBus()
	if st, err := sd.State(ctx, "edgeroutegw.service"); err != nil {
		check(false, true, "systemd D-Bus", err.Error())
	} else {
		check(true, false, "systemd D-Bus", "edgeroutegw.service is "+st.ActiveState)
		for _, u := range []string{"edgeroutegw-xray.service", "edgeroutegw-mosdns.service", "frr.service"} {
			if st, err := sd.State(ctx, u); err == nil {
				check(st.Active(), true, u, st.ActiveState+"/"+st.SubState)
			}
		}
	}
	if _, err := os.Stat("/etc/frr/daemons"); err == nil {
		b, _ := os.ReadFile("/etc/frr/daemons")
		check(strings.Contains(string(b), "ospfd=yes"), true, "frr ospfd enabled", "edgeroutegw enables it on the first Mode B/C apply")
	}
	if failed {
		return 1
	}
	return 0
}
