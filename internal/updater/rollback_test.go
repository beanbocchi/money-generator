package updater

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/beanbocchi/money-generator/internal/runx"
)

// Cases 1-3 of tests/test_rollback.sh, then checks the shell suite did not have.

// rollbackRunner runs git for real and records docker calls, like the stub in
// tests/test_rollback.sh.
type rollbackRunner struct {
	docker fakeDocker
}

func (r *rollbackRunner) Run(ctx context.Context, name string, args ...string) (string, error) {
	if name == "git" {
		return runx.Exec{}.Run(ctx, name, args...)
	}
	return r.docker.Run(ctx, name, args...)
}

// rollbackFixture is test_rollback.sh's setup(): a repo with commits v1 and v2, under
// a temporary repo root.
func rollbackFixture(t *testing.T) (nfi, state string) {
	d := t.TempDir()
	nfi, state = filepath.Join(d, "nfi"), filepath.Join(d, "state")
	gitT(t, d, "init", "-q", "-b", "main", nfi)
	gitT(t, nfi, "config", "user.email", "t@example.com")
	gitT(t, nfi, "config", "user.name", "test")
	gitT(t, nfi, "config", "commit.gpgsign", "false")
	for _, v := range []string{"v1", "v2"} {
		writeFile(t, filepath.Join(nfi, "f"), v+"\n")
		gitT(t, nfi, "add", "-A")
		gitT(t, nfi, "commit", "-qm", v)
	}
	if err := os.MkdirAll(state, 0o755); err != nil {
		t.Fatal(err)
	}
	return nfi, state
}

// runRollback passes NFI_DIR and STATE_DIR relative to the root, as the bash tool took
// them after cd'ing there.
func runRollback(t *testing.T, nfi, state string, r *rollbackRunner) (int, string) {
	t.Helper()
	root := filepath.Dir(nfi)
	setEnv(t, map[string]string{"NFI_DIR": filepath.Base(nfi), "STATE_DIR": filepath.Base(state)})
	var out, errw strings.Builder
	rc := rollback(context.Background(), root, r, &out, &errw)
	return rc, out.String() + errw.String()
}

func TestRollback(t *testing.T) {
	t.Run("case1 no previous_sha refuses", func(t *testing.T) {
		nfi, state := rollbackFixture(t)
		r := &rollbackRunner{}
		rc, out := runRollback(t, nfi, state, r)
		if rc == 0 {
			t.Error("proceeded with nothing to roll back to")
		}
		if r.docker.log() != "" {
			t.Error("touched docker anyway")
		}
		if !strings.Contains(out, "the updater has not applied an update yet") || !strings.Contains(out, "docker stop bottrade-updater") {
			t.Errorf("no hand-pinning instructions: %s", out)
		}
	})

	t.Run("case2 rolls back, stopping the updater first", func(t *testing.T) {
		nfi, state := rollbackFixture(t)
		first := gitT(t, nfi, "rev-list", "--max-parents=0", "HEAD")
		writeFile(t, filepath.Join(state, "previous_sha"), first+"\n")
		r := &rollbackRunner{}
		rc, out := runRollback(t, nfi, state, r)
		if rc != 0 {
			t.Errorf("exited %d: %s", rc, out)
		}
		if gitT(t, nfi, "rev-parse", "HEAD") != first {
			t.Error("did not pin")
		}
		if readFile(t, filepath.Join(nfi, "f")) != "v1\n" {
			t.Error("working tree not rewound")
		}
		calls := r.docker.calls
		if len(calls) != 2 || calls[0] != "stop bottrade-updater" || calls[1] != "restart bottrade" {
			t.Errorf("docker calls %q: want stop of the updater, then restart of the bot", calls)
		}
		if !strings.Contains(out, "Bot now runs "+first[:8]+". The updater is STOPPED") {
			t.Errorf("no closing message: %s", out)
		}
	})

	t.Run("case3 garbage previous_sha refuses, changes nothing", func(t *testing.T) {
		nfi, state := rollbackFixture(t)
		before := gitT(t, nfi, "rev-parse", "HEAD")
		writeFile(t, filepath.Join(state, "previous_sha"), "not-a-sha\n")
		r := &rollbackRunner{}
		rc, out := runRollback(t, nfi, state, r)
		if rc == 0 {
			t.Error("accepted an invalid previous_sha")
		}
		if r.docker.log() != "" {
			t.Error("stopped the updater before validating")
		}
		if gitT(t, nfi, "rev-parse", "HEAD") != before {
			t.Error("moved the clone")
		}
		if !strings.Contains(out, "does not name a commit") {
			t.Errorf("no reason given: %s", out)
		}
	})

	t.Run("empty previous_sha refuses", func(t *testing.T) {
		nfi, state := rollbackFixture(t)
		writeFile(t, filepath.Join(state, "previous_sha"), "")
		r := &rollbackRunner{}
		if rc, _ := runRollback(t, nfi, state, r); rc == 0 || r.docker.log() != "" {
			t.Error("acted on an empty previous_sha")
		}
	})

	t.Run("updater stop failure does not stop the rollback", func(t *testing.T) {
		nfi, state := rollbackFixture(t)
		first := gitT(t, nfi, "rev-list", "--max-parents=0", "HEAD")
		writeFile(t, filepath.Join(state, "previous_sha"), first+"\n")
		r := &rollbackRunner{}
		r.docker.stopFail = true
		if rc, out := runRollback(t, nfi, state, r); rc != 0 || gitT(t, nfi, "rev-parse", "HEAD") != first {
			t.Errorf("rc %d: %s", rc, out)
		}
	})

	t.Run("bot restart failure exits non-zero", func(t *testing.T) {
		nfi, state := rollbackFixture(t)
		first := gitT(t, nfi, "rev-list", "--max-parents=0", "HEAD")
		writeFile(t, filepath.Join(state, "previous_sha"), first+"\n")
		r := &rollbackRunner{}
		r.docker.restartFail = true
		if rc, _ := runRollback(t, nfi, state, r); rc == 0 {
			t.Error("reported success with the bot not restarted")
		}
	})
}
