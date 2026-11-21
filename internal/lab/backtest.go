package lab

import (
	"context"
	"fmt"
	"os"
	"strings"
)

const nfiDir = "NostalgiaForInfinity"

// Backtest runs one freqtrade backtest into lab/results (was lab/backtest.sh).
//
//	mg backtest [STRATEGY] [TIMERANGE]
//
// Environment, each falling back to its default when unset or empty:
//
//	IMAGE             freqtradeorg/freqtrade:stable
//	DATADIR           lab/data. Point it at a snapshot while a download writes
//	                  lab/data: freqtrade rewrites feather files whole, and a file
//	                  read mid-write can come back short without any error.
//	CONFIG            lab/config-backtest.json
//	STRATDIR          user_data/strategies. A directory holding a historical
//	                  NostalgiaForInfinityX7.py (ADR-0006 rule 1).
//	PAIRLIST          lab/pairlists/live-traded.json
//	EXTRA             extra freqtrade arguments, split on whitespace
//	BT_OOM_SCORE_ADJ  1000. A run peaks around 11-13GB next to the live bot; if the
//	                  kernel has to kill something it should pick the backtest.
//	NFI_MAX_DEPLOYED  passed through to the container by name.
func Backtest(ctx context.Context, args []string) int {
	if isHelp(args) {
		fmt.Fprintln(os.Stderr, "usage: mg backtest [STRATEGY] [TIMERANGE]")
		return 2
	}
	c, err := defaultConfig()
	if err != nil {
		return report(os.Stderr, "backtest", err)
	}
	return report(c.Stderr, "backtest", runBacktest(ctx, c, arg(args, 0), arg(args, 1)))
}

func backtestDefaults(strategy, timerange string) (string, string) {
	if strategy == "" {
		strategy = "NFIX7Risk"
	}
	if timerange == "" {
		timerange = "20260228-20260716"
	}
	return strategy, timerange
}

// runBacktest prints the same banner lines as backtest.sh and runs the container.
// An empty strategy or timerange takes the default, as ${1:-...} did.
func runBacktest(ctx context.Context, c Config, strategy, timerange string) error {
	strategy, timerange = backtestDefaults(strategy, timerange)
	if err := mkdirs(c, "lab/results", "lab/logs"); err != nil {
		return err
	}
	fmt.Fprintf(c.Stdout, "==> %s over %s\n", strategy, timerange)
	if err := c.run(ctx, backtestArgv(c, strategy, timerange)); err != nil {
		return err
	}
	fmt.Fprintf(c.Stdout, "\n==> results: lab/results/%s-%s.json\n", strategy, timerange)
	return nil
}

// backtestArgv is the docker command line from backtest.sh, in its order.
//
// Upstream configs come from lab/configs-pinned/. The updater runs `git reset --hard`
// on the NostalgiaForInfinity clone every 15 minutes, and an update landing between
// two arms of a comparison would give them different blacklists or pairlists.
//
// configs/blacklist-binance.json is left out on purpose. Today's blacklist lists the
// pairs that later blew up, and applying it to 2026 history removes the tail events
// the rig exists to measure.
func backtestArgv(c Config, strategy, timerange string) []string {
	strategy, timerange = backtestDefaults(strategy, timerange)
	argv := []string{
		"docker", "run", "--rm", "-u", c.user(), "--oom-score-adj", c.env("BT_OOM_SCORE_ADJ", "1000"),
		"-v", c.mount(c.env("DATADIR", "lab/data")) + ":/freqtrade/user_data/data",
		"-v", c.mount("lab/results") + ":/freqtrade/user_data/backtest_results",
		"-v", c.mount("lab/logs") + ":/freqtrade/user_data/logs",
		"-v", c.mount(c.env("CONFIG", "lab/config-backtest.json")) + ":/freqtrade/lab-config.json:ro",
		"-v", c.mount(c.env("PAIRLIST", "lab/pairlists/live-traded.json")) + ":/freqtrade/lab-pairlist.json:ro",
		"-v", c.mount(c.env("STRATDIR", "user_data/strategies")) + ":/freqtrade/user_data/strategies:ro",
		"-v", c.mount(nfiDir) + ":/freqtrade/" + nfiDir + ":ro",
		"-v", c.mount("lab/configs-pinned") + ":/freqtrade/pinned:ro",
		"-e", "COLUMNS=200",
		"-e", "NFI_MAX_DEPLOYED",
		c.env("IMAGE", defaultImage), "backtesting",
		"--strategy", strategy,
		"--timerange", timerange,
		"--config", "/freqtrade/pinned/trading_mode-futures.json",
		"--config", "/freqtrade/pinned/exampleconfig.json",
		"--config", "/freqtrade/lab-pairlist.json",
		"--config", "/freqtrade/lab-config.json",
		"--cache", "none", "--breakdown", "day",
		"--export", "signals",
		"--export-filename", "user_data/backtest_results/" + strategy + "-" + timerange + ".json",
	}
	return append(argv, strings.Fields(c.env("EXTRA", ""))...)
}
