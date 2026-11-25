package updater

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// interrupted leaves the fixture as an updater killed inside deploy leaves the host:
// the clone on upstream's newest commit, which the guard never passed, and the
// deploying file naming the commit the clone was on before. It returns both commits.
func (f *fixture) interrupted() (old, newSHA string) {
	f.t.Helper()
	old = f.head()
	f.commit("NostalgiaForInfinityX7.py", "class X: pass  # interrupted")
	newSHA = gitT(f.t, f.upstream, "rev-parse", "HEAD")
	gitT(f.t, f.nfi, "fetch", "-q", "origin", "main")
	gitT(f.t, f.nfi, "reset", "--hard", "-q", newSHA)
	writeFile(f.t, f.statePath(deployingFile), old+"\n")
	return old, newSHA
}

func TestInterruptedDeployIsRolledBack(t *testing.T) {
	// The 2026-11-25 case: the updater came back after a reboot, found the clone on the
	// unguarded commit and called it up to date. The next cycle must instead put the
	// clone back and run the guard on the update again, which here fails.
	f := newFixture(t)
	old, _ := f.interrupted()
	f.docker.importFail = true
	out := f.runOnce()
	if f.head() != old {
		t.Error("clone left on the commit the guard never passed")
	}
	if strings.Contains(out, "up to date") {
		t.Errorf("called the unguarded commit up to date:\n%s", out)
	}
	if !strings.Contains(out, "the last deploy did not finish") {
		t.Errorf("no trace of the rollback in the log:\n%s", out)
	}
	if exists(f.statePath(deployingFile)) {
		t.Error("deploying file left behind")
	}
	if f.docker.restarted() {
		t.Error("restarted the bot on a failing guard")
	}
}

func TestInterruptedDeployIsRetried(t *testing.T) {
	f := newFixture(t)
	old, newSHA := f.interrupted()
	out := f.runOnce()
	if !f.docker.restarted() || f.head() != newSHA {
		t.Errorf("update not redeployed after the rollback (head %s):\n%s", short(f.head()), out)
	}
	if got := strings.TrimSpace(readFile(t, f.statePath("previous_sha"))); got != old {
		t.Errorf("previous_sha = %q, want the commit before the interrupted deploy %s", got, old)
	}
	if exists(f.statePath(deployingFile)) {
		t.Error("deploying file left after a finished deploy")
	}
}

func TestUnusableDeployingFileStopsTheCycle(t *testing.T) {
	f := newFixture(t)
	_, newSHA := f.interrupted()
	writeFile(t, f.statePath(deployingFile), "not-a-sha\n")
	u, logs := f.updater()
	if got := u.cycle(context.Background()); got != outError {
		t.Errorf("outcome %s, want error\n%s", got, logs)
	}
	if n := f.git.count("fetch"); n != 0 {
		t.Error("went on to fetch with the tree in an unknown state")
	}
	if f.head() != newSHA || !exists(f.statePath(deployingFile)) {
		t.Error("changed the tree or dropped the file without a commit to go back to")
	}
}

func TestDeployRecordsBeforeMovingTheTree(t *testing.T) {
	f := newFixture(t)
	old := f.head()
	f.commit("NostalgiaForInfinityX7.py", "class X: pass  # record")
	newSHA := gitT(t, f.upstream, "rev-parse", "HEAD")
	var during, headDuring string
	f.docker.onExec = func() {
		during = strings.TrimSpace(readFile(t, f.statePath(deployingFile)))
		headDuring = f.head()
	}
	f.runOnce()
	if headDuring != newSHA {
		t.Fatalf("guard ran with the clone on %s, want %s", short(headDuring), short(newSHA))
	}
	if during != old {
		t.Errorf("deploying file held %q while the guard ran, want %s", during, old)
	}
	if exists(f.statePath(deployingFile)) {
		t.Error("deploying file left after the restart")
	}
}

func TestDeployingFileClearedOnEveryOutcome(t *testing.T) {
	cases := []struct {
		name string
		set  func(*fakeDocker)
		kv   []string
	}{
		{"guard fails", func(d *fakeDocker) { d.importFail = true }, nil},
		{"config guard fails", func(d *fakeDocker) { d.configFail = true }, nil},
		{"dry run", func(*fakeDocker) {}, []string{"DRY_RUN=1"}},
		{"restart fails", func(d *fakeDocker) { d.restartFail = true }, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t)
			tc.set(f.docker)
			f.commit("NostalgiaForInfinityX7.py", "class X: pass  # "+tc.name)
			f.runOnce(tc.kv...)
			if exists(f.statePath(deployingFile)) {
				t.Error("deploying file left behind")
			}
		})
	}
}

func TestHungGuardTimesOut(t *testing.T) {
	f := newFixture(t)
	old := f.head()
	f.commit("NostalgiaForInfinityX7.py", "class X: pass  # hang")
	f.docker.execHang = true
	start := time.Now()
	out := f.runOnce("GUARD_TIMEOUT=1")
	if d := time.Since(start); d > 10*time.Second {
		t.Errorf("cycle took %s with a 1s guard timeout", d)
	}
	if f.head() != old {
		t.Error("clone left on the commit whose guard hung")
	}
	if !strings.Contains(out, "guard did not finish within 1s") {
		t.Errorf("timeout not named in the log:\n%s", out)
	}
	if f.docker.restarted() || exists(f.statePath(deployingFile)) {
		t.Error("restarted, or left the deploying file")
	}
}

func TestRollbackClearsDeployingFile(t *testing.T) {
	nfi, state := rollbackFixture(t)
	first := gitT(t, nfi, "rev-list", "--max-parents=0", "HEAD")
	writeFile(t, filepath.Join(state, "previous_sha"), first+"\n")
	writeFile(t, filepath.Join(state, deployingFile), gitT(t, nfi, "rev-parse", "HEAD")+"\n")
	if rc, out := runRollback(t, nfi, state, &rollbackRunner{}); rc != 0 {
		t.Fatalf("rc %d: %s", rc, out)
	}
	if exists(filepath.Join(state, deployingFile)) {
		t.Error("left the deploying file, so the updater would undo the pin on its next start")
	}
}
