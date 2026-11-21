package lab

import (
	"archive/zip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// mountOf returns the host side of the -v whose container side is target.
func mountOf(argv []string, target string) string {
	for i := 0; i+1 < len(argv); i++ {
		if argv[i] != "-v" {
			continue
		}
		host, rest, _ := strings.Cut(argv[i+1], ":")
		if rest == target || strings.TrimSuffix(rest, ":ro") == target {
			return host
		}
	}
	return ""
}

func flagValue(argv []string, flag string) string {
	for i := 0; i+1 < len(argv); i++ {
		if argv[i] == flag {
			return argv[i+1]
		}
	}
	return ""
}

// writeResultZip writes the zip freqtrade leaves in lab/results: a timestamped name
// holding a *N.json whose "strategy" object has one block.
func writeResultZip(path string, block map[string]any, strategy string) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	zw := zip.NewWriter(f)
	meta, err := zw.Create("backtest-result-1.meta.json")
	if err != nil {
		return err
	}
	if _, err := io.WriteString(meta, `{}`); err != nil {
		return err
	}
	w, err := zw.Create("backtest-result-1.json")
	if err != nil {
		return err
	}
	if err := json.NewEncoder(w).Encode(map[string]any{"strategy": map[string]any{strategy: block}}); err != nil {
		return err
	}
	if err := zw.Close(); err != nil {
		return err
	}
	return f.Close()
}

// fakeFreqtrade plays a successful backtest: it prints a line and leaves a result zip
// in lab/results, like the stub backtest.sh in tests/test_queue.sh.
func fakeFreqtrade(root string) *fakeRunner {
	var n atomic.Int64
	return &fakeRunner{do: func(argv []string, stdout, _ io.Writer) error {
		fmt.Fprintln(stdout, "fake freqtrade", flagValue(argv, "--strategy"))
		name := fmt.Sprintf("backtest-result-2026-11-21_10-00-%02d.zip", n.Add(1))
		block := map[string]any{"total_trades": 7, "losses": 2, "profit_total_abs": 12.5, "trades": []any{}}
		return writeResultZip(filepath.Join(root, "lab/results", name), block, flagValue(argv, "--strategy"))
	}}
}

// queueFixture is a temp repo with a matrix whose mtime is an hour old, so every zip
// written during the test is newer (test_queue.sh slept 50ms for the same reason).
func queueFixture(t *testing.T, env map[string]string, f func(root string) *fakeRunner, rows ...string) (Config, *fakeRunner, *strings.Builder) {
	t.Helper()
	c, _, _ := testConfig(t, env, &fakeRunner{})
	fr := f(c.Root)
	c.Runner = fr.factory
	var out strings.Builder
	c.Stdout = &out
	writeMatrix(t, c.Root, rows...)
	return c, fr, &out
}

func writeMatrix(t *testing.T, root string, rows ...string) {
	t.Helper()
	p := filepath.Join(root, "lab/matrix.tsv")
	writeFile(t, p, strings.Join(rows, "\n")+"\n")
	old := time.Now().Add(-time.Hour)
	if err := os.Chtimes(p, old, old); err != nil {
		t.Fatal(err)
	}
}

func readLines(t *testing.T, path string) []string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return strings.Split(strings.TrimSuffix(string(b), "\n"), "\n")
}

func exists(path string) bool { _, err := os.Stat(path); return err == nil }

var twoRuns = []string{
	"# a comment line that must be ignored",
	"",
	"runA\tNFIX7Risk\t20260604-20260730\tlab/sd\tlab/cfg\t--dry-run-wallet 780",
	"runB\tNFIX7Risk\t20260604-20260730\tlab/sd\tlab/cfg\t--dry-run-wallet 270",
}

