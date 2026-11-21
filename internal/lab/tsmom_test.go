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

func wantTsmomPy(root, image string, args ...string) []string {
	return append([]string{
		"docker", "run", "--rm", "-u", "1234:5678", "-w", "/tmp",
		"-v", root + "/lab/tsmom.py:/code/tsmom.py:ro",
		"-v", root + "/lab/data-tsmom:/data:ro",
		"--entrypoint", "python3", image, "-P", "-E", "/code/tsmom.py",
	}, args...)
}

func TestTSMOMDownload(t *testing.T) {
	f := &fakeRunner{}
	c, _, _ := testConfig(t, nil, f)
	if rc := runTSMOM(context.Background(), c, "download"); rc != 0 {
		t.Fatalf("rc = %d", rc)
	}
	want := []string{
		"docker", "run", "--rm", "-u", "1234:5678",
		"-v", c.Root + "/lab/data-tsmom:/freqtrade/user_data/data",
		"freqtradeorg/freqtrade:stable", "download-data",
		"--exchange", "binance", "--trading-mode", "futures",
		"--pairs", "BTC/USDT:USDT", "ETH/USDT:USDT",
		"--timeframes", "1d", "--timerange", "20191201-",
	}
	equalArgv(t, f.Calls()[0], want)
	for _, d := range []string{"lab/data-tsmom", "lab/results"} {
		if _, err := os.Stat(filepath.Join(c.Root, d)); err != nil {
			t.Errorf("%s not created: %v", d, err)
		}
	}
}

func TestTSMOMSelfTest(t *testing.T) {
	f := &fakeRunner{}
	c, _, _ := testConfig(t, nil, f)
	if rc := runTSMOM(context.Background(), c, "test"); rc != 0 {
		t.Fatalf("rc = %d", rc)
	}
	equalArgv(t, f.Calls()[0], wantTsmomPy(c.Root, "freqtradeorg/freqtrade:stable", "--self-test"))
}

func TestTSMOMRunTeesReport(t *testing.T) {
	f := &fakeRunner{do: func(_ []string, stdout, _ io.Writer) error {
		_, err := io.WriteString(stdout, "verdict: ok\n")
		return err
	}}
	c, stdout, _ := testConfig(t, map[string]string{"IMAGE": "img:y"}, f)
	report := filepath.Join(c.Root, "lab/results/tsmom-report.txt")
	writeFile(t, report, "stale report from an earlier run\n")
	if rc := runTSMOM(context.Background(), c, "run"); rc != 0 {
		t.Fatalf("rc = %d", rc)
	}
	equalArgv(t, f.Calls()[0], wantTsmomPy(c.Root, "img:y", "/data"))
	b, err := os.ReadFile(report)
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != "verdict: ok\n" || stdout.String() != "verdict: ok\n" {
		t.Fatalf("report %q, stdout %q; want both %q", b, stdout, "verdict: ok\n")
	}
}

func TestTSMOMFailureIsNonZero(t *testing.T) {
	f := &fakeRunner{do: func([]string, io.Writer, io.Writer) error { return errors.New("no data") }}
	c, _, stderr := testConfig(t, nil, f)
	if rc := runTSMOM(context.Background(), c, "run"); rc == 0 {
		t.Fatal("rc = 0 after a failed run")
	}
	if !strings.Contains(stderr.String(), "no data") {
		t.Fatalf("error not reported: %q", stderr)
	}
}

func TestTSMOMUsage(t *testing.T) {
	for _, sub := range []string{"", "bogus", "--help"} {
		f := &fakeRunner{}
		c, _, stderr := testConfig(t, nil, f)
		if rc := runTSMOM(context.Background(), c, sub); rc != 2 {
			t.Fatalf("%q: rc = %d, want 2", sub, rc)
		}
		if len(f.Calls()) != 0 {
			t.Fatalf("%q: ran docker", sub)
		}
		if !strings.Contains(stderr.String(), "mg tsmom download") {
			t.Fatalf("%q: no usage: %q", sub, stderr)
		}
	}
}
