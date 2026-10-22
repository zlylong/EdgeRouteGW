package update

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"
)

// TestGitHubRateLimitAndETag covers conditional requests (a 304 replays the
// cached body), the rate-limit error with its reset time, and the
// redirect fallback for Latest.
func TestGitHubRateLimitAndETag(t *testing.T) {
	calls := 0
	limited := false
	reset := time.Now().Add(7 * time.Minute).Unix()
	mux := http.NewServeMux()
	mux.HandleFunc("/api/repos/x/y/releases", func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Header.Get("If-None-Match") == `"abc"` {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.Header().Set("ETag", `"abc"`)
		w.Write([]byte(`[{"tag_name":"v2.0.0","assets":[]},{"tag_name":"v1.9.0","prerelease":true,"assets":[]}]`))
	})
	mux.HandleFunc("/api/repos/x/y/releases/latest", func(w http.ResponseWriter, r *http.Request) {
		if limited {
			w.Header().Set("X-RateLimit-Remaining", "0")
			w.Header().Set("X-RateLimit-Reset", strconv.FormatInt(reset, 10))
			w.WriteHeader(http.StatusForbidden)
			return
		}
		w.Write([]byte(`{"tag_name":"v2.0.0","assets":[]}`))
	})
	mux.HandleFunc("/dl/x/y/releases/latest", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/dl/x/y/releases/tag/v2.0.1", http.StatusFound)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	g := &GitHub{APIBase: srv.URL + "/api", DownloadBase: srv.URL + "/dl", Client: srv.Client(), Token: "tok"}
	ctx := context.Background()

	rels, err := g.Releases(ctx, "x/y", 20)
	if err != nil || len(rels) != 2 || rels[0].Tag != "v2.0.0" {
		t.Fatalf("releases: %v %+v", err, rels)
	}
	rels, err = g.Releases(ctx, "x/y", 20) // 304 → cached body
	if err != nil || len(rels) != 2 || !rels[1].Prerelease || calls != 2 {
		t.Fatalf("cached releases: %v %+v calls=%d", err, rels, calls)
	}

	rel, err := g.Latest(ctx, "x/y")
	if err != nil || rel.Tag != "v2.0.0" {
		t.Fatalf("latest: %v %+v", err, rel)
	}
	limited = true
	rel, err = g.Latest(ctx, "x/y") // API exhausted → redirect fallback
	if err != nil || rel.Tag != "v2.0.1" {
		t.Fatalf("latest fallback: %v %+v", err, rel)
	}
	// No fallback for the list: the error names the reset time.
	g.etags = nil
	mux.HandleFunc("/api/repos/x/z/releases", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-RateLimit-Remaining", "0")
		w.Header().Set("X-RateLimit-Reset", strconv.FormatInt(reset, 10))
		w.WriteHeader(http.StatusForbidden)
	})
	_, err = g.Releases(ctx, "x/z", 20)
	var rl *RateLimitError
	if !errors.As(err, &rl) || rl.Reset.Unix() != reset {
		t.Fatalf("expected RateLimitError, got %v", err)
	}
}
