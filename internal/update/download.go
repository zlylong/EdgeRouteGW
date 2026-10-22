package update

import (
	"archive/zip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Progress reports download progress.
type Progress func(done, total int64)

// Download fetches url into dest, verifying its SHA-256. The file is written
// to a temp name and renamed only after the hash matches; a mismatch leaves
// nothing behind.
func Download(ctx context.Context, client *http.Client, url, dest, sha256hex string, progress Progress) error {
	if len(sha256hex) != 64 {
		return fmt.Errorf("refusing to download %s without a SHA-256 to check", url)
	}
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Minute}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "EdgeRouteGW/2.0")
	res, err := client.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		return fmt.Errorf("download %s: status %d", url, res.StatusCode)
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(dest), ".dl-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if res.ContentLength > maxDownload {
		tmp.Close()
		return fmt.Errorf("%s is %d bytes, larger than the %d MB limit", filepath.Base(url), res.ContentLength, maxDownload>>20)
	}
	h := sha256.New()
	var done int64
	buf := make([]byte, 256<<10)
	body := io.LimitReader(res.Body, maxDownload+1)
	for {
		n, rerr := body.Read(buf)
		if n > 0 {
			if _, err := tmp.Write(buf[:n]); err != nil {
				tmp.Close()
				return err
			}
			h.Write(buf[:n])
			done += int64(n)
			if done > maxDownload {
				tmp.Close()
				return fmt.Errorf("%s exceeds the %d MB download limit", filepath.Base(url), maxDownload>>20)
			}
			if progress != nil {
				progress(done, res.ContentLength)
			}
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			tmp.Close()
			return rerr
		}
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if got := hex.EncodeToString(h.Sum(nil)); !strings.EqualFold(got, sha256hex) {
		return fmt.Errorf("checksum mismatch for %s: want %s, got %s", filepath.Base(url), sha256hex, got)
	}
	return os.Rename(tmpName, dest)
}

// fileSHA256 hashes a file.
func fileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// Unzip extracts the named members of a zip into dir (flat), refusing paths
// that escape it. Returns the extracted paths keyed by member name.
func Unzip(zipPath, dir string, members []string) (map[string]string, error) {
	r, err := zip.OpenReader(zipPath)
	if err != nil {
		return nil, err
	}
	defer r.Close()
	want := map[string]bool{}
	for _, m := range members {
		want[m] = true
	}
	out := map[string]string{}
	for _, f := range r.File {
		name := filepath.Base(f.Name)
		if !want[name] || strings.Contains(f.Name, "..") || f.FileInfo().IsDir() {
			continue
		}
		if f.UncompressedSize64 > 512<<20 {
			return nil, fmt.Errorf("%s too large", name)
		}
		rc, err := f.Open()
		if err != nil {
			return nil, err
		}
		dest := filepath.Join(dir, name)
		w, err := os.OpenFile(dest, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, f.Mode().Perm()|0o600)
		if err != nil {
			rc.Close()
			return nil, err
		}
		if _, err := io.Copy(w, io.LimitReader(rc, 512<<20)); err != nil {
			w.Close()
			rc.Close()
			return nil, err
		}
		w.Close()
		rc.Close()
		out[name] = dest
	}
	for m := range want {
		if _, ok := out[m]; !ok {
			return nil, fmt.Errorf("%s not found in archive", m)
		}
	}
	return out, nil
}

// maxDownload bounds any single release asset (the largest, the .deb with
// bundled components, is well under 200 MB).
const maxDownload = 1 << 30

// SwapFile installs src at dst keeping the previous dst as dst+".prev". The
// new file is fully written next to dst before anything moves, so dst is
// never missing.
func SwapFile(src, dst string, perm os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	tmp := dst + ".new"
	if err := copyFile(src, tmp, perm); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if _, err := os.Stat(dst); err == nil {
		_ = os.Remove(dst + ".prev")
		if err := os.Rename(dst, dst+".prev"); err != nil {
			_ = os.Remove(tmp)
			return err
		}
	}
	if err := os.Rename(tmp, dst); err != nil {
		_ = os.Rename(dst+".prev", dst)
		return err
	}
	return nil
}

// RestorePrev puts dst+".prev" back.
func RestorePrev(dst string) error {
	if _, err := os.Stat(dst + ".prev"); err != nil {
		return fmt.Errorf("no previous version to restore")
	}
	return os.Rename(dst+".prev", dst)
}

func copyFile(src, dst string, perm os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, perm)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		os.Remove(dst)
		return err
	}
	if err := out.Sync(); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}
