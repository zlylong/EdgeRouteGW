// Package config resolves runtime settings from flags and ERGW_* environment
// variables. Flags win over the environment, the environment wins over defaults.
package config

import (
	"flag"
	"fmt"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
)

// Config is everything the process needs to know before it opens the store.
// Operational state (mode, nodes, rules) lives in the store, not here.
type Config struct {
	// ListenAddr is the HTTP listen address for API and UI.
	ListenAddr string
	// DataDir holds the SQLite database, encryption keys and downloaded
	// components. It must be writable by the service.
	DataDir string
	// LogLevel is one of debug, info, warn, error.
	LogLevel string
}

const (
	DefaultListenAddr = ":80"
	DefaultDataDir    = "/var/lib/edgeroutegw"
	DefaultLogLevel   = "info"
	envPrefix         = "ERGW_"
)

// LookupEnv is the shape of os.LookupEnv; injected so tests can control it.
type LookupEnv func(key string) (string, bool)

// Parse builds a Config from args and env. It registers the shared flags on fs,
// so the caller owns fs and may add subcommand-specific flags before calling.
func Parse(fs *flag.FlagSet, args []string, env LookupEnv) (Config, error) {
	cfg := Config{
		ListenAddr: envOr(env, "LISTEN", DefaultListenAddr),
		DataDir:    envOr(env, "DATA_DIR", DefaultDataDir),
		LogLevel:   envOr(env, "LOG_LEVEL", DefaultLogLevel),
	}
	fs.StringVar(&cfg.ListenAddr, "listen", cfg.ListenAddr, "HTTP listen address (env ERGW_LISTEN)")
	fs.StringVar(&cfg.DataDir, "data-dir", cfg.DataDir, "data directory for database, keys and components (env ERGW_DATA_DIR)")
	fs.StringVar(&cfg.LogLevel, "log-level", cfg.LogLevel, "log level: debug|info|warn|error (env ERGW_LOG_LEVEL)")
	if err := fs.Parse(args); err != nil {
		return Config{}, err
	}
	if err := cfg.validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func (c *Config) validate() error {
	if strings.TrimSpace(c.ListenAddr) == "" {
		return fmt.Errorf("listen address must not be empty")
	}
	if strings.TrimSpace(c.DataDir) == "" {
		return fmt.Errorf("data directory must not be empty")
	}
	abs, err := filepath.Abs(c.DataDir)
	if err != nil {
		return fmt.Errorf("resolve data directory: %w", err)
	}
	c.DataDir = abs
	if _, err := ParseLevel(c.LogLevel); err != nil {
		return err
	}
	c.LogLevel = strings.ToLower(c.LogLevel)
	return nil
}

// DBPath is the SQLite database file inside DataDir.
func (c Config) DBPath() string { return filepath.Join(c.DataDir, "edgeroutegw.db") }

// ParseLevel maps a level name to slog.Level.
func ParseLevel(s string) (slog.Level, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "debug":
		return slog.LevelDebug, nil
	case "info":
		return slog.LevelInfo, nil
	case "warn", "warning":
		return slog.LevelWarn, nil
	case "error":
		return slog.LevelError, nil
	default:
		return 0, fmt.Errorf("unknown log level %q (want debug|info|warn|error)", s)
	}
}

// NewLogger builds the process logger writing to w at the configured level.
func (c Config) NewLogger(w io.Writer) *slog.Logger {
	lvl, err := ParseLevel(c.LogLevel)
	if err != nil {
		lvl = slog.LevelInfo
	}
	return slog.New(slog.NewTextHandler(w, &slog.HandlerOptions{Level: lvl}))
}

func envOr(env LookupEnv, key, def string) string {
	if env == nil {
		return def
	}
	if v, ok := env(envPrefix + key); ok && strings.TrimSpace(v) != "" {
		return v
	}
	return def
}
