package lab

import (
	"archive/zip"
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
)

// Queue runs a matrix of backtests one at a time, resumably (was lab/queue.sh).
//
//	mg queue lab/matrices/<name>.tsv
//
// Serial on purpose. The machine has 19GB of RAM, and most of its 35GB of swap is
// zram, which is compressed RAM and adds no capacity. One 1m-detail backtest fits
// next to the live bot; two push the bot into swap and stall its trading loop.
//
// Matrix format, one run per line, '#' comments and blank lines ignored:
//
//	NAME<TAB>STRATEGY<TAB>TIMERANGE<TAB>STRATDIR<TAB>CONFIG<TAB>EXTRA<TAB>PAIRLIST
//
// PAIRLIST is optional; blank keeps the backtest default. NAME must be unique; a run
// whose lab/results/NAME.zip exists is skipped, so the queue can be killed and
// restarted without repeating work. INDEX (default lab/results/index.tsv) gets one
// summary row per finished run.
func Queue(ctx context.Context, args []string) int {
	if len(args) == 0 || isHelp(args) {
		fmt.Fprintln(os.Stderr, "usage: mg queue lab/matrices/<name>.tsv")
		return 1
	}
	c, err := defaultConfig()
	if err != nil {
		return report(os.Stderr, "queue", err)
	}
	return runQueue(ctx, c, args[0])
}

// matrixRow is one line of a matrix file.
type matrixRow struct {
	Name, Strategy, Timerange, StratDir, Config, Extra, PairList string
}

// parseMatrix reads the rows of a matrix, skipping blank lines and '#' comments.
//
// Columns are split on every tab. queue.sh used `IFS=$'\t' read`, which merges
// adjacent tabs (an empty EXTRA would shift PAIRLIST into EXTRA) and drops a last
// line that has no newline. No matrix in lab/matrices hits either case.
func parseMatrix(r io.Reader) ([]matrixRow, error) {
	var rows []matrixRow
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	for sc.Scan() {
		f := strings.SplitN(sc.Text(), "\t", 7)
		if f[0] == "" || strings.HasPrefix(f[0], "#") {
			continue
		}
		for len(f) < 7 {
			f = append(f, "")
		}
		rows = append(rows, matrixRow{f[0], f[1], f[2], f[3], f[4], f[5], f[6]})
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("read matrix: %w", err)
	}
	return rows, nil
}

// env is the environment queue.sh gave backtest.sh for a row. STRATDIR, CONFIG and
// EXTRA are always set, so an empty column means the backtest default. PAIRLIST is
// set only when the column is not empty, so an empty one falls back to the caller's
// PAIRLIST and then to the default.
func (r matrixRow) env() map[string]string {
	m := map[string]string{"STRATDIR": r.StratDir, "CONFIG": r.Config, "EXTRA": r.Extra}
	if r.PairList != "" {
		m["PAIRLIST"] = r.PairList
	}
	return m
}

// queueTally counts rows the way queue.sh did. A run that succeeds without leaving a
// zip counts as failed.
type queueTally struct{ total, ran, skipped, failed int }

func runQueue(ctx context.Context, c Config, matrix string) int {
	matrixPath := c.path(matrix)
	if fi, err := os.Stat(matrixPath); err != nil || !fi.Mode().IsRegular() {
		fmt.Fprintf(c.Stdout, "no matrix file: %s\n", matrix)
		return 2
	}
	if err := mkdirs(c, "lab/results", "lab/logs"); err != nil {
		return report(c.Stderr, "queue", err)
	}
	// status.sh tails this log.
	base := filepath.Base(matrix)
	if base != ".tsv" { // basename(1) keeps a name that is only the suffix
		base = strings.TrimSuffix(base, ".tsv")
	}
	lf, err := os.OpenFile(c.path(filepath.Join("lab/logs", "queue-"+base+".log")), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return report(c.Stderr, "queue", fmt.Errorf("open queue log: %w", err))
	}
	defer lf.Close()
	q := queueRun{c: c, out: io.MultiWriter(c.Stdout, lf), matrix: matrixPath, index: c.env("INDEX", "lab/results/index.tsv")}

	t, err := q.run(ctx)
	if err != nil {
		q.log("%v", err)
		if ctx.Err() != nil {
			return 130
		}
		return 1
	}
	q.log("matrix complete: %d entries, %d ran, %d skipped, %d failed", t.total, t.ran, t.skipped, t.failed)
	q.log("summary table: %s", q.index)
	return 0
}

