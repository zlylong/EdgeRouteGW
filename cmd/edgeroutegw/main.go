// Command edgeroutegw is the EdgeRouteGW 2.0 control plane: HTTP API + UI,
// the reconciler, and the operator CLI. Subcommands:
//
//	serve    run the service (default when no subcommand is given)
//	migrate  apply pending database migrations and exit
//	version  print build information
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/zlylong/EdgeRouteGW/internal/config"
	"github.com/zlylong/EdgeRouteGW/internal/store"
	"github.com/zlylong/EdgeRouteGW/internal/version"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr, os.LookupEnv))
}

func run(args []string, stdout, stderr io.Writer, env config.LookupEnv) int {
	cmd := "serve"
	if len(args) > 0 && !isFlag(args[0]) {
		cmd, args = args[0], args[1:]
	}
	switch cmd {
	case "serve":
		return cmdServe(args, stdout, stderr, env)
	case "migrate":
		return cmdMigrate(args, stdout, stderr, env)
	case "version":
		return cmdVersion(stdout)
	case "help", "-h", "--help":
		usage(stdout)
		return 0
	default:
		fmt.Fprintf(stderr, "edgeroutegw: unknown command %q\n\n", cmd)
		usage(stderr)
		return 2
	}
}

func isFlag(s string) bool { return len(s) > 0 && s[0] == '-' }

func usage(w io.Writer) {
	fmt.Fprint(w, `Usage: edgeroutegw <command> [flags]

Commands:
  serve     run the API, UI and reconciler (default)
  migrate   apply pending database migrations and exit
  version   print build information

Flags shared by serve and migrate (environment variable in parentheses):
  -listen     HTTP listen address        (ERGW_LISTEN,    default :80)
  -data-dir   database and state dir     (ERGW_DATA_DIR,  default /var/lib/edgeroutegw)
  -log-level  debug|info|warn|error      (ERGW_LOG_LEVEL, default info)

serve only:
  -no-reconcile   store desired state but never drive Xray/Mosdns/nftables (development)
`)
}

func cmdVersion(stdout io.Writer) int {
	v := version.Get()
	fmt.Fprintf(stdout, "edgeroutegw %s\ncommit:  %s\nbuilt:   %s\ngo:      %s %s/%s\n",
		v.Version, v.Commit, orDash(v.BuildTime), v.Go, v.OS, v.Arch)
	return 0
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func cmdMigrate(args []string, stdout, stderr io.Writer, env config.LookupEnv) int {
	fs := flag.NewFlagSet("migrate", flag.ContinueOnError)
	fs.SetOutput(stderr)
	cfg, err := config.Parse(fs, args, env)
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		fmt.Fprintln(stderr, "edgeroutegw migrate:", err)
		return 2
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	st, err := store.Open(ctx, cfg.DBPath())
	if err != nil {
		fmt.Fprintln(stderr, "open database:", err)
		return 1
	}
	defer st.Close()
	applied, err := st.Migrate(ctx)
	for _, v := range applied {
		fmt.Fprintln(stdout, "applied", v)
	}
	if err != nil {
		fmt.Fprintln(stderr, "migrate:", err)
		return 1
	}
	if len(applied) == 0 {
		fmt.Fprintln(stdout, "database is up to date")
	}
	return 0
}
