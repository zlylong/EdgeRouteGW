package driver

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// WriteFileAtomic writes data to path via a temp file + rename so a crash
// never leaves a half-written config for the component to load. It reports
// whether the content changed.
func WriteFileAtomic(path string, data []byte, perm os.FileMode) (changed bool, err error) {
	if existing, err := os.ReadFile(path); err == nil && bytes.Equal(existing, data) {
		// Still enforce permissions: an operator may have loosened them.
		_ = os.Chmod(path, perm)
		return false, nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return false, err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*")
	if err != nil {
		return false, err
	}
	tmpName := tmp.Name()
	defer func() {
		if err != nil {
			_ = os.Remove(tmpName)
		}
	}()
	if _, err = tmp.Write(data); err != nil {
		_ = tmp.Close()
		return false, err
	}
	if err = tmp.Chmod(perm); err != nil {
		_ = tmp.Close()
		return false, err
	}
	if err = tmp.Close(); err != nil {
		return false, err
	}
	if err = os.Rename(tmpName, path); err != nil {
		return false, err
	}
	return true, nil
}

// SameContent reports whether path holds exactly data.
func SameContent(path string, data []byte) bool {
	existing, err := os.ReadFile(path)
	return err == nil && bytes.Equal(existing, data)
}

// LineDiff is a small unified-ish diff good enough for a change preview: it
// lists removed and added lines, capped so huge domain lists do not flood the
// UI.
func LineDiff(before, after []byte, maxLines int) string {
	if bytes.Equal(before, after) {
		return ""
	}
	a := strings.Split(strings.TrimRight(string(before), "\n"), "\n")
	b := strings.Split(strings.TrimRight(string(after), "\n"), "\n")
	inA := map[string]int{}
	for _, l := range a {
		inA[l]++
	}
	inB := map[string]int{}
	for _, l := range b {
		inB[l]++
	}
	var out []string
	emit := func(prefix string, lines []string, other map[string]int) {
		for _, l := range lines {
			if other[l] > 0 {
				other[l]--
				continue
			}
			out = append(out, prefix+l)
		}
	}
	emit("- ", a, copyCounts(inB))
	emit("+ ", b, copyCounts(inA))
	if maxLines > 0 && len(out) > maxLines {
		out = append(out[:maxLines], fmt.Sprintf("… %d more lines", len(out)-maxLines))
	}
	if len(out) == 0 {
		return "(reordered)"
	}
	return strings.Join(out, "\n")
}

func copyCounts(m map[string]int) map[string]int {
	c := make(map[string]int, len(m))
	for k, v := range m {
		c[k] = v
	}
	return c
}
