package lab

import (
	"bytes"
	"context"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/beanbocchi/money-generator/internal/runx"
)

// fakeRunner records every command instead of running it. do, when set, plays the
// program: it can write output, create files or fail.
type fakeRunner struct {
	mu    sync.Mutex
	calls [][]string
	do    func(argv []string, stdout, stderr io.Writer) error
}

func (f *fakeRunner) factory(stdout, stderr io.Writer) runx.Runner {
	return fakeRun{f: f, stdout: stdout, stderr: stderr}
}

func (f *fakeRunner) Calls() [][]string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([][]string(nil), f.calls...)
}

type fakeRun struct {
	f              *fakeRunner
	stdout, stderr io.Writer
}

func (r fakeRun) Run(_ context.Context, name string, args ...string) (string, error) {
	argv := append([]string{name}, args...)
	r.f.mu.Lock()
	r.f.calls = append(r.f.calls, argv)
	do := r.f.do
	r.f.mu.Unlock()
	if do != nil {
		return "", do(argv, r.stdout, r.stderr)
	}
	return "", nil
}

var testNow = time.Date(2026, 11, 21, 10, 0, 0, 0, time.UTC)

// testConfig is a Config rooted in a temp dir with env as the whole environment.
// UID and GID differ so a swap would show.
func testConfig(t *testing.T, env map[string]string, f *fakeRunner) (Config, *bytes.Buffer, *bytes.Buffer) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	return Config{
		Root:   t.TempDir(),
		Getenv: func(k string) string { return env[k] },
		UID:    1234,
		GID:    5678,
		Stdout: &stdout,
		Stderr: &stderr,
		Runner: f.factory,
		Now:    func() time.Time { return testNow },
	}, &stdout, &stderr
}

func equalArgv(t *testing.T, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("argv has %d words, want %d\n got: %q\nwant: %q", len(got), len(want), got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("argv[%d] = %q, want %q\n got: %q\nwant: %q", i, got[i], want[i], got, want)
		}
	}
}
