package lab

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/beanbocchi/money-generator/internal/fsx"
)

// Download fetches Binance futures candles into lab/data (was lab/download.sh).
//
//	mg download
//
// Environment: IMAGE, PAIRLIST (default lab/pairlists/live-traded.json), SLOW_FROM
// (1d and 4h, default 20250101-), FAST_FROM (1h, 15m, 5m and 1m, default 20260101-),
// SKIP_1M=1 to leave out 1m.
//
// The data goes to lab/data. user_data/data is bind-mounted into the live trading
// container.
//
// The two lead-ins exist because NFI's 1d informative carries long moving averages and
// needs about 200 days before the backtest start, while 5m needs only
// startup_candle_count (800 candles, about 2.8 days).
//
// freqtrade does not backfill earlier data into an existing file. A pair first
// downloaded with a later --timerange is only topped up forward, and the only sign is
// an INFO line suggesting --prepend. Check with `freqtrade list-data --show-timerange`
// after a partial download and repair those pairs with --erase or --prepend.
func Download(ctx context.Context, args []string) int {
	if isHelp(args) {
		fmt.Fprintln(os.Stderr, "usage: mg download   (PAIRLIST, SLOW_FROM, FAST_FROM, SKIP_1M, IMAGE from the environment)")
		return 2
	}
	c, err := defaultConfig()
	if err != nil {
		return report(os.Stderr, "download", err)
	}
	return report(c.Stderr, "download", runDownload(ctx, c))
}

// downloadPass is one docker run: a set of timeframes from one start date.
type downloadPass struct {
	Timeframes string // space-separated, as download.sh passed them
	From       string
}

func downloadPasses(c Config) []downloadPass {
	fast := c.env("FAST_FROM", "20260101-")
	passes := []downloadPass{
		{"1d 4h", c.env("SLOW_FROM", "20250101-")},
		{"1h 15m 5m", fast},
	}
	// 1m feeds --timeframe-detail 1m, which every matrix uses. It is about five
	// times the volume of 5m.
	if c.env("SKIP_1M", "0") != "1" {
		passes = append(passes, downloadPass{"1m", fast})
	}
	return passes
}

func runDownload(ctx context.Context, c Config) error {
	pairlist := c.env("PAIRLIST", "lab/pairlists/live-traded.json")
	pairs, err := readPairs(c.path(pairlist))
	if err != nil {
		return err
	}
	fmt.Fprintf(c.Stdout, "==> %d pairs from %s\n", len(pairs), pairlist)
	for _, p := range downloadPasses(c) {
		fmt.Fprintf(c.Stdout, "==> downloading [%s] from %s\n", p.Timeframes, p.From)
		if err := c.run(ctx, downloadArgv(c, pairs, p)); err != nil {
			return err
		}
	}
	files, size := fsx.DirUsage(c.path("lab/data"))
	fmt.Fprintf(c.Stdout, "\n==> done. %d files, %s\n", files, fsx.HumanSize(size))
	return nil
}

// downloadArgv is one pass of download.sh. No API keys are mounted: candle, mark and
// funding-rate history are public endpoints.
func downloadArgv(c Config, pairs []string, p downloadPass) []string {
	argv := []string{
		"docker", "run", "--rm", "-u", c.user(),
		"-v", c.mount("lab/data") + ":/freqtrade/user_data/data",
		c.env("IMAGE", defaultImage), "download-data",
		"--exchange", "binance", "--trading-mode", "futures",
		"--pairs",
	}
	argv = append(argv, pairs...)
	argv = append(argv, "--timeframes")
	argv = append(argv, strings.Fields(p.Timeframes)...)
	return append(argv, "--timerange", p.From)
}

// readPairs returns exchange.pair_whitelist from a freqtrade pairlist file.
func readPairs(path string) ([]string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read pairlist: %w", err)
	}
	var pl struct {
		Exchange struct {
			PairWhitelist []string `json:"pair_whitelist"`
		} `json:"exchange"`
	}
	if err := json.Unmarshal(b, &pl); err != nil {
		return nil, fmt.Errorf("parse pairlist %s: %w", path, err)
	}
	return pl.Exchange.PairWhitelist, nil
}
