// Package execx is the one place external commands are run. Drivers depend
// on the Runner interface so tests can record invocations instead of needing
// nft, xray or systemctl on the build machine.
package execx

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// Result is what a command produced.
type Result struct {
	Output   []byte // combined stdout+stderr
	ExitCode int
	Duration time.Duration
}

// Runner runs a command to completion.
type Runner interface {
	Run(ctx context.Context, name string, args ...string) (Result, error)
}

// Real runs commands with os/exec under a per-command timeout.
type Real struct {
	Timeout time.Duration
	Log     *slog.Logger
}

// Run implements Runner.
func (r Real) Run(ctx context.Context, name string, args ...string) (Result, error) {
	timeout := r.Timeout
	if timeout <= 0 {
		timeout = 60 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	start := time.Now()
	cmd := exec.CommandContext(ctx, name, args...)
	var buf bytes.Buffer
	cmd.Stdout, cmd.Stderr = &buf, &buf
	err := cmd.Run()
	res := Result{Output: buf.Bytes(), Duration: time.Since(start)}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		res.ExitCode = exitErr.ExitCode()
	}
	if err != nil && r.Log != nil {
		r.Log.Warn("command failed", "cmd", name, "args", strings.Join(args, " "), "exit", res.ExitCode, "dur_ms", res.Duration.Milliseconds(), "output", truncate(buf.String(), 400))
	} else if r.Log != nil && res.Duration > 2*time.Second {
		r.Log.Info("slow command", "cmd", name, "args", strings.Join(args, " "), "dur_ms", res.Duration.Milliseconds())
	}
	if err != nil {
		return res, fmt.Errorf("%s %s: %w: %s", name, strings.Join(args, " "), err, truncate(buf.String(), 200))
	}
	return res, nil
}

func truncate(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) > n {
		return s[:n] + "…"
	}
	return s
}

// Call is one recorded invocation.
type Call struct {
	Name string
	Args []string
}

// Fake records calls and answers from a script of responses.
type Fake struct {
	mu    sync.Mutex
	Calls []Call
	// Handler decides the outcome; nil means success with empty output.
	Handler func(name string, args []string) (Result, error)
}

// Run implements Runner.
func (f *Fake) Run(_ context.Context, name string, args ...string) (Result, error) {
	f.mu.Lock()
	f.Calls = append(f.Calls, Call{Name: name, Args: append([]string(nil), args...)})
	h := f.Handler
	f.mu.Unlock()
	if h == nil {
		return Result{}, nil
	}
	return h(name, args)
}

// Count returns how many calls started with name and the given leading args.
func (f *Fake) Count(name string, lead ...string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
outer:
	for _, c := range f.Calls {
		if c.Name != name || len(c.Args) < len(lead) {
			continue
		}
		for i, a := range lead {
			if c.Args[i] != a {
				continue outer
			}
		}
		n++
	}
	return n
}

// Reset clears recorded calls.
func (f *Fake) Reset() {
	f.mu.Lock()
	f.Calls = nil
	f.mu.Unlock()
}
