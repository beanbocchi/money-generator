package newsgate

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// testEnv sets vars in the process environment for the test and captures the output.
func testEnv(t *testing.T, vars map[string]string, now time.Time) (env, *bytes.Buffer, *bytes.Buffer) {
	for _, k := range []string{"NEWSGATE_STATE", "NEWSGATE_PROXY_KEY", "NEWSGATE_BASE_URL", "MG_ROOT"} {
		t.Setenv(k, vars[k])
	}
	var out, errb bytes.Buffer
	return env{stdout: &out, stderr: &errb, now: func() time.Time { return now }}, &out, &errb
}

const windowsLog = `{"kind": "start", "model": "gpt-6.1-sol", "cycle": 300, "ts": "2026-11-19T12:00:00+00:00"}
{"kind": "item", "id": "c:1", "source": "cointelegraph", "title": "Hong Kong adds Hyperliquid to list of ‘suspicious’ crypto platforms and then some more words to pass seventy", "link": "", "published": "2026-11-19T12:44:06+00:00", "fetched": "2026-11-19T12:48:35+00:00", "decided": "2026-11-19T12:51:48+00:00", "event_type": "regulatory", "scope": "TICKERS", "tickers": ["HYPE"], "severity": 2, "reason": "r", "window": {"targets": ["HYPE"], "from": "2026-11-19T12:51:48+00:00", "until": "2026-11-20T12:51:48+00:00"}, "ts": "2026-11-19T12:51:48+00:00"}
{"kind": "item", "id": "c:2", "source": "coindesk", "title": "Binance halts withdrawals", "link": "", "published": "2026-11-20T08:04:40+00:00", "fetched": "2026-11-20T08:08:35+00:00", "decided": "2026-11-20T08:11:45+00:00", "event_type": "exchange_incident", "scope": "MARKET", "tickers": [], "severity": 3, "reason": "r", "window": {"targets": ["MARKET"], "from": "2026-11-20T08:11:45+00:00", "until": "2026-11-21T08:11:45+00:00"}, "ts": "2026-11-20T08:11:45+00:00"}
{"kind": "cycle", "fetched": 1, "new": 0, "old": 0, "classified": 0, "fetch_errors": 0, "api_errors": 0, "ts": "2026-11-20T08:12:00+00:00"}
`

func TestWindowsCommand(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "log.jsonl"), []byte(windowsLog), 0o644); err != nil {
		t.Fatal(err)
	}
	e, out, errb := testEnv(t, map[string]string{"NEWSGATE_STATE": dir}, time.Date(2026, 11, 21, 0, 0, 0, 0, time.UTC))
	if code := run(context.Background(), []string{"windows"}, e); code != 0 {
		t.Fatalf("exit %d: %s", code, errb)
	}
	// Expected text produced by bin/newsgate.sh's Python snippet on the same log at the same time.
	want := "closed 2026-11-19T12:51:48+00:00 HYPE         sev2 regulatory        Hong Kong adds Hyperliquid to list of ‘suspicious’ crypto platforms an\n" +
		"OPEN   2026-11-20T08:11:45+00:00 MARKET       sev3 exchange_incident Binance halts withdrawals\n"
	if out.String() != want {
		t.Errorf("windows output:\n%s\nwant:\n%s", out, want)
	}
}

func TestLogsCommand(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "log.jsonl"), []byte("a\nb\nc\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct {
		args []string
		want string
	}{
		{[]string{"logs"}, "a\nb\nc\n"},
		{[]string{"logs", "2"}, "b\nc\n"},
		{[]string{"logs", "0"}, ""},
		{[]string{"logs", "9"}, "a\nb\nc\n"},
	} {
		e, out, errb := testEnv(t, map[string]string{"NEWSGATE_STATE": dir}, time.Now())
		if code := run(context.Background(), tt.args, e); code != 0 {
			t.Fatalf("%v: exit %d: %s", tt.args, code, errb)
		}
		if out.String() != tt.want {
			t.Errorf("%v: got %q, want %q", tt.args, out, tt.want)
		}
	}
	e, _, _ := testEnv(t, map[string]string{"NEWSGATE_STATE": dir}, time.Now())
	if code := run(context.Background(), []string{"logs", "x"}, e); code != 2 {
		t.Errorf("logs x: exit %d, want 2", code)
	}
}

func TestToolStateFromRepoRoot(t *testing.T) {
	root := t.TempDir()
	state := filepath.Join(root, "services", "newsgate", "state")
	if err := os.MkdirAll(state, 0o755); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(state, "log.jsonl"), []byte("x\n"), 0o644)
	e, out, errb := testEnv(t, map[string]string{"MG_ROOT": root}, time.Now())
	if code := run(context.Background(), []string{"logs", "1"}, e); code != 0 || out.String() != "x\n" {
		t.Errorf("exit %d out %q err %s", code, out, errb)
	}
}

func TestRunNeedsCredentials(t *testing.T) {
	e, _, errb := testEnv(t, map[string]string{"NEWSGATE_BASE_URL": "http://x"}, time.Now())
	if code := run(context.Background(), []string{"run", "--once"}, e); code != 1 || !strings.Contains(errb.String(), "NEWSGATE_PROXY_KEY") {
		t.Errorf("exit %d: %s", code, errb)
	}
}

func TestUsage(t *testing.T) {
	for _, args := range [][]string{nil, {"start"}} {
		e, _, errb := testEnv(t, nil, time.Now())
		if code := run(context.Background(), args, e); code != 2 || !strings.Contains(errb.String(), "usage") {
			t.Errorf("%v: exit %d: %s", args, code, errb)
		}
	}
}
