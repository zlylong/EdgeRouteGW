package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/zlylong/EdgeRouteGW/internal/api"
	"github.com/zlylong/EdgeRouteGW/internal/auth"
	"github.com/zlylong/EdgeRouteGW/internal/config"
	"github.com/zlylong/EdgeRouteGW/internal/desired"
	"github.com/zlylong/EdgeRouteGW/internal/driver"
	"github.com/zlylong/EdgeRouteGW/internal/driver/execx"
	"github.com/zlylong/EdgeRouteGW/internal/driver/frr"
	"github.com/zlylong/EdgeRouteGW/internal/driver/mosdns"
	"github.com/zlylong/EdgeRouteGW/internal/driver/nft"
	"github.com/zlylong/EdgeRouteGW/internal/driver/route"
	"github.com/zlylong/EdgeRouteGW/internal/driver/systemd"
	"github.com/zlylong/EdgeRouteGW/internal/driver/xray"
	"github.com/zlylong/EdgeRouteGW/internal/events"
	"github.com/zlylong/EdgeRouteGW/internal/geo"
	"github.com/zlylong/EdgeRouteGW/internal/httpserver"
	"github.com/zlylong/EdgeRouteGW/internal/netinfo"
	"github.com/zlylong/EdgeRouteGW/internal/observe"
	"github.com/zlylong/EdgeRouteGW/internal/paths"
	"github.com/zlylong/EdgeRouteGW/internal/provision"
	"github.com/zlylong/EdgeRouteGW/internal/reconcile"
	"github.com/zlylong/EdgeRouteGW/internal/render"
	"github.com/zlylong/EdgeRouteGW/internal/resolver"
	"github.com/zlylong/EdgeRouteGW/internal/routes"
	"github.com/zlylong/EdgeRouteGW/internal/secret"
	"github.com/zlylong/EdgeRouteGW/internal/store"
	"github.com/zlylong/EdgeRouteGW/internal/update"
	"github.com/zlylong/EdgeRouteGW/internal/version"
)

