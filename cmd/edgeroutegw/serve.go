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
	"github.com/zlylong/EdgeRouteGW/internal/config"
	"github.com/zlylong/EdgeRouteGW/internal/driver"
	"github.com/zlylong/EdgeRouteGW/internal/driver/execx"
	"github.com/zlylong/EdgeRouteGW/internal/driver/mosdns"
	"github.com/zlylong/EdgeRouteGW/internal/driver/nft"
	"github.com/zlylong/EdgeRouteGW/internal/driver/systemd"
	"github.com/zlylong/EdgeRouteGW/internal/driver/xray"
	"github.com/zlylong/EdgeRouteGW/internal/events"
	"github.com/zlylong/EdgeRouteGW/internal/geo"
	"github.com/zlylong/EdgeRouteGW/internal/httpserver"
	"github.com/zlylong/EdgeRouteGW/internal/paths"
	"github.com/zlylong/EdgeRouteGW/internal/reconcile"
	"github.com/zlylong/EdgeRouteGW/internal/render"
	"github.com/zlylong/EdgeRouteGW/internal/secret"
	"github.com/zlylong/EdgeRouteGW/internal/store"
	"github.com/zlylong/EdgeRouteGW/internal/version"
)

func cmdServe(args []string, stdout, stderr io.Writer, env config.LookupEnv) int {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	fs.SetOutput(stderr)
	noReconcile := fs.Bool("no-reconcile", false, "serve the API without driving Xray/Mosdns/nftables (development)")
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

	bus := events.New(256)
	deps := render.Deps{Paths: p}
	if _, err := os.Stat(p.GeoSite()); err == nil {
		deps.GeoSite = geo.OpenSite(p.GeoSite())
	} else {
		log.Warn("geosite.dat not installed; geosite rules will not be expanded for DNS", "path", p.GeoSite())
	}

	var drivers []driver.Driver
	if !*noReconcile {
		run := execx.Real{Timeout: 60 * time.Second, Log: log}
		sd := systemd.NewDBus()
		drivers = []driver.Driver{
			xray.New(p, run, sd, log),
			mosdns.New(p, sd, log),
			nft.New(p, run, log),
		}
	} else {
		log.Warn("reconciliation disabled (-no-reconcile): desired state is stored but never applied")
	}
	rec := reconcile.New(st, kr, deps, drivers, bus, log)

	h := api.NewHandler(api.Deps{
		Store: st, Keyring: kr, Reconciler: rec, Bus: bus, Paths: p, Log: log, StartedAt: time.Now(),
	})

	g, gctx := errgroup.WithContext(ctx)
	g.Go(func() error { return rec.Run(gctx) })
	g.Go(func() error { return httpserver.Serve(gctx, cfg.ListenAddr, h, log, nil) })
	if err := g.Wait(); err != nil {
		log.Error("service failed", "err", err)
		return 1
	}
	log.Info("edgeroutegw stopped")
	return 0
}