// queueRun is one pass over a matrix. out is stdout plus the queue log; only this
// goroutine writes to it.
type queueRun struct {
	c      Config
	out    io.Writer
	matrix string // absolute
	index  string // as given, relative to the repo root unless absolute
}

func (q queueRun) log(format string, a ...any) {
	fmt.Fprintf(q.out, "[queue %s] %s\n", q.c.Now().UTC().Format("2006-01-02T15:04:05Z"), fmt.Sprintf(format, a...))
}

func (q queueRun) run(ctx context.Context) (queueTally, error) {
	var t queueTally
	index := q.c.path(q.index)
	if err := ensureIndex(index); err != nil {
		return t, err
	}
	f, err := os.Open(q.matrix)
	if err != nil {
		return t, fmt.Errorf("open matrix: %w", err)
	}
	rows, err := parseMatrix(f)
	f.Close()
	if err != nil {
		return t, err
	}
	for _, r := range rows {
		if err := ctx.Err(); err != nil {
			return t, fmt.Errorf("interrupted before %s: %w", r.Name, err)
		}
		t.total++
		zipPath := q.c.path(filepath.Join("lab/results", r.Name+".zip"))
		if _, err := os.Stat(zipPath); err == nil {
			q.log("skip %s (already present)", r.Name)
			t.skipped++
			continue
		}
		q.log("run %s: %s %s", r.Name, r.Strategy, r.Timerange)
		if err := q.backtest(ctx, r); err != nil {
			if ctx.Err() != nil {
				return t, fmt.Errorf("interrupted during %s (see lab/logs/%s.log): %w", r.Name, r.Name, ctx.Err())
			}
			q.log("  FAILED %s (see lab/logs/%s.log)", r.Name, r.Name)
			t.failed++
			continue
		}
		// freqtrade names its result zip by timestamp; give it the run's name.
		newest, err := newestResultZip(q.c.path("lab/results"), q.matrix)
		if err != nil {
			return t, err
		}
		if newest == "" {
			q.log("  WARNING %s produced no zip", r.Name)
			t.failed++
			continue
		}
		if err := os.Rename(newest, zipPath); err != nil {
			return t, fmt.Errorf("claim result zip: %w", err)
		}
		t.ran++
		if err := appendSummary(index, zipPath, r); err != nil {
			// queue.sh let the summary script fail without stopping or counting it.
			q.log("  summary row for %s not written: %v", r.Name, err)
		}
		q.log("  done %s -> lab/results/%s.zip", r.Name, r.Name)
	}
	return t, nil
}

// backtest runs one row with its output in lab/logs/NAME.log.
func (q queueRun) backtest(ctx context.Context, r matrixRow) error {
	lf, err := os.Create(q.c.path(filepath.Join("lab/logs", r.Name+".log")))
	if err != nil {
		return fmt.Errorf("create run log: %w", err)
	}
	defer lf.Close()
	c := q.c.withEnv(r.env())
	c.Stdout, c.Stderr = lf, lf
	err = runBacktest(ctx, c, r.Strategy, r.Timerange)
	if err != nil {
		fmt.Fprintf(lf, "mg backtest: %v\n", err)
	}
	return err
}

