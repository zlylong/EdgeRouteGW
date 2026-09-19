package main

import (
	"context"
	"log"
	"os"
	"os/exec"
	"strings"
	"sync/atomic"
	"time"
)

type commandExecutor struct {
	sem     chan struct{}
	timeout time.Duration
	seq     uint64
}

type commandResult struct {
	Output []byte
	Err    error
}

func newCommandExecutor(maxConcurrent int, timeout time.Duration) *commandExecutor {
	if maxConcurrent <= 0 {
		maxConcurrent = 4
	}
	if timeout <= 0 {
		timeout = 20 * time.Second
	}
	return &commandExecutor{sem: make(chan struct{}, maxConcurrent), timeout: timeout}
}

func (e *commandExecutor) acquire() func() {
	e.sem <- struct{}{}
	return func() { <-e.sem }
}

func (e *commandExecutor) runCombinedOutput(name string, args ...string) commandResult {
	ctx, cancel := context.WithTimeout(context.Background(), e.timeout)
	defer cancel()
	return e.runCombinedOutputCtx(ctx, name, args...)
}

// cmdLogVerbose restores the per-command [start]/[ok] lines. They are off by
// default: the status endpoint alone forks about ten commands per poll, and an
// open UI tab polls every two seconds, which came to ~140k journal lines a day
// on a production gateway and pushed everything else out of the journal.
// Failures and slow commands are always logged, so nothing diagnostic is lost.
// Set PROXYGW_CMD_LOG=1 in the unit's environment to trace every command.
var cmdLogVerbose = os.Getenv("PROXYGW_CMD_LOG") == "1"

// slowCommandThreshold is the duration above which a successful command is
// logged even when cmdLogVerbose is off.
const slowCommandThreshold = 2 * time.Second

// isExpectedProbeFailure reports whether a non-zero exit is the answer rather
// than a failure. "systemctl is-active" exits 3 for a stopped unit, which is
// the normal state of frr outside Mode B/C.
func isExpectedProbeFailure(name string, args []string) bool {
	return name == "systemctl" && len(args) > 0 && args[0] == "is-active"
}

func (e *commandExecutor) runCombinedOutputCtx(ctx context.Context, name string, args ...string) commandResult {
	release := e.acquire()
	defer release()
	id := atomic.AddUint64(&e.seq, 1)
	start := time.Now()
	if cmdLogVerbose {
		log.Printf("[CMD][%d][start] %s %s", id, name, strings.Join(redactSensitiveCommandArgs(args), " "))
	}
	cmd := exec.CommandContext(ctx, name, args...)
	out, err := cmd.CombinedOutput()
	cost := time.Since(start)
	if err != nil {
		if cmdLogVerbose || !isExpectedProbeFailure(name, args) {
			// The command line is repeated here because the [start] line that
			// used to carry it is no longer printed by default.
			log.Printf("[CMD][%d][error] cost=%s err=%v cmd=%s %s out=%s", id, cost, err, name, strings.Join(redactSensitiveCommandArgs(args), " "), strings.TrimSpace(string(out)))
		}
		return commandResult{Output: out, Err: err}
	}
	if cmdLogVerbose {
		log.Printf("[CMD][%d][ok] cost=%s", id, cost)
	} else if cost >= slowCommandThreshold {
		log.Printf("[CMD][%d][slow] cost=%s cmd=%s %s", id, cost, name, strings.Join(redactSensitiveCommandArgs(args), " "))
	}
	return commandResult{Output: out, Err: nil}
}

func (e *commandExecutor) run(name string, args ...string) error {
	res := e.runCombinedOutput(name, args...)
	return res.Err
}

func (e *commandExecutor) output(name string, args ...string) ([]byte, error) {
	res := e.runCombinedOutput(name, args...)
	return res.Output, res.Err
}

func redactSensitiveCommandArgs(args []string) []string {
	out := make([]string, len(args))
	copy(out, args)

	isSensitiveKey := func(key string) bool {
		k := strings.ToLower(strings.TrimSpace(key))
		return strings.Contains(k, "password") || strings.Contains(k, "passwd") || strings.Contains(k, "token") || strings.Contains(k, "secret") || strings.Contains(k, "apikey") || strings.Contains(k, "api_key") || strings.Contains(k, "authorization")
	}

	for i := range out {
		arg := out[i]
		if strings.TrimSpace(arg) == "" {
			continue
		}

		if i > 0 {
			prevRaw := strings.ToLower(strings.TrimSpace(args[i-1]))
			prevKey := strings.TrimLeft(prevRaw, "-")
			if isSensitiveKey(prevKey) {
				out[i] = "[REDACTED]"
				continue
			}
		}

		if eq := strings.Index(arg, "="); eq > 0 {
			key := arg[:eq]
			if isSensitiveKey(key) {
				out[i] = key + "=[REDACTED]"
				continue
			}
		}

		lower := strings.ToLower(arg)
		if strings.Contains(lower, "authorization:") {
			parts := strings.SplitN(arg, ":", 2)
			if len(parts) == 2 {
				out[i] = parts[0] + ": [REDACTED]"
			} else {
				out[i] = "[REDACTED]"
			}
		}
	}

	return out
}

var sysCmd = newCommandExecutor(4, 20*time.Second)
