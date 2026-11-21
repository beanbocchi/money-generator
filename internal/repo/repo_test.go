package repo

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRootFromEnv(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "lab"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MG_ROOT", dir)
	if got, err := Root("lab"); err != nil || got != dir {
		t.Fatalf("Root = %q, %v", got, err)
	}
	if _, err := Root("missing-marker"); err == nil {
		t.Fatal("missing marker: want error")
	}
}
