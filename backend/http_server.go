package main

import (
	"compress/gzip"
	"context"
	"errors"
	"io"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/gin-gonic/gin"
)

// newHTTPServer wraps the router in an http.Server with header and idle
// timeouts. gin's Run used http.ListenAndServe, which has none: a client that
// opened a connection and never sent a request line held a goroutine forever.
// There is deliberately no WriteTimeout: component updates and remote deploys
// legitimately take minutes, and the executor already bounds child processes.
func newHTTPServer(addr string, h http.Handler) *http.Server {
	return &http.Server{
		Addr:              addr,
		Handler:           h,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
		MaxHeaderBytes:    1 << 20,
	}
}

// shutdownGrace bounds how long in-flight requests may finish after SIGTERM.
const shutdownGrace = 15 * time.Second

// serveWithGracefulShutdown runs srv until SIGTERM/SIGINT, then drains
// in-flight requests before returning so a systemctl restart does not cut a
// response (or a config apply) in half.
func serveWithGracefulShutdown(srv *http.Server) error {
	errCh := make(chan error, 1)
	go func() {
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
		close(errCh)
	}()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)
	defer signal.Stop(sigCh)

	select {
	case err := <-errCh:
		return err
	case sig := <-sigCh:
		log.Printf("received %s, shutting down HTTP server (grace %s)", sig, shutdownGrace)
		ctx, cancel := context.WithTimeout(context.Background(), shutdownGrace)
		defer cancel()
		if err := srv.Shutdown(ctx); err != nil {
			log.Printf("[WARN] HTTP shutdown: %v", err)
			return err
		}
		return nil
	}
}

// securityHeadersMiddleware sets the response headers every page and API
// answer should carry. The UI is a single page served from the same origin
// with an inline application script, so an enforced CSP would need a
// hash/nonce scheme; it is published report-only for now.
func securityHeadersMiddleware() gin.HandlerFunc {
	const csp = "default-src 'self'; script-src 'self' 'unsafe-inline'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; font-src 'self'; connect-src 'self'; frame-ancestors 'none'; base-uri 'self'; form-action 'self'"
	return func(c *gin.Context) {
		h := c.Writer.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "same-origin")
		h.Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
		h.Set("Content-Security-Policy-Report-Only", csp)
		c.Next()
	}
}

// staticCacheMiddleware sets caching for the UI. index.html must always be
// revalidated so a release is picked up immediately; the libraries under
// /ui/libs are large (Vue, the CSS bundle, Font Awesome) and only change with
// a release, so they may be cached and revalidated with If-Modified-Since,
// which http.FileServer answers with 304.
func staticCacheMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		p := c.Request.URL.Path
		if !strings.HasPrefix(p, "/ui") && p != "/favicon.ico" {
			c.Next()
			return
		}
		h := c.Writer.Header()
		switch {
		case p == "/ui" || p == "/ui/" || strings.HasSuffix(p, "/index.html"):
			h.Set("Cache-Control", "no-cache, no-store, must-revalidate")
			h.Set("Pragma", "no-cache")
			h.Set("Expires", "0")
		default:
			h.Set("Cache-Control", "public, max-age=3600")
		}
		c.Next()
	}
}

// --- gzip ------------------------------------------------------------------

// gzipMinSize is the smallest Content-Length worth compressing when the
// handler declares one (static files); JSON bodies from gin carry no length
// and are compressed regardless.
const gzipMinSize = 1024

var gzipWriterPool = sync.Pool{New: func() interface{} {
	w, _ := gzip.NewWriterLevel(io.Discard, gzip.BestSpeed)
	return w
}}

func gzipCompressibleContentType(ct string) bool {
	ct = strings.ToLower(ct)
	switch {
	case strings.HasPrefix(ct, "text/"),
		strings.HasPrefix(ct, "application/json"),
		strings.HasPrefix(ct, "application/javascript"),
		strings.HasPrefix(ct, "application/x-javascript"),
		strings.HasPrefix(ct, "image/svg+xml"):
		return true
	}
	return false
}

type gzipResponseWriter struct {
	gin.ResponseWriter
	gz       *gzip.Writer
	decided  bool
	compress bool
}

// decide looks at the headers the handler set before its first body write
// and switches the writer into gzip mode when the payload is compressible.
func (w *gzipResponseWriter) decide() {
	if w.decided {
		return
	}
	w.decided = true
	h := w.Header()
	if h.Get("Content-Encoding") != "" || !gzipCompressibleContentType(h.Get("Content-Type")) {
		return
	}
	if cl := h.Get("Content-Length"); cl != "" {
		if n, err := strconv.Atoi(cl); err == nil && n < gzipMinSize {
			return
		}
		h.Del("Content-Length")
	}
	h.Set("Content-Encoding", "gzip")
	h.Add("Vary", "Accept-Encoding")
	w.gz = gzipWriterPool.Get().(*gzip.Writer)
	w.gz.Reset(w.ResponseWriter)
	w.compress = true
}

func (w *gzipResponseWriter) Write(b []byte) (int, error) {
	w.decide()
	if w.compress {
		return w.gz.Write(b)
	}
	return w.ResponseWriter.Write(b)
}

func (w *gzipResponseWriter) WriteString(s string) (int, error) {
	return w.Write([]byte(s))
}

func (w *gzipResponseWriter) Flush() {
	if w.gz != nil {
		_ = w.gz.Flush()
	}
	w.ResponseWriter.Flush()
}

func (w *gzipResponseWriter) finish() {
	if w.gz == nil {
		return
	}
	_ = w.gz.Close()
	gzipWriterPool.Put(w.gz)
	w.gz = nil
}

// gzipMiddleware compresses JSON and static text responses for clients that
// accept it. It is dependency-free on purpose; the UI ships about a megabyte
// of JavaScript and CSS that compresses roughly 4:1, and the rules and
// connection lists are repetitive JSON.
func gzipMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		req := c.Request
		if !strings.Contains(req.Header.Get("Accept-Encoding"), "gzip") ||
			req.Header.Get("Range") != "" ||
			strings.EqualFold(req.Header.Get("Upgrade"), "websocket") {
			c.Next()
			return
		}
		gw := &gzipResponseWriter{ResponseWriter: c.Writer}
		c.Writer = gw
		defer gw.finish()
		c.Next()
	}
}