// test_queue.sh case 1.
func TestQueueRunsEveryEntryOnceWithSummary(t *testing.T) {
	c, f, out := queueFixture(t, nil, fakeFreqtrade, twoRuns...)
	if rc := runQueue(context.Background(), c, "lab/matrix.tsv"); rc != 0 {
		t.Fatalf("rc = %d\n%s", rc, out)
	}
	for _, n := range []string{"runA", "runB"} {
		if !exists(filepath.Join(c.Root, "lab/results", n+".zip")) {
			t.Errorf("%s.zip missing", n)
		}
	}
	calls := f.Calls()
	if len(calls) != 2 {
		t.Fatalf("invoked backtest %d times, want 2 (comments and blanks ignored)", len(calls))
	}
	argv := calls[0]
	if mountOf(argv, "/freqtrade/user_data/strategies") != c.Root+"/lab/sd" ||
		mountOf(argv, "/freqtrade/lab-config.json") != c.Root+"/lab/cfg" ||
		flagValue(argv, "--dry-run-wallet") != "780" {
		t.Fatalf("did not pass STRATDIR, CONFIG and EXTRA through: %q", argv)
	}
	if mountOf(argv, "/freqtrade/lab-pairlist.json") != c.Root+"/lab/pairlists/live-traded.json" {
		t.Fatalf("empty PAIRLIST column did not keep the default: %q", argv)
	}
	idx := readLines(t, filepath.Join(c.Root, "lab/results/index.tsv"))
	if len(idx) != 3 {
		t.Fatalf("index has %d lines, want header plus 2:\n%s", len(idx), strings.Join(idx, "\n"))
	}
	if idx[0] != "name\ttrades\tlosses\taccount_total\twallet\tstrategy\ttimerange" {
		t.Errorf("header %q", idx[0])
	}
	if idx[1] != "runA\t7\t2\t12.50\t780\tNFIX7Risk\t20260604-20260730" {
		t.Errorf("summary row %q", idx[1])
	}
	if !strings.HasSuffix(out.String(),
		"[queue 2026-11-21T10:00:00Z] matrix complete: 2 entries, 2 ran, 0 skipped, 0 failed\n"+
			"[queue 2026-11-21T10:00:00Z] summary table: lab/results/index.tsv\n") {
		t.Errorf("final lines wrong:\n%s", out)
	}
	runLog, err := os.ReadFile(filepath.Join(c.Root, "lab/logs/runA.log"))
	if err != nil {
		t.Fatal(err)
	}
	want := "==> NFIX7Risk over 20260604-20260730\nfake freqtrade NFIX7Risk\n\n==> results: lab/results/NFIX7Risk-20260604-20260730.json\n"
	if string(runLog) != want {
		t.Errorf("run log %q, want %q", runLog, want)
	}
}

// test_queue.sh case 1b.
func TestQueuePairlistColumnReachesBacktest(t *testing.T) {
	c, f, out := queueFixture(t, nil, fakeFreqtrade,
		"runP\tNFIX7Risk\t2026\tlab/sd\tlab/cfg\t--wallet 780\tlab/pairlists/343.json")
	if rc := runQueue(context.Background(), c, "lab/matrix.tsv"); rc != 0 {
		t.Fatalf("rc = %d\n%s", rc, out)
	}
	if got := mountOf(f.Calls()[0], "/freqtrade/lab-pairlist.json"); got != c.Root+"/lab/pairlists/343.json" {
		t.Fatalf("PAIRLIST lost: mounted %q", got)
	}
	if !exists(filepath.Join(c.Root, "lab/results/runP.zip")) {
		t.Fatal("run did not complete")
	}
	idx := readLines(t, filepath.Join(c.Root, "lab/results/index.tsv"))
	if idx[1] != "runP\t7\t2\t12.50\t\tNFIX7Risk\t2026" {
		t.Errorf("summary row without --dry-run-wallet %q", idx[1])
	}
}

