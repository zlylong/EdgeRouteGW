// Package httpserver wraps net/http.Server with the timeouts and graceful
// shutdown every EdgeRouteGW listener uses.
package httpserver

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"time"
)

const (
	readHeaderTimeout = 10 * time.Second
	idleTimeout       = 120 * time.Second
	maxHeaderBytes    = 64 << 10
	shutdownGrace     = 10 * time.Second
)

// Serve listens on addr and serves h until ctx is cancelled, then shuts down
// gracefully. It returns nil on a clean shutdown. ready, if non-nil, is called
// once the listener is bound, with the actual address (useful for :0 in tests).
func Serve(ctx context.Context, addr string, h http.Handler, log *slog.Logger, ready func(net.Addr)) error {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	srv := &http.Server{
		Handler:           h,
		ReadHeaderTimeout: readHeaderTimeout,
		IdleTimeout:       idleTimeout,
		MaxHeaderBytes:    maxHeaderBytes,
		BaseContext:       func(net.Listener) context.Context { return ctx },
	}
	if ready != nil {
		ready(ln.Addr())
	}
	log.Info("http server listening", "addr", ln.Addr().String())

	errCh := make(chan error, 1)
	go func() { errCh <- srv.Serve(ln) }()

	select {
	case err := <-errCh:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownGrace)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Warn("http server shutdown incomplete, closing", "err", err)
		_ = srv.Close()
	}
	<-errCh
	log.Info("http server stopped")
	return nil
}