// newestResultZip is the newest lab/results/backtest-result-*.zip modified after the
// matrix file, or "" when there is none (queue.sh's find -newer MATRIX).
func newestResultZip(dir, matrix string) (string, error) {
	mfi, err := os.Stat(matrix)
	if err != nil {
		return "", fmt.Errorf("stat matrix: %w", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", fmt.Errorf("list results: %w", err)
	}
	var best string
	var bestMod time.Time
	for _, e := range entries {
		if ok, _ := filepath.Match("backtest-result-*.zip", e.Name()); !ok {
			continue
		}
		fi, err := e.Info()
		if errors.Is(err, fs.ErrNotExist) {
			continue
		} else if err != nil {
			return "", fmt.Errorf("stat %s: %w", e.Name(), err)
		}
		p, mod := filepath.Join(dir, e.Name()), fi.ModTime()
		if mod.After(mfi.ModTime()) && (best == "" || mod.After(bestMod) || mod.Equal(bestMod) && p > best) {
			best, bestMod = p, mod
		}
	}
	return best, nil
}

const indexHeader = "name\ttrades\tlosses\taccount_total\twallet\tstrategy\ttimerange\n"

func ensureIndex(path string) error {
	if _, err := os.Stat(path); err == nil {
		return nil
	} else if !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("stat index: %w", err)
	}
	if err := os.WriteFile(path, []byte(indexHeader), 0o644); err != nil {
		return fmt.Errorf("create index: %w", err)
	}
	return nil
}

// appendSummary adds one row per run, so a sweep can be read without unzipping
// anything.
func appendSummary(index, zipPath string, r matrixRow) error {
	row, err := summaryRow(zipPath, r)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(index, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o644)
	if err != nil {
		return fmt.Errorf("open index: %w", err)
	}
	_, err = f.WriteString(row)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return fmt.Errorf("write index: %w", err)
	}
	return nil
}

var (
	resultJSON = regexp.MustCompile(`\d\.json$`)
	walletArg  = regexp.MustCompile(`--dry-run-wallet\s+(\S+)`)
)

// strategyResult is the part of a strategy block the index reads. A missing count
// prints as None and a missing profit as 0.00, as queue.sh's Python did.
type strategyResult struct {
	TotalTrades    *int64  `json:"total_trades"`
	Losses         *int64  `json:"losses"`
	ProfitTotalAbs float64 `json:"profit_total_abs"`
}

// summaryRow is the index row for the first *N.json in the zip. freqtrade writes one
// strategy block per run.
func summaryRow(zipPath string, r matrixRow) (string, error) {
	zr, err := zip.OpenReader(zipPath)
	if err != nil {
		return "", fmt.Errorf("open %s: %w", zipPath, err)
	}
	defer zr.Close()
	i := slices.IndexFunc(zr.File, func(f *zip.File) bool { return resultJSON.MatchString(f.Name) })
	if i < 0 {
		return "", fmt.Errorf("%s holds no result json", zipPath)
	}
	rc, err := zr.File[i].Open()
	if err != nil {
		return "", fmt.Errorf("open %s in %s: %w", zr.File[i].Name, zipPath, err)
	}
	defer rc.Close()
	var res struct {
		Strategy map[string]strategyResult `json:"strategy"`
	}
	if err := json.NewDecoder(rc).Decode(&res); err != nil {
		return "", fmt.Errorf("parse %s: %w", zr.File[i].Name, err)
	}
	if len(res.Strategy) != 1 {
		return "", fmt.Errorf("%s: %d strategy blocks, want 1", zr.File[i].Name, len(res.Strategy))
	}
	s := slices.Collect(maps.Values(res.Strategy))[0]
	wallet := ""
	if m := walletArg.FindStringSubmatch(r.Extra); m != nil {
		wallet = m[1]
	}
	return fmt.Sprintf("%s\t%s\t%s\t%.2f\t%s\t%s\t%s\n", r.Name, orNone(s.TotalTrades), orNone(s.Losses),
		s.ProfitTotalAbs, wallet, r.Strategy, r.Timerange), nil
}

func orNone(n *int64) string {
	if n == nil {
		return "None"
	}
	return strconv.FormatInt(*n, 10)
}