func TestQueueEmptyPairlistColumnKeepsCallersPairlist(t *testing.T) {
	c, f, _ := queueFixture(t, map[string]string{"PAIRLIST": "lab/pairlists/disjoint.json"}, fakeFreqtrade,
		"r1\tS\tT\t\t\t", "r2\tS\tT\tsd\tcfg\t\tlab/pairlists/343.json")
	runQueue(context.Background(), c, "lab/matrix.tsv")
	calls := f.Calls()
	if got := mountOf(calls[0], "/freqtrade/lab-pairlist.json"); got != c.Root+"/lab/pairlists/disjoint.json" {
		t.Errorf("empty column: mounted %q, want the caller's PAIRLIST", got)
	}
	// Empty STRATDIR and CONFIG columns fall back to backtest defaults.
	if got := mountOf(calls[0], "/freqtrade/user_data/strategies"); got != c.Root+"/user_data/strategies" {
		t.Errorf("empty STRATDIR column mounted %q", got)
	}
	if got := mountOf(calls[0], "/freqtrade/lab-config.json"); got != c.Root+"/lab/config-backtest.json" {
		t.Errorf("empty CONFIG column mounted %q", got)
	}
	// An empty EXTRA column must not shift PAIRLIST into EXTRA.
	if got := mountOf(calls[1], "/freqtrade/lab-pairlist.json"); got != c.Root+"/lab/pairlists/343.json" {
		t.Errorf("row with empty EXTRA: mounted %q", got)
	}
}

// test_queue.sh case 2.
func TestQueueRerunSkipsCompletedWork(t *testing.T) {
	c, f, _ := queueFixture(t, nil, fakeFreqtrade, twoRuns...)
	runQueue(context.Background(), c, "lab/matrix.tsv")
	var out strings.Builder
	c.Stdout = &out
	if rc := runQueue(context.Background(), c, "lab/matrix.tsv"); rc != 0 {
		t.Fatalf("rc = %d", rc)
	}
	if n := len(f.Calls()); n != 2 {
		t.Fatalf("re-ran completed entries (%d calls total)", n)
	}
	if !strings.Contains(out.String(), "skip runA (already present)") ||
		!strings.Contains(out.String(), "2 entries, 0 ran, 2 skipped, 0 failed") {
		t.Fatalf("skips not reported:\n%s", out.String())
	}
	if n := len(readLines(t, filepath.Join(c.Root, "lab/results/index.tsv"))); n != 3 {
		t.Fatalf("index has %d lines after a rerun, want 3", n)
	}
	// The queue log is appended to, as tee -a did.
	log := readLines(t, filepath.Join(c.Root, "lab/logs/queue-matrix.log"))
	complete := 0
	for _, l := range log {
		if strings.Contains(l, "matrix complete") {
			complete++
		}
	}
	if complete != 2 {
		t.Fatalf("queue log holds %d final lines, want 2:\n%s", complete, strings.Join(log, "\n"))
	}
}

// test_queue.sh case 3.
func TestQueueFailingRunIsReported(t *testing.T) {
	fail := func(string) *fakeRunner {
		return &fakeRunner{do: func(_ []string, _, stderr io.Writer) error {
			fmt.Fprintln(stderr, "Traceback: out of memory")
			return errors.New("exit status 1")
		}}
	}
	c, _, out := queueFixture(t, nil, fail, "boom\tS\t2026\tsd\tcfg\t")
	if rc := runQueue(context.Background(), c, "lab/matrix.tsv"); rc != 0 {
		t.Fatalf("rc = %d, want 0 as queue.sh", rc)
	}
	if !strings.Contains(out.String(), "  FAILED boom (see lab/logs/boom.log)") {
		t.Errorf("failure not reported:\n%s", out)
	}
	if !strings.Contains(out.String(), "1 entries, 0 ran, 0 skipped, 1 failed") {
		t.Errorf("not counted as failed:\n%s", out)
	}
	if exists(filepath.Join(c.Root, "lab/results/boom.zip")) {
		t.Error("left a zip for a failed run")
	}
	b, err := os.ReadFile(filepath.Join(c.Root, "lab/logs/boom.log"))
	if err != nil {
		t.Fatal("discarded the run log")
	}
	if !strings.Contains(string(b), "Traceback: out of memory") || !strings.Contains(string(b), "==> S over 2026") {
		t.Errorf("run log lacks the run's output: %q", b)
	}
}

