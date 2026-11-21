package repo

// Repository-level checks: the ignore rules that keep secrets and bulk out of git, and
// the self-tests of the Python that freqtrade and the lab need. They run with go test
// so that `go test ./...` is the one command that checks everything.

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

const root = "../.."

func TestGitignore(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	ignored := []string{
		".env", "NostalgiaForInfinity/", "backups/db/20261115T025359Z/tradesv3.sqlite",
		"services/updater/state/previous_sha", "services/newsgate/state/log.jsonl",
		"user_data/khoakomlem_binance_futures-tradesv3.sqlite", "user_data/logs/bot.log",
		"user_data/data/", "user_data/nfix7-profit_max-khoakomlem-binance-USDT.json",
		"lab/data/", "lab/data-tsmom/", "lab/strategies-20260501/NostalgiaForInfinityX7.py",
		"bin/mg",
	}
	tracked := []string{
		".env.example", "docker-compose.yml", "Dockerfile", "go.mod", "user_data/config.json",
		"user_data/strategies/NFIX7Risk.py", "internal/updater/cycle.go",
		"lab/strategies-20260501/NFIX7Stop.py", "lab/matrices/h9-stop.tsv",
	}
	check := func(path string) bool {
		return exec.Command("git", "-C", root, "check-ignore", "-q", path).Run() == nil
	}
	for _, p := range ignored {
		if !check(p) {
			t.Errorf("not ignored: %s", p)
		}
	}
	for _, p := range tracked {
		if check(p) {
			t.Errorf("wrongly ignored: %s", p)
		}
	}
}

func python(t *testing.T) string {
	t.Helper()
	py, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 not on PATH")
	}
	return py
}

// NFIX7Risk's overrides (leverage cap, TradFi and delisting refusals) against a stub
// upstream class, so no freqtrade install is needed.
func TestNFIX7Risk(t *testing.T) {
	py := python(t)
	dir := t.TempDir()
	for src, dst := range map[string]string{
		filepath.Join(root, "user_data/strategies/NFIX7Risk.py"): "NFIX7Risk.py",
		"testdata/stub_x7.py": "NostalgiaForInfinityX7.py",
	} {
		b, err := os.ReadFile(src)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, dst), b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	out, err := exec.Command(py, "-I", "testdata/nfix7risk_check.py", dir).CombinedOutput()
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
}

// lab/analyze.py's arithmetic: a bug there would corrupt every lab conclusion.
func TestAnalyzeSelfTest(t *testing.T) {
	out, err := exec.Command(python(t), "-I", filepath.Join(root, "lab/analyze.py"), "--self-test").CombinedOutput()
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
}
