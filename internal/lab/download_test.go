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

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

const twoPairs = `{"exchange": {"pair_whitelist": ["BTC/USDT:USDT", "ETH/USDT:USDT"]}}`

// wantDownloadArgv is one dl() call from download.sh with the two test pairs.
func wantDownloadArgv(root, image string, tfs []string, from string) []string {
	argv := []string{
		"docker", "run", "--rm", "-u", "1234:5678",
		"-v", root + "/lab/data:/freqtrade/user_data/data",
		image, "download-data",
		"--exchange", "binance", "--trading-mode", "futures",
		"--pairs", "BTC/USDT:USDT", "ETH/USDT:USDT",
		"--timeframes",
	}
	argv = append(argv, tfs...)
	return append(argv, "--timerange", from)
}

func TestDownloadDefaultsThreePasses(t *testing.T) {
	f := &fakeRunner{}
	c, stdout, _ := testConfig(t, nil, f)
	writeFile(t, filepath.Join(c.Root, "lab/pairlists/live-traded.json"), twoPairs)
	writeFile(t, filepath.Join(c.Root, "lab/data/futures/BTC_USDT_USDT-1d-futures.feather"), "abc")

	if err := runDownload(context.Background(), c); err != nil {
		t.Fatal(err)
	}
	calls := f.Calls()
	if len(calls) != 3 {
		t.Fatalf("%d docker calls, want 3", len(calls))
	}
	img := "freqtradeorg/freqtrade:stable"
	equalArgv(t, calls[0], wantDownloadArgv(c.Root, img, []string{"1d", "4h"}, "20250101-"))
	equalArgv(t, calls[1], wantDownloadArgv(c.Root, img, []string{"1h", "15m", "5m"}, "20260101-"))
	equalArgv(t, calls[2], wantDownloadArgv(c.Root, img, []string{"1m"}, "20260101-"))

	out := stdout.String()
	for _, want := range []string{
		"==> 2 pairs from lab/pairlists/live-traded.json\n",
		"==> downloading [1d 4h] from 20250101-\n",
		"==> downloading [1h 15m 5m] from 20260101-\n",
		"==> downloading [1m] from 20260101-\n",
		"\n==> done. 1 files, 3\n",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
}

func TestDownloadEnvAndSkip1m(t *testing.T) {
	env := map[string]string{"IMAGE": "img:x", "PAIRLIST": "lab/pairlists/343.json",
		"SLOW_FROM": "20240101-", "FAST_FROM": "20250601-", "SKIP_1M": "1"}
	f := &fakeRunner{}
	c, stdout, _ := testConfig(t, env, f)
	writeFile(t, filepath.Join(c.Root, "lab/pairlists/343.json"), twoPairs)

	if err := runDownload(context.Background(), c); err != nil {
		t.Fatal(err)
	}
	calls := f.Calls()
	if len(calls) != 2 {
		t.Fatalf("%d docker calls with SKIP_1M=1, want 2", len(calls))
	}
	equalArgv(t, calls[0], wantDownloadArgv(c.Root, "img:x", []string{"1d", "4h"}, "20240101-"))
	equalArgv(t, calls[1], wantDownloadArgv(c.Root, "img:x", []string{"1h", "15m", "5m"}, "20250601-"))
	if !strings.Contains(stdout.String(), "==> 2 pairs from lab/pairlists/343.json") {
		t.Errorf("output does not name the pairlist:\n%s", stdout)
	}
}

func TestDownloadSkip1mOnlyForExactlyOne(t *testing.T) {
	for _, v := range []string{"", "0", "true", "yes"} {
		c, _, _ := testConfig(t, map[string]string{"SKIP_1M": v}, &fakeRunner{})
		if n := len(downloadPasses(c)); n != 3 {
			t.Errorf("SKIP_1M=%q gave %d passes, want 3", v, n)
		}
	}
}

func TestDownloadMissingPairlistRunsNothing(t *testing.T) {
	f := &fakeRunner{}
	c, _, _ := testConfig(t, nil, f)
	if err := runDownload(context.Background(), c); err == nil {
		t.Fatal("no error for a missing pairlist")
	}
	if len(f.Calls()) != 0 {
		t.Fatal("ran docker without a pairlist")
	}
}

func TestDownloadStopsAtFirstFailure(t *testing.T) {
	boom := errors.New("pull failed")
	f := &fakeRunner{do: func([]string, io.Writer, io.Writer) error { return boom }}
	c, stdout, _ := testConfig(t, nil, f)
	writeFile(t, filepath.Join(c.Root, "lab/pairlists/live-traded.json"), twoPairs)
	if err := runDownload(context.Background(), c); !errors.Is(err, boom) {
		t.Fatalf("err = %v, want %v", err, boom)
	}
	if n := len(f.Calls()); n != 1 {
		t.Fatalf("%d docker calls after a failure, want 1", n)
	}
	if strings.Contains(stdout.String(), "done.") {
		t.Fatal("printed done after a failure")
	}
}