func TestQueueSuccessWithoutZipCountsAsFailed(t *testing.T) {
	c, _, out := queueFixture(t, nil, func(string) *fakeRunner { return &fakeRunner{} }, "nozip\tS\tT\tsd\tcfg\t")
	runQueue(context.Background(), c, "lab/matrix.tsv")
	if !strings.Contains(out.String(), "  WARNING nozip produced no zip") ||
		!strings.Contains(out.String(), "1 entries, 0 ran, 0 skipped, 1 failed") {
		t.Fatalf("missing zip not counted:\n%s", out)
	}
}

func TestQueueIgnoresZipsOlderThanMatrix(t *testing.T) {
	c, _, out := queueFixture(t, nil, func(string) *fakeRunner { return &fakeRunner{} }, "r\tS\tT\tsd\tcfg\t")
	stale := filepath.Join(c.Root, "lab/results/backtest-result-old.zip")
	writeFile(t, stale, "x")
	old := time.Now().Add(-2 * time.Hour)
	if err := os.Chtimes(stale, old, old); err != nil {
		t.Fatal(err)
	}
	runQueue(context.Background(), c, "lab/matrix.tsv")
	if !exists(stale) || exists(filepath.Join(c.Root, "lab/results/r.zip")) {
		t.Fatal("claimed a zip older than the matrix")
	}
	if !strings.Contains(out.String(), "WARNING r produced no zip") {
		t.Fatalf("no warning:\n%s", out)
	}
}

