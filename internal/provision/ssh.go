package provision

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"
)

// Target is how to reach a host.
type Target struct {
	Host       string
	Port       int
	User       string
	AuthKind   string // password | key
	Credential string
	// HostKey is the pinned fingerprint; empty means trust-on-first-use and
	// the dialer reports the fingerprint it saw through FirstKey.
	HostKey string
}

// Session runs commands on a host.
type Session interface {
	// Run executes a script, streaming each output line to onLine (stream
	// is "stdout" or "stderr"). It returns the exit error, if any.
	Run(ctx context.Context, script string, timeout time.Duration, onLine func(stream, line string)) error
	Close() error
}

// Dialer opens sessions; the SSH implementation is production, tests use a
// fake.
type Dialer interface {
	Dial(ctx context.Context, t Target) (Session, string, error) // returns session and the host key fingerprint seen
}

// ErrHostKeyMismatch is returned when a pinned fingerprint does not match.
var ErrHostKeyMismatch = errors.New("ssh host key mismatch")

// SSHDialer uses golang.org/x/crypto/ssh.
type SSHDialer struct{ Timeout time.Duration }

// Fingerprint returns the OpenSSH-style SHA256 fingerprint of a key.
func Fingerprint(key ssh.PublicKey) string {
	sum := sha256.Sum256(key.Marshal())
	return "SHA256:" + base64.RawStdEncoding.EncodeToString(sum[:])
}

// Dial implements Dialer.
func (d SSHDialer) Dial(ctx context.Context, t Target) (Session, string, error) {
	var auth ssh.AuthMethod
	switch t.AuthKind {
	case "password":
		auth = ssh.Password(t.Credential)
	case "key":
		signer, err := ssh.ParsePrivateKey([]byte(t.Credential))
		if err != nil {
			return nil, "", fmt.Errorf("parse private key: %w", err)
		}
		auth = ssh.PublicKeys(signer)
	default:
		return nil, "", fmt.Errorf("unsupported auth kind %q", t.AuthKind)
	}
	seen := ""
	cfg := &ssh.ClientConfig{
		User: t.User, Auth: []ssh.AuthMethod{auth},
		HostKeyCallback: func(_ string, _ net.Addr, key ssh.PublicKey) error {
			seen = Fingerprint(key)
			if t.HostKey == "" {
				return nil // first contact: the caller pins what it saw
			}
			if t.HostKey != seen {
				return fmt.Errorf("%w: pinned %s, got %s", ErrHostKeyMismatch, t.HostKey, seen)
			}
			return nil
		},
		Timeout: d.Timeout,
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = 15 * time.Second
	}
	addr := net.JoinHostPort(t.Host, fmt.Sprint(t.Port))
	conn, err := (&net.Dialer{Timeout: cfg.Timeout}).DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, "", err
	}
	c, chans, reqs, err := ssh.NewClientConn(conn, addr, cfg)
	if err != nil {
		conn.Close()
		return nil, seen, err
	}
	return &sshSession{client: ssh.NewClient(c, chans, reqs), sudo: t.User != "root"}, seen, nil
}

type sshSession struct {
	client *ssh.Client
	// sudo wraps every script in `sudo -n` because the login user is not
	// root; deploy, check and tune scripts all need root.
	sudo bool
}

func (s *sshSession) Run(ctx context.Context, script string, timeout time.Duration, onLine func(stream, line string)) error {
	sess, err := s.client.NewSession()
	if err != nil {
		return fmt.Errorf("new session: %w", err)
	}
	defer sess.Close()
	stdout, err := sess.StdoutPipe()
	if err != nil {
		return err
	}
	stderr, err := sess.StderrPipe()
	if err != nil {
		return err
	}
	sess.Stdin = strings.NewReader(script)
	var wg sync.WaitGroup
	pump := func(r io.Reader, stream string) {
		defer wg.Done()
		sc := bufio.NewScanner(r)
		sc.Buffer(make([]byte, 64<<10), 1<<20)
		for sc.Scan() {
			onLine(stream, sc.Text())
		}
	}
	wg.Add(2)
	go pump(stdout, "stdout")
	go pump(stderr, "stderr")
	// Feed the script through stdin so no temp file with secrets lands on
	// the remote filesystem.
	cmd := "bash -s"
	if s.sudo {
		// A non-root login must have passwordless sudo; sudo's own error
		// ("a password is required") lands in the deploy log otherwise.
		cmd = "sudo -n bash -s"
	}
	if err := sess.Start(cmd); err != nil {
		return err
	}
	done := make(chan error, 1)
	go func() { done <- sess.Wait() }()
	if timeout <= 0 {
		timeout = 10 * time.Minute
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case err := <-done:
		wg.Wait()
		return err
	case <-timer.C:
		_ = sess.Signal(ssh.SIGKILL)
		return fmt.Errorf("remote command timed out after %s", timeout)
	case <-ctx.Done():
		_ = sess.Signal(ssh.SIGKILL)
		return ctx.Err()
	}
}

func (s *sshSession) Close() error { return s.client.Close() }

// FakeDialer records scripts and answers from a handler (tests).
type FakeDialer struct {
	Fingerprint string
	DialErr     error
	// Handler receives the script and may emit lines; its error is the
	// command's exit error.
	Handler func(script string, onLine func(stream, line string)) error
	mu      sync.Mutex
	Scripts []string
}

type fakeSession struct {
	d *FakeDialer
}

// Dial implements Dialer.
func (f *FakeDialer) Dial(_ context.Context, t Target) (Session, string, error) {
	if f.DialErr != nil {
		return nil, f.Fingerprint, f.DialErr
	}
	if t.HostKey != "" && t.HostKey != f.Fingerprint {
		return nil, f.Fingerprint, fmt.Errorf("%w: pinned %s, got %s", ErrHostKeyMismatch, t.HostKey, f.Fingerprint)
	}
	return &fakeSession{d: f}, f.Fingerprint, nil
}

func (s *fakeSession) Run(_ context.Context, script string, _ time.Duration, onLine func(stream, line string)) error {
	s.d.mu.Lock()
	s.d.Scripts = append(s.d.Scripts, script)
	h := s.d.Handler
	s.d.mu.Unlock()
	if h == nil {
		onLine("stdout", "ok")
		return nil
	}
	return h(script, onLine)
}

func (s *fakeSession) Close() error { return nil }
