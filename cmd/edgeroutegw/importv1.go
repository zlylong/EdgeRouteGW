package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/zlylong/EdgeRouteGW/internal/config"
	"github.com/zlylong/EdgeRouteGW/internal/migrate1x"
	"github.com/zlylong/EdgeRouteGW/internal/paths"
	"github.com/zlylong/EdgeRouteGW/internal/secret"
	"github.com/zlylong/EdgeRouteGW/internal/store"
)

func cmdImportV1(args []string, stdout, stderr io.Writer, env config.LookupEnv) int {
	fs := flag.NewFlagSet("import-v1", flag.ContinueOnError)
	fs.SetOutput(stderr)
	oldKey := fs.String("old-key", "", "path to the 1.x config/aes.key (needed to carry over SSH credentials of managed hosts)")
	dryRun := fs.Bool("dry-run", false, "report what would be imported without writing")
	replace := fs.Bool("replace", false, "import even if the target already has nodes or rules (replaces them)")
	asJSON := fs.Bool("json", false, "print the report as JSON")
	fs.Usage = func() {
		fmt.Fprintln(stderr, "Usage: edgeroutegw import-v1 [flags] /root/proxygw/config/proxygw.db")
		fs.PrintDefaults()
	}
	cfg, err := config.Parse(fs, args, env)
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		fmt.Fprintln(stderr, "edgeroutegw import-v1:", err)
		return 2
	}
	if fs.NArg() != 1 {
		fs.Usage()
		return 2
	}
	opts := migrate1x.Options{SourceDB: fs.Arg(0), DryRun: *dryRun, Replace: *replace}
	if *oldKey != "" {
		b, err := os.ReadFile(*oldKey)
		if err != nil {
			fmt.Fprintln(stderr, "read old key:", err)
			return 1
		}
		if len(b) != 32 {
			fmt.Fprintf(stderr, "old key must be 32 raw bytes, got %d\n", len(b))
			return 1
		}
		opts.OldKey = b
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	st, err := store.Open(ctx, cfg.DBPath())
	if err != nil {
		fmt.Fprintln(stderr, "open database:", err)
		return 1
	}
	defer st.Close()
	if _, err := st.Migrate(ctx); err != nil {
		fmt.Fprintln(stderr, "migrate:", err)
		return 1
	}
	kr, err := secret.LoadOrCreate(paths.New(cfg.DataDir).MasterKey())
	if err != nil {
		fmt.Fprintln(stderr, "master key:", err)
		return 1
	}
	rep, err := migrate1x.Import(ctx, st, kr, opts)
	if rep != nil && *asJSON {
		_ = json.NewEncoder(stdout).Encode(rep)
	} else if rep != nil {
		verb := "imported"
		if *dryRun {
			verb = "would import"
		}
		fmt.Fprintf(stdout, "%s: %d nodes, %d outbound groups, %d rule sets, %d rules, %d device policies, %d bypass destinations, %d hosts (%d deploy records); mode %s\n",
			verb, rep.Nodes, rep.Groups, rep.RuleSets, rep.Rules, rep.DevicePolicies, rep.Bypass, rep.Hosts, rep.Deploys, rep.Mode)
		for _, w := range rep.SortedWarnings() {
			fmt.Fprintln(stdout, "  warning:", w)
		}
		if rep.Generation > 0 {
			fmt.Fprintf(stdout, "generation %d; the reconciler applies it on the next start of edgeroutegw serve\n", rep.Generation)
		}
	}
	if err != nil {
		fmt.Fprintln(stderr, "import failed:", err)
		return 1
	}
	return 0
}
