package updater

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestMainEndToEnd runs Main as the container would, with a shell-script docker. The
// stub writes the bot's log to stderr, as freqtrade does, so this also checks that the
// real docker runner reads stderr for the exchange-error check.
func TestMainEndToEnd(t *testing.T) {
	f := newFixture(t)
	stub := filepath.Join(f.dir, "docker")
	calls := filepath.Join(f.dir, "docker.log")
	botlog := filepath.Join(f.dir, "botlog")
	writeFile(t, stub, `#!/bin/sh
echo "$@" >> "`+calls+`"
if [ "$1" = logs ]; then cat "`+botlog+`" >&2; fi
exit 0
`)
	if err := os.Chmod(stub, 0o755); err != nil {
		t.Fatal(err)
	}
	env := f.baseEnv()
	env["DOCKER"] = stub
	setEnv(t, env)
	f.commit("NostalgiaForInfinityX7.py", "class X: pass  # main")

	writeFile(t, botlog, "binance -2015 Invalid API-key\n")
	if rc := Main(context.Background(), nil); rc != 0 {
		t.Fatalf("rc %d", rc)
	}
	if strings.Contains(readFile(t, calls), "restart") {
		t.Fatal("restarted although the bot logged -2015 on stderr")
	}

	writeFile(t, botlog, "all fine\n")
	if rc := Main(context.Background(), nil); rc != 0 {
		t.Fatalf("rc %d", rc)
	}
	log := readFile(t, calls)
	if !strings.Contains(log, "restart bottrade") || !strings.Contains(log, "exec bottrade python3 -P -c import sys;") {
		t.Errorf("docker calls:\n%s", log)
	}
}

func TestMainRefusesBadInput(t *testing.T) {
	if rc := Main(context.Background(), []string{"--now"}); rc != 2 {
		t.Errorf("unexpected argument: rc %d", rc)
	}
	setEnv(t, map[string]string{"VOL_LIMIT_PCT": "two"})
	if rc := Main(context.Background(), nil); rc != 2 {
		t.Errorf("bad VOL_LIMIT_PCT: rc %d", rc)
	}
}

func TestLoadConfigDefaults(t *testing.T) {
	setEnv(t, nil)
	c, err := LoadConfig()
	if err != nil {
		t.Fatal(err)
	}
	checks := map[string][2]any{
		"NFI_DIR":              {c.NFIDir, "/nfi"},
		"STATE_DIR":            {string(c.StateDir), "/state"},
		"DOCKER":               {c.Docker, "docker"},
		"TARGET_CONTAINER":     {c.Target, "bottrade"},
		"STRATEGY_MODULE":      {c.StrategyModule, "NFIX7Risk"},
		"STRATEGY_FILE":        {c.StrategyFile, "NostalgiaForInfinityX7.py"},
		"UPDATE_INTERVAL":      {c.Interval, 900 * time.Second},
		"DRY_RUN":              {c.DryRun, false},
		"RUN_ONCE":             {c.RunOnce, false},
		"WATCH_PATHS":          {strings.Join(c.WatchPaths, "|"), "NostalgiaForInfinityX7.py|configs/"},
		"RISK_PATTERN":         {c.RiskPattern.String(), defRiskPattern},
		"MAX_RESTARTS_PER_DAY": {c.MaxRestartsPerDay, 4},
		"MIN_RESTART_GAP":      {c.MinRestartGap, 21600 * time.Second},
		"VOL_LIMIT_PCT":        {c.VolLimit, 2.0},
		"VOL_URL":              {c.VolURL, defVolURL},
		"VOL_LONG_LIMIT_PCT":   {c.VolLongLimit, 4.0},
		"VOL_LONG_URL":         {c.VolLongURL, defVolLongURL},
		"UNHEALTHY_PATTERN":    {c.Unhealthy.String(), "(?m)" + defUnhealthy},
		"UNHEALTHY_WINDOW":     {c.UnhealthyWindow, "15m"},
		"TELEGRAM_API":         {c.TelegramAPI, ""},
		"SELFTEST_SECONDS":     {c.SelftestWait, 180 * time.Second},
		"GUARD_TIMEOUT":        {c.GuardTimeout, 300 * time.Second},
		"approval":             {c.ApprovalEnabled(), false},
	}
	for name, v := range checks {
		if v[0] != v[1] {
			t.Errorf("%s = %v, want %v", name, v[0], v[1])
		}
	}
}

func TestLoadConfigValues(t *testing.T) {
	setEnv(t, map[string]string{
		"MIN_RESTART_GAP": "0", "VOL_LIMIT_PCT": "0", "VOL_LONG_LIMIT_PCT": " 1.5",
		"UNHEALTHY_PATTERN": "", "DRY_RUN": "true", "WATCH_PATHS": " a\tb  c ",
	})
	c, err := LoadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if c.MinRestartGap != 0 || c.VolLimit != 0 || c.VolLongLimit != 1.5 {
		t.Errorf("gap %v, limits %v %v", c.MinRestartGap, c.VolLimit, c.VolLongLimit)
	}
	if c.Unhealthy.String() != "(?m)"+defUnhealthy {
		t.Error("empty UNHEALTHY_PATTERN did not fall back to the default, as ${VAR:-default} does")
	}
	if c.DryRun {
		t.Error(`DRY_RUN=true enabled the dry run; only "1" does`)
	}
	if strings.Join(c.WatchPaths, ",") != "a,b,c" {
		t.Errorf("watch paths %q", c.WatchPaths)
	}
	// The shell kept a gate on for any limit but the string "0", so "0.0" meant "never
	// restart". It must not quietly turn the gate off here.
	for k, v := range map[string]string{
		"UPDATE_INTERVAL": "15m", "MAX_RESTARTS_PER_DAY": "four", "MIN_RESTART_GAP": "1.5",
		"VOL_LIMIT_PCT": "2%", "VOL_LONG_LIMIT_PCT": "0.0", "RISK_PATTERN": "stop(", "UNHEALTHY_PATTERN": "[",
		"GUARD_TIMEOUT": "0",
	} {
		setEnv(t, map[string]string{k: v})
		if _, err := LoadConfig(); err == nil {
			t.Errorf("%s=%q accepted", k, v)
		}
	}
}