func TestNewestResultZipPicksLatest(t *testing.T) {
	dir := t.TempDir()
	matrix := filepath.Join(dir, "m.tsv")
	writeFile(t, matrix, "")
	base := time.Now().Add(-time.Hour)
	for i, name := range []string{"backtest-result-a.zip", "backtest-result-c.zip", "backtest-result-b.zip", "other.zip"} {
		p := filepath.Join(dir, name)
		writeFile(t, p, "")
		mt := base.Add(time.Duration(i+1) * time.Minute)
		if err := os.Chtimes(p, mt, mt); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Chtimes(matrix, base, base); err != nil {
		t.Fatal(err)
	}
	got, err := newestResultZip(dir, matrix)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(got) != "backtest-result-b.zip" {
		t.Fatalf("picked %s, want the newest backtest-result zip", got)
	}
}

// test_queue.sh case 4.
func TestQueueMissingMatrixIsAnError(t *testing.T) {
	f := &fakeRunner{}
	c, _, _ := testConfig(t, nil, f)
	var out strings.Builder
	c.Stdout = &out
	if rc := runQueue(context.Background(), c, "lab/nope.tsv"); rc == 0 {
		t.Fatal("exited 0 with no matrix")
	}
	if out.String() != "no matrix file: lab/nope.tsv\n" {
		t.Fatalf("output %q", out.String())
	}
	if len(f.Calls()) != 0 || exists(filepath.Join(c.Root, "lab/logs")) {
		t.Fatal("did work without a matrix")
	}
}

func TestQueueNoArgsIsUsage(t *testing.T) {
	if rc := Queue(context.Background(), nil); rc == 0 {
		t.Fatal("exited 0 without a matrix argument")
	}
}

func TestQueueStopsWhenInterrupted(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	interrupt := func(string) *fakeRunner {
		return &fakeRunner{do: func([]string, io.Writer, io.Writer) error {
			cancel()
			return context.Canceled
		}}
	}
	c, f, out := queueFixture(t, nil, interrupt, twoRuns...)
	if rc := runQueue(ctx, c, "lab/matrix.tsv"); rc != 130 {
		t.Fatalf("rc = %d, want 130", rc)
	}
	if n := len(f.Calls()); n != 1 {
		t.Fatalf("%d runs after the interrupt, want 1", n)
	}
	if strings.Contains(out.String(), "matrix complete") || strings.Contains(out.String(), "FAILED") {
		t.Fatalf("an interrupted queue reported completion or a failure:\n%s", out)
	}
}

func TestQueueSummaryFailureStillCountsTheRun(t *testing.T) {
	badZip := func(root string) *fakeRunner {
		return &fakeRunner{do: func([]string, io.Writer, io.Writer) error {
			return os.WriteFile(filepath.Join(root, "lab/results/backtest-result-x.zip"), []byte("not a zip"), 0o644)
		}}
	}
	c, _, out := queueFixture(t, nil, badZip, "r\tS\tT\tsd\tcfg\t")
	runQueue(context.Background(), c, "lab/matrix.tsv")
	if !exists(filepath.Join(c.Root, "lab/results/r.zip")) ||
		!strings.Contains(out.String(), "1 entries, 1 ran, 0 skipped, 0 failed") ||
		!strings.Contains(out.String(), "summary row for r not written") {
		t.Fatalf("summary failure handled wrong:\n%s", out)
	}
	if n := len(readLines(t, filepath.Join(c.Root, "lab/results/index.tsv"))); n != 1 {
		t.Fatalf("index has %d lines, want only the header", n)
	}
}

func TestQueueIndexEnvAndExistingIndexKept(t *testing.T) {
	c, _, out := queueFixture(t, map[string]string{"INDEX": "lab/results/sweep.tsv"}, fakeFreqtrade, "r\tS\tT\tsd\tcfg\t")
	writeFile(t, filepath.Join(c.Root, "lab/results/sweep.tsv"), "old header\n")
	runQueue(context.Background(), c, "lab/matrix.tsv")
	lines := readLines(t, filepath.Join(c.Root, "lab/results/sweep.tsv"))
	if len(lines) != 2 || lines[0] != "old header" || !strings.HasPrefix(lines[1], "r\t7\t2\t12.50\t") {
		t.Fatalf("index %q", lines)
	}
	if !strings.Contains(out.String(), "summary table: lab/results/sweep.tsv") {
		t.Fatalf("summary table line:\n%s", out)
	}
}

func TestParseMatrix(t *testing.T) {
	in := "# c\n\nn1\tS\tT\tsd\tcfg\t\tpl.json\nn2\tS\tT\n  \n#x\tS\nn3\tS\tT\tsd\tcfg\t--x 1 \tpl\textra\tcols"
	rows, err := parseMatrix(strings.NewReader(in))
	if err != nil {
		t.Fatal(err)
	}
	want := []matrixRow{
		{"n1", "S", "T", "sd", "cfg", "", "pl.json"},
		{"n2", "S", "T", "", "", "", ""},
		{"  ", "", "", "", "", "", ""},
		{"n3", "S", "T", "sd", "cfg", "--x 1 ", "pl\textra\tcols"},
	}
	if len(rows) != len(want) {
		t.Fatalf("got %d rows %q, want %d", len(rows), rows, len(want))
	}
	for i := range want {
		if rows[i] != want[i] {
			t.Errorf("row %d = %q, want %q", i, rows[i], want[i])
		}
	}
}

func TestSummaryRowPythonFormatting(t *testing.T) {
	dir := t.TempDir()
	for _, tc := range []struct {
		block map[string]any
		want  string
	}{
		{map[string]any{"total_trades": 386, "losses": 1, "profit_total_abs": -131.215}, "386\t1\t-131.22"}, // Python prints -131.22 as well
		{map[string]any{"total_trades": 0}, "0\tNone\t0.00"},
		{map[string]any{"total_trades": 3, "losses": 0, "profit_total_abs": 2.005}, "3\t0\t2.00"},
		{map[string]any{"total_trades": 1, "losses": 0, "profit_total_abs": 0.125}, "1\t0\t0.12"},
	} {
		p := filepath.Join(dir, "r.zip")
		if err := writeResultZip(p, tc.block, "S"); err != nil {
			t.Fatal(err)
		}
		row, err := summaryRow(p, matrixRow{Name: "n", Strategy: "S", Timerange: "T", Extra: "--dry-run-wallet  270 --x"})
		if err != nil {
			t.Fatal(err)
		}
		if want := "n\t" + tc.want + "\t270\tS\tT\n"; row != want {
			t.Errorf("row %q, want %q", row, want)
		}
	}
}
