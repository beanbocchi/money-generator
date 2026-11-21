package lab

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// wantBacktestDefaults is backtest.sh's docker command for root R, uid 1234, gid 5678,
// with no arguments and no environment, written out from the script by hand.
func wantBacktestDefaults(r string) []string {
	return []string{
		"docker", "run", "--rm", "-u", "1234:5678", "--oom-score-adj", "1000",
		"-v", r + "/lab/data:/freqtrade/user_data/data",
		"-v", r + "/lab/results:/freqtrade/user_data/backtest_results",
		"-v", r + "/lab/logs:/freqtrade/user_data/logs",
		"-v", r + "/lab/config-backtest.json:/freqtrade/lab-config.json:ro",
		"-v", r + "/lab/pairlists/live-traded.json:/freqtrade/lab-pairlist.json:ro",
		"-v", r + "/user_data/strategies:/freqtrade/user_data/strategies:ro",
		"-v", r + "/NostalgiaForInfinity:/freqtrade/NostalgiaForInfinity:ro",
		"-v", r + "/lab/configs-pinned:/freqtrade/pinned:ro",
		"-e", "COLUMNS=200",
		"-e", "NFI_MAX_DEPLOYED",
		"freqtradeorg/freqtrade:stable", "backtesting",
		"--strategy", "NFIX7Risk",
		"--timerange", "20260228-20260716",
		"--config", "/freqtrade/pinned/trading_mode-futures.json",
		"--config", "/freqtrade/pinned/exampleconfig.json",
		"--config", "/freqtrade/lab-pairlist.json",
		"--config", "/freqtrade/lab-config.json",
		"--cache", "none", "--breakdown", "day",
		"--export", "signals",
		"--export-filename", "user_data/backtest_results/NFIX7Risk-20260228-20260716.json",
	}
}

func TestBacktestArgvDefaults(t *testing.T) {
	c, _, _ := testConfig(t, nil, &fakeRunner{})
	equalArgv(t, backtestArgv(c, "", ""), wantBacktestDefaults(c.Root))
}

func TestBacktestArgvEmptyEnvMeansDefault(t *testing.T) {
	env := map[string]string{"IMAGE": "", "DATADIR": "", "CONFIG": "", "STRATDIR": "",
		"PAIRLIST": "", "EXTRA": "", "BT_OOM_SCORE_ADJ": ""}
	c, _, _ := testConfig(t, env, &fakeRunner{})
	equalArgv(t, backtestArgv(c, "", ""), wantBacktestDefaults(c.Root))
}

func TestBacktestArgvOverrides(t *testing.T) {
	env := map[string]string{
		"IMAGE":            "freqtradeorg/freqtrade:2026.10",
		"DATADIR":          "lab/data-snap",
		"CONFIG":           "lab/config-h7.json",
		"STRATDIR":         "lab/strategies-20260501",
		"PAIRLIST":         "lab/pairlists/343.json",
		"EXTRA":            "  --timeframe-detail 1m\t--dry-run-wallet 780 --enable-protections ",
		"BT_OOM_SCORE_ADJ": "500",
		"NFI_MAX_DEPLOYED": "0.5", // passed by name only; docker reads the value itself
	}
	c, _, _ := testConfig(t, env, &fakeRunner{})
	r := c.Root
	want := []string{
		"docker", "run", "--rm", "-u", "1234:5678", "--oom-score-adj", "500",
		"-v", r + "/lab/data-snap:/freqtrade/user_data/data",
		"-v", r + "/lab/results:/freqtrade/user_data/backtest_results",
		"-v", r + "/lab/logs:/freqtrade/user_data/logs",
		"-v", r + "/lab/config-h7.json:/freqtrade/lab-config.json:ro",
		"-v", r + "/lab/pairlists/343.json:/freqtrade/lab-pairlist.json:ro",
		"-v", r + "/lab/strategies-20260501:/freqtrade/user_data/strategies:ro",
		"-v", r + "/NostalgiaForInfinity:/freqtrade/NostalgiaForInfinity:ro",
		"-v", r + "/lab/configs-pinned:/freqtrade/pinned:ro",
		"-e", "COLUMNS=200",
		"-e", "NFI_MAX_DEPLOYED",
		"freqtradeorg/freqtrade:2026.10", "backtesting",
		"--strategy", "NFIX7Stop55",
		"--timerange", "20260501-20260701",
		"--config", "/freqtrade/pinned/trading_mode-futures.json",
		"--config", "/freqtrade/pinned/exampleconfig.json",
		"--config", "/freqtrade/lab-pairlist.json",
		"--config", "/freqtrade/lab-config.json",
		"--cache", "none", "--breakdown", "day",
		"--export", "signals",
		"--export-filename", "user_data/backtest_results/NFIX7Stop55-20260501-20260701.json",
		"--timeframe-detail", "1m", "--dry-run-wallet", "780", "--enable-protections",
	}
	equalArgv(t, backtestArgv(c, "NFIX7Stop55", "20260501-20260701"), want)
}

func TestRunBacktestPrintsBannersAndRunsOnce(t *testing.T) {
	f := &fakeRunner{do: func(_ []string, stdout, _ io.Writer) error {
		_, err := io.WriteString(stdout, "freqtrade output\n")
		return err
	}}
	c, stdout, _ := testConfig(t, nil, f)
	if err := runBacktest(context.Background(), c, "S", "T"); err != nil {
		t.Fatal(err)
	}
	want := "==> S over T\nfreqtrade output\n\n==> results: lab/results/S-T.json\n"
	if stdout.String() != want {
		t.Fatalf("output %q, want %q", stdout.String(), want)
	}
	if n := len(f.Calls()); n != 1 {
		t.Fatalf("%d docker calls, want 1", n)
	}
	equalArgv(t, f.Calls()[0], backtestArgv(c, "S", "T"))
	for _, d := range []string{"lab/results", "lab/logs"} {
		if fi, err := os.Stat(filepath.Join(c.Root, d)); err != nil || !fi.IsDir() {
			t.Errorf("%s not created", d)
		}
	}
}

func TestRunBacktestFailureSkipsResultsLine(t *testing.T) {
	boom := errors.New("docker failed")
	c, stdout, _ := testConfig(t, nil, &fakeRunner{do: func([]string, io.Writer, io.Writer) error { return boom }})
	err := runBacktest(context.Background(), c, "", "")
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want %v", err, boom)
	}
	if strings.Contains(stdout.String(), "results:") {
		t.Fatalf("printed a results line after a failure: %q", stdout.String())
	}
}

func TestWithEnvOverlaysWithoutTouchingBase(t *testing.T) {
	c, _, _ := testConfig(t, map[string]string{"PAIRLIST": "base.json", "IMAGE": "img"}, &fakeRunner{})
	o := c.withEnv(map[string]string{"PAIRLIST": "over.json", "STRATDIR": ""})
	if o.Getenv("PAIRLIST") != "over.json" || o.Getenv("IMAGE") != "img" || o.env("STRATDIR", "d") != "d" {
		t.Fatal("overlay not applied")
	}
	if c.Getenv("PAIRLIST") != "base.json" {
		t.Fatal("overlay changed the base config")
	}
}
