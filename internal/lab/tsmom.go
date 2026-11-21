package lab

import (
	"context"
	"fmt"
	"io"
	"os"
)

const (
	tsmomData   = "lab/data-tsmom"
	tsmomReport = "lab/results/tsmom-report.txt"
)

const tsmomUsage = `ADR-0026: the trend baseline. Its own data directory, so a download never rewrites a
feather file that a running NFI backtest is reading from lab/data.

Usage:  mg tsmom download     # BTC and ETH daily candles and funding since 2019-12
        mg tsmom test         # the script's self-test
        mg tsmom run          # report and verdict, also saved to lab/results/
`

// TSMOM wraps lab/tsmom.py, ADR-0026's trend baseline (was lab/tsmom.sh).
//
//	mg tsmom download|test|run
func TSMOM(ctx context.Context, args []string) int {
	c, err := defaultConfig()
	if err != nil {
		return report(os.Stderr, "tsmom", err)
	}
	return runTSMOM(ctx, c, arg(args, 0))
}

func runTSMOM(ctx context.Context, c Config, sub string) int {
	if err := mkdirs(c, tsmomData, "lab/results"); err != nil {
		return report(c.Stderr, "tsmom", err)
	}
	switch sub {
	case "download":
		return report(c.Stderr, "tsmom", c.run(ctx, tsmomDownloadArgv(c)))
	case "test":
		return report(c.Stderr, "tsmom", c.run(ctx, tsmomPyArgv(c, "--self-test")))
	case "run":
		return report(c.Stderr, "tsmom", tsmomRun(ctx, c))
	default:
		fmt.Fprint(c.Stderr, tsmomUsage)
		return 2
	}
}

// tsmomRun prints the report and saves it, like `py /data | tee lab/results/tsmom-report.txt`.
func tsmomRun(ctx context.Context, c Config) error {
	f, err := os.Create(c.path(tsmomReport))
	if err != nil {
		return fmt.Errorf("create report: %w", err)
	}
	tee := c
	tee.Stdout = io.MultiWriter(c.Stdout, f)
	runErr := tee.run(ctx, tsmomPyArgv(c, "/data"))
	if err := f.Close(); err != nil && runErr == nil {
		return fmt.Errorf("write report: %w", err)
	}
	return runErr
}

// tsmomPyArgv runs tsmom.py in the freqtrade image. pandas lives in the image's user
// site, so there is no -I (it implies -s); -P and -E keep the working directory and
// PYTHON* variables out of the import path.
func tsmomPyArgv(c Config, args ...string) []string {
	argv := []string{
		"docker", "run", "--rm", "-u", c.user(), "-w", "/tmp",
		"-v", c.mount("lab/tsmom.py") + ":/code/tsmom.py:ro",
		"-v", c.mount(tsmomData) + ":/data:ro",
		"--entrypoint", "python3", c.env("IMAGE", defaultImage), "-P", "-E", "/code/tsmom.py",
	}
	return append(argv, args...)
}

// tsmomDownloadArgv fetches BTC and ETH daily candles. Public endpoints only, no keys
// mounted; futures mode also fetches funding and mark.
func tsmomDownloadArgv(c Config) []string {
	return []string{
		"docker", "run", "--rm", "-u", c.user(),
		"-v", c.mount(tsmomData) + ":/freqtrade/user_data/data",
		c.env("IMAGE", defaultImage), "download-data",
		"--exchange", "binance", "--trading-mode", "futures",
		"--pairs", "BTC/USDT:USDT", "ETH/USDT:USDT",
		"--timeframes", "1d", "--timerange", "20191201-",
	}
}