func cmdServe(args []string, stdout, stderr io.Writer, env config.LookupEnv) int {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	fs.SetOutput(stderr)
	noReconcile := fs.Bool("no-reconcile", false, "serve the API without driving Xray/Mosdns/nftables (development)")
	bundleDir := fs.String("bundle-dir", update.BundleDir, "directory with pre-fetched components (edgeroutegw-components package)")
	cfg, err := config.Parse(fs, args, env)
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		fmt.Fprintln(stderr, "edgeroutegw serve:", err)
		return 2
	}
	log := cfg.NewLogger(stderr)
	v := version.Get()
	log.Info("edgeroutegw starting", "version", v.Version, "commit", v.Commit, "data_dir", cfg.DataDir, "listen", cfg.ListenAddr)

	// SIGHUP means "re-seed components from the bundle" (the components
	// package's postinst sends one). Register before anything else so an
	// early HUP never hits Go's default handler and kills the process.
	hup := make(chan os.Signal, 1)
	signal.Notify(hup, syscall.SIGHUP)
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	p := paths.New(cfg.DataDir)
	if rd, ok := env("RUNTIME_DIRECTORY"); ok && rd != "" {
		p.RunDir = rd
	}
	// Xray reads geosite/geoip from this directory, both as a service (unit
	// sets it too) and when we run "xray -test" as a child.
	_ = os.Setenv("XRAY_LOCATION_ASSET", p.GeoDir())

	st, err := store.Open(ctx, cfg.DBPath())
	if err != nil {
		log.Error("open database failed", "path", cfg.DBPath(), "err", err)
		return 1
	}
	defer st.Close()
	applied, err := st.Migrate(ctx)
	if err != nil {
		log.Error("database migration failed", "err", err, "applied", applied)
		return 1
	}
	if len(applied) > 0 {
		log.Info("database migrated", "applied", applied)
	}

	kr, err := secret.LoadOrCreate(p.MasterKey())
	if err != nil {
		log.Error("master key unavailable", "path", p.MasterKey(), "err", err)
		return 1
	}

	run := execx.Real{Timeout: 60 * time.Second, Log: log}
	authSvc := auth.New(st, cfg.DataDir, log)
	if err := authSvc.Bootstrap(ctx); err != nil {
		log.Error("auth bootstrap failed", "err", err)
		return 1
	}

	bus := events.New(256)

	// Components first: an offline install has them only in the bundle, and
	// the geo readers below need the files on disk.
	var updater *update.Service
	if !*noReconcile {
		updater = update.New(p, st, run, systemd.NewDBus(), bus, nil, log)
	} else {
		updater = update.New(p, st, run, systemd.NewFake(), bus, nil, log)
	}
	updater.BundleDir = *bundleDir
	updater.ApplyDownloadSettings(ctx)
	if seeded := updater.SeedFromBundle(ctx); len(seeded) > 0 {
		log.Info("installed bundled components", "components", seeded)
	}

	deps := render.Deps{Paths: p, IfaceAddr: render.SystemInterfaceAddr, DefaultIface: netinfo.DefaultIface}
	var geoSite *geo.Site
	var geoIP *geo.IP
	if _, err := os.Stat(p.GeoSite()); err == nil {
		geoSite = geo.OpenSite(p.GeoSite())
		deps.GeoSite = geoSite
	} else {
		log.Warn("geosite.dat not installed; geosite rules will not be expanded for DNS", "path", p.GeoSite())
	}
	if _, err := os.Stat(p.GeoIP()); err == nil {
		geoIP = geo.OpenIP(p.GeoIP())
		deps.GeoIP = geoIP
	} else {
		log.Warn("geoip.dat not installed; geoip rules will not be published as routes", "path", p.GeoIP())
	}
	conns := observe.NewConnTracker(p.XrayAccessLog(), bus, log)
	latency := observe.NewLatencyProber(st, bus, log)
	traffic := observe.NewTrafficSampler(st, bus, observe.XrayStats(run, p.XrayBinary()), log)

	var drivers []driver.Driver
	var frrDriver *frr.Driver
	if !*noReconcile {
		sd := systemd.NewDBus()
		frrDriver = frr.New(run, sd, route.New(log), log)
		drivers = []driver.Driver{
			xray.New(p, run, sd, log),
			mosdns.New(p, sd, log),
			nft.New(p, run, log),
			frrDriver,
		}
	} else {
		log.Warn("reconciliation disabled (-no-reconcile): desired state is stored but never applied")
	}
	rec := reconcile.New(st, kr, deps, drivers, bus, log)

	// Mode C resolution goes through the local Mosdns so published routes
	// match what LAN clients resolve.
	res := resolver.New(st, "127.0.0.1:53", 8, log)
	resLoop := resolver.NewLoop(res, func(ctx context.Context) ([]string, error) {
		_, arts, err := rec.PlanOnly(ctx)
		if err != nil {
			return nil, err
		}
		if arts.Mode == "A" {
			return nil, nil
		}
		snap, err := desired.Load(ctx, st, nil)
		if err != nil {
			return nil, err
		}
		return append(arts.ResolveDomains, routes.ProtectedDomains(snap)...), nil
	}, rec.Notify, log)

	prov := provision.New(st, kr, bus, rec.Notify, log)
	updater.Notify = rec.Notify
	go func() {
		for range hup {
			updater.SeedFromBundle(context.Background())
		}
	}()

	h := api.NewHandler(api.Deps{
		Store: st, Keyring: kr, Reconciler: rec, Bus: bus, Paths: p, Log: log, StartedAt: time.Now(),
		Auth: authSvc, GeoSite: geoSite, GeoIP: geoIP, Connections: conns, FRR: frrDriver, ResolverKick: resLoop.Kick,
		IfaceAddr: render.SystemInterfaceAddr, DefaultIface: netinfo.DefaultIface, Provision: prov, Updater: updater, Traffic: traffic, Latency: latency,
	})

	g, gctx := errgroup.WithContext(ctx)
	g.Go(func() error { return rec.Run(gctx) })
	g.Go(func() error { return conns.Run(gctx) })
	g.Go(func() error { return resLoop.Run(gctx) })
	g.Go(func() error { return updater.RunScheduler(gctx) })
	g.Go(func() error { return latency.Run(gctx) })
	if !*noReconcile {
		g.Go(func() error { return traffic.Run(gctx) })
		g.Go(func() error {
			// Keep 90 days of per-minute samples.
			t := time.NewTicker(6 * time.Hour)
			defer t.Stop()
			for {
				select {
				case <-gctx.Done():
					return nil
				case <-t.C:
					_ = st.PruneTraffic(gctx, 90*24*time.Hour)
				}
			}
		})
	}
	g.Go(func() error { return httpserver.Serve(gctx, cfg.ListenAddr, h, log, nil) })
	if err := g.Wait(); err != nil {
		log.Error("service failed", "err", err)
		return 1
	}
	log.Info("edgeroutegw stopped")
	return 0
}
