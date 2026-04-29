package main

import (
	"context"
	"log"
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

func (e *commandExecutor) runCombinedOutputCtx(ctx context.Context, name string, args ...string) commandResult {
	release := e.acquire()
	defer release()
	id := atomic.AddUint64(&e.seq, 1)
	start := time.Now()
	log.Printf("[CMD][%d][start] %s %s", id, name, strings.Join(args, " "))
	cmd := exec.CommandContext(ctx, name, args...)
	out, err := cmd.CombinedOutput()
	cost := time.Since(start)
	if err != nil {
		log.Printf("[CMD][%d][error] cost=%s err=%v out=%s", id, cost, err, strings.TrimSpace(string(out)))
		return commandResult{Output: out, Err: err}
	}
	log.Printf("[CMD][%d][ok] cost=%s", id, cost)
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

var sysCmd = newCommandExecutor(4, 20*time.Second)
