package config

import (
	"flag"
	"io"
	"log/slog"
	"testing"
)

func envFrom(m map[string]string) LookupEnv {
	return func(k string) (string, bool) { v, ok := m[k]; return v, ok }
}

func parse(t *testing.T, args []string, env map[string]string) (Config, error) {
	t.Helper()
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	return Parse(fs, args, envFrom(env))
}

func TestDefaults(t *testing.T) {
	cfg, err := parse(t, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ListenAddr != DefaultListenAddr || cfg.DataDir != DefaultDataDir || cfg.LogLevel != DefaultLogLevel {
		t.Fatalf("unexpected defaults: %+v", cfg)
	}
	if cfg.DBPath() != DefaultDataDir+"/edgeroutegw.db" {
		t.Fatalf("db path %q", cfg.DBPath())
	}
}

func TestEnvThenFlagPrecedence(t *testing.T) {
	env := map[string]string{"ERGW_LISTEN": "127.0.0.1:9000", "ERGW_LOG_LEVEL": "WARN", "ERGW_DATA_DIR": "  "}
	cfg, err := parse(t, []string{"-listen", "127.0.0.1:9001"}, env)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ListenAddr != "127.0.0.1:9001" {
		t.Fatalf("flag should beat env, got %q", cfg.ListenAddr)
	}
	if cfg.LogLevel != "warn" {
		t.Fatalf("log level should come from env and be normalised, got %q", cfg.LogLevel)
	}
	if cfg.DataDir != DefaultDataDir {
		t.Fatalf("blank env value should fall back to default, got %q", cfg.DataDir)
	}
}

func TestRelativeDataDirBecomesAbsolute(t *testing.T) {
	cfg, err := parse(t, []string{"-data-dir", "./.dev-data"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.DataDir[0] != '/' {
		t.Fatalf("data dir not absolute: %q", cfg.DataDir)
	}
}

func TestInvalid(t *testing.T) {
	if _, err := parse(t, []string{"-log-level", "loud"}, nil); err == nil {
		t.Fatal("bad log level accepted")
	}
	if _, err := parse(t, []string{"-listen", ""}, nil); err == nil {
		t.Fatal("empty listen accepted")
	}
	if _, err := parse(t, []string{"-bogus"}, nil); err == nil {
		t.Fatal("unknown flag accepted")
	}
}

func TestParseLevel(t *testing.T) {
	cases := map[string]slog.Level{"debug": slog.LevelDebug, "Info": slog.LevelInfo, "warning": slog.LevelWarn, "ERROR": slog.LevelError}
	for in, want := range cases {
		got, err := ParseLevel(in)
		if err != nil || got != want {
			t.Fatalf("ParseLevel(%q) = %v, %v; want %v", in, got, err, want)
		}
	}
}
