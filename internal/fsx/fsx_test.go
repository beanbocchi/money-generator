package fsx

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestTailLines(t *testing.T) {
	dir := t.TempDir()
	write := func(name, body string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	big := strings.Repeat("x", 70<<10) + "\n" // spans more than one 64KB chunk
	tests := []struct {
		name, body string
		n          int
		want       string
	}{
		{"fewer lines than n", "a\nb\n", 5, "a\nb\n"},
		{"last n", "a\nb\nc\nd\n", 2, "c\nd\n"},
		{"no trailing newline", "a\nb\nc", 2, "b\nc"},
		{"empty", "", 3, ""},
		{"across chunks", big + "y\nz\n", 2, "y\nz\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := TailLines(write(tt.name, tt.body), tt.n)
			if err != nil || got != tt.want {
				t.Fatalf("got %q, %v; want %q", got, err, tt.want)
			}
		})
	}
	if _, err := TailLines(filepath.Join(dir, "missing"), 1); err == nil {
		t.Fatal("missing file: want error")
	}
}

func TestDirUsageAndHumanSize(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	for p, n := range map[string]int{"a": 100, "sub/b": 2000} {
		if err := os.WriteFile(filepath.Join(dir, p), make([]byte, n), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if files, size := DirUsage(dir); files != 2 || size != 2100 {
		t.Fatalf("DirUsage = %d files, %d bytes", files, size)
	}
	for in, want := range map[int64]string{512: "512", 2100: "2.1K", 50 << 20: "50M"} {
		if got := HumanSize(in); got != want {
			t.Errorf("HumanSize(%d) = %q, want %q", in, got, want)
		}
	}
}
