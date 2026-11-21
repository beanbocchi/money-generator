// Package fsx has small file helpers shared by the host tools.
package fsx

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// TailLines returns the last n lines of path, like tail -n, reading backwards so a large
// log is not read whole.
func TailLines(path string, n int) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return "", err
	}
	const chunk = 64 << 10
	size := st.Size()
	var buf []byte
	for off := size; off > 0; {
		step := min(int64(chunk), off)
		off -= step
		b := make([]byte, step)
		if _, err := f.ReadAt(b, off); err != nil && !errors.Is(err, io.EOF) {
			return "", err
		}
		buf = append(b, buf...)
		body := strings.TrimSuffix(string(buf), "\n")
		if strings.Count(body, "\n") >= n {
			break
		}
	}
	s := string(buf)
	trailing := strings.HasSuffix(s, "\n")
	ls := strings.Split(strings.TrimSuffix(s, "\n"), "\n")
	if s == "" {
		ls = nil
	}
	if len(ls) > n {
		ls = ls[len(ls)-n:]
	}
	out := strings.Join(ls, "\n")
	if trailing && out != "" {
		out += "\n"
	}
	return out, nil
}

// DirUsage counts regular files under dir and sums their sizes. Unreadable entries
// are skipped; the result is only printed.
func DirUsage(dir string) (files int, size int64) {
	_ = filepath.WalkDir(dir, func(_ string, d fs.DirEntry, err error) error {
		if err != nil || !d.Type().IsRegular() {
			return nil
		}
		files++
		if fi, err := d.Info(); err == nil {
			size += fi.Size()
		}
		return nil
	})
	return files, size
}

// HumanSize formats bytes in du -h style: 1024-based, one decimal below 10.
func HumanSize(n int64) string {
	const units = "KMGTPE"
	if n < 1024 {
		return fmt.Sprintf("%d", n)
	}
	v := float64(n)
	i := -1
	for v >= 1024 && i < len(units)-1 {
		v /= 1024
		i++
	}
	if v < 10 {
		return fmt.Sprintf("%.1f%c", v, units[i])
	}
	return fmt.Sprintf("%.0f%c", v, units[i])
}
