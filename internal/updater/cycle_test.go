package updater

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

// Cases 1-12e of tests/test_updater.sh, in the same order and with the same checks.

func TestCase01NoUpstreamChange(t *testing.T) {
	f := newFixture(t)
	out := f.runOnce()
	if strings.Contains(f.docker.log(), "restart") {
		t.Error("restarted with nothing to do")
	}
	if !strings.Contains(out, "up to date at") {
		t.Errorf("no up-to-date line: %s", out)
	}
}

func TestCase02WatchedFileChanged(t *testing.T) {
	f := newFixture(t)
	old := f.head()
	f.commit("NostalgiaForInfinityX7.py", "class X: pass  # v2")
	out := f.runOnce()
	if !f.docker.restarted() {
		t.Errorf("did not restart: %s", out)
	}
	if !regexp.MustCompile(`(?m)^exec bottrade`).MatchString(f.docker.log()) {
		t.Error("skipped the guard")
	}
	if got := strings.TrimSpace(readFile(t, f.statePath("previous_sha"))); got != old {
		t.Errorf("previous_sha is %q, expected %s", got, old)
	}
	if f.head() == old {
		t.Error("working tree did not advance")
	}
	if !strings.Contains(out, "guard passed for") {
		t.Error("a passing guard left no trace in the log")
	}
}

func TestCase03UnwatchedOnly(t *testing.T) {
	f := newFixture(t)
	old := f.head()
	f.commit("README.md", "docs v2")
	f.runOnce()
	if strings.Contains(f.docker.log(), "restart") {
		t.Error("restarted for an unwatched file")
	}
	if f.head() == old {
		t.Error("clone did not advance")
	}
}

func TestCase04GuardFails(t *testing.T) {
	f := newFixture(t)
	f.docker.importFail = true
	old := f.head()
	f.commit("NostalgiaForInfinityX7.py", "def broken(")
	f.runOnce()
	if strings.Contains(f.docker.log(), "restart") {
		t.Error("restarted despite a failing guard")
	}
	if f.head() != old {
		t.Error("left the bad commit checked out")
	}
	if exists(f.statePath("previous_sha")) {
		t.Error("wrote previous_sha on a failed update")
	}
}

func TestCase05DryRun(t *testing.T) {
	f := newFixture(t)
	f.commit("NostalgiaForInfinityX7.py", "class X: pass  # v3")
	old := f.head()
	out := f.runOnce("DRY_RUN=1")
	if strings.Contains(f.docker.log(), "restart") {
		t.Error("restarted in DRY_RUN")
	}
	if f.head() != old {
		t.Error("DRY_RUN advanced the tree, so the bot's next start would run unreviewed code")
	}
	if !strings.Contains(strings.ToLower(out), "would restart") {
		t.Errorf("no decision logged: %s", out)
	}
	if exists(f.statePath("previous_sha")) {
		t.Error("wrote previous_sha in DRY_RUN")
	}
}

func TestCase06BrokenConfig(t *testing.T) {
	f := newFixture(t)
	f.docker.configFail = true // import ok, show-config fails
	old := f.head()
	f.commit("configs/blacklist-binance.json", `{"broken":`)
	f.runOnce()
	if strings.Contains(f.docker.log(), "restart") {
		t.Error("restarted with an unloadable config")
	}
	if f.head() != old {
		t.Error("left the bad config checked out")
	}
	if !strings.Contains(f.docker.log(), "show-config") {
		t.Error("skipped the config guard")
	}
}

func TestCase07StopDisabledIsHeld(t *testing.T) {
	f := newFuseFixture(t)
	old := f.head()
	f.commit("NostalgiaForInfinityX7.py", strategy("False", "800", calcLine))
	out := f.runOnce()
	if strings.Contains(f.docker.log(), "restart") {
		t.Error("restarted into a stop-disabling change")
	}
	if f.head() != old {
		t.Error("tree advanced to the held commit")
	}
	pending := readFile(t, f.statePath("pending_risk"))
	if !regexp.MustCompile(`(?m)^- system_v3_2_stops_enable = True$`).MatchString(pending) ||
		!regexp.MustCompile(`(?m)^\+ system_v3_2_stops_enable = False$`).MatchString(pending) {
		t.Errorf("pending_risk missing or wrong: %q", pending)
	}
	if !strings.Contains(out, "HELD") {
		t.Error("no HELD line")
	}
	if !strings.Contains(out, "paged about held") {
		t.Error("did not page")
	}
	out = f.runOnce()
	if strings.Contains(out, "paged about held") {
		t.Error("paged again for the same held change")
	}
	if strings.Contains(pending, "Cache") || strings.Contains(pending, "stoploss = 1") {
		t.Error("read a setting from another class")
	}

	t.Run("case08 approving deploys it; a further change needs a fresh approval", func(t *testing.T) {
		var stdout, stderr strings.Builder
		if rc := approve(stateDir(f.state), []string{"--yes"}, strings.NewReader(""), &stdout, &stderr); rc != 0 {
			t.Fatalf("approve returned %d: %s %s", rc, stdout.String(), stderr.String())
		}
		if readFile(t, f.statePath("approved_risk")) == "" {
			t.Error("no approval written")
		}
		out := f.runOnce()
		if !f.docker.restarted() {
			t.Errorf("still held after approval: %s", out)
		}
		if exists(f.statePath("pending_risk")) {
			t.Error("pending_risk left behind after deploy")
		}
		f.docker.reset()
		f.commit("NostalgiaForInfinityX7.py", strategy("None", "800", calcLine))
		f.runOnce()
		if strings.Contains(f.docker.log(), "restart") {
			t.Error("an old approval let a different risk change through")
		}
	})
}

func TestCase09DroppedSafeParameterIsHeld(t *testing.T) {
	f := newFuseFixture(t)
	f.commit("NostalgiaForInfinityX7.py", strategy("True", "800", ""))
	f.runOnce()
	if strings.Contains(f.docker.log(), "restart") {
		t.Error("deployed a change that drops a config-overridable name")
	}
	pending := readFile(t, f.statePath("pending_risk"))
	if !regexp.MustCompile(`(?m)^- NFI_SAFE_PARAMETERS has num_cores_indicators_calc$`).MatchString(pending) {
		t.Errorf("did not name the dropped parameter: %q", pending)
	}
}

func TestCase10NonRiskChangeDeploys(t *testing.T) {
	f := newFuseFixture(t)
	f.commit("NostalgiaForInfinityX7.py", strategy("True", "900", calcLine))
	f.runOnce()
	if !f.docker.restarted() {
		t.Error("held a change that touches no risk setting")
	}
}

func TestCase11RestartCap(t *testing.T) {
	f := newFixture(t)
	old := f.head()
	now := time.Now().Unix()
	var b strings.Builder
	for i := int64(1); i <= 4; i++ {
		fmt.Fprintf(&b, "%d\n", now-i*600)
	}
	fmt.Fprintf(&b, "%d\n", now-90000) // older than 24h, must not count
	writeFile(t, f.statePath("restarts"), b.String())
	f.commit("NostalgiaForInfinityX7.py", "class X: pass  # v4")
	out := f.runOnce()
	if strings.Contains(f.docker.log(), "restart") {
		t.Error("restarted past the daily cap")
	}
	if f.head() != old {
		t.Error("tree advanced while deferred")
	}
	if !strings.Contains(out, "already 4 restarts") {
		t.Errorf("wrong count: %s", out)
	}
	f.runOnce("MAX_RESTARTS_PER_DAY=5")
	if !f.docker.restarted() {
		t.Error("did not deploy under the cap")
	}
	if n := len(records(readFile(t, f.statePath("restarts")))); n != 6 {
		t.Errorf("restart log has %d lines, want 6", n)
	}
}

func TestCase12VolatilityGate(t *testing.T) {
	f := newFixture(t)
	f.commit("NostalgiaForInfinityX7.py", "class X: pass  # v5")
	out := f.runOnce("VOL_URL=" + f.url("wild.json"))
	if strings.Contains(f.docker.log(), "restart") {
		t.Error("restarted in a 6% BTC range")
	}
	if !regexp.MustCompile(`range 6\.[0-9]+%`).MatchString(out) {
		t.Errorf("range not logged: %s", out)
	}
	f.runOnce("VOL_URL=" + f.url("missing.json"))
	if strings.Contains(f.docker.log(), "restart") {
		t.Error("restarted with no price data")
	}
	f.runOnce("VOL_URL="+f.url("wild.json"), "VOL_LIMIT_PCT=0")
	if !f.docker.restarted() {
		t.Error("gate not disabled by 0")
	}
}

func TestCase12bMinimumGap(t *testing.T) {
	f := newFixture(t)
	old := f.head()
	writeFile(t, f.statePath("restarts"), fmt.Sprintf("%d\n", time.Now().Unix()-3600))
	f.commit("NostalgiaForInfinityX7.py", "class X: pass  # gap")
	out := f.runOnce("MIN_RESTART_GAP=21600")
	if f.docker.restarted() {
		t.Error("restarted an hour after the last restart")
	}
	if !strings.Contains(out, "minimum gap 21600s") {
		t.Errorf("gap not logged: %s", out)
	}
	if f.head() != old {
		t.Error("tree advanced inside the gap")
	}
	f.runOnce("MIN_RESTART_GAP=1800")
	if !f.docker.restarted() {
		t.Error("did not deploy after the gap")
	}
}

func TestCase12cLongVolatility(t *testing.T) {
	f := newFixture(t)
	f.commit("NostalgiaForInfinityX7.py", "class X: pass  # trend")
	out := f.runOnce("VOL_LONG_URL=" + f.url("wild.json"))
	if f.docker.restarted() {
		t.Error("restarted into a 6% 4h range")
	}
	if !strings.Contains(out, "BTC 4h range") {
		t.Errorf("4h range not logged: %s", out)
	}
	f.runOnce("VOL_LONG_URL=" + f.url("missing.json"))
	if f.docker.restarted() {
		t.Error("restarted with no 4h data")
	}
	f.runOnce("VOL_LONG_URL="+f.url("wild.json"), "VOL_LONG_LIMIT_PCT=0")
	if !f.docker.restarted() {
		t.Error("4h gate not disabled by 0")
	}
}

func TestCase12dUnhealthyBot(t *testing.T) {
	f := newFixture(t)
	old := f.head()
	f.docker.botLog = `binance {"code":-2015,"msg":"Invalid API-key, IP, or permissions for action."}` + "\n"
	f.commit("NostalgiaForInfinityX7.py", "class X: pass  # unhealthy")
	out := f.runOnce()
	if f.docker.restarted() {
		t.Error("restarted a bot that cannot reach the exchange")
	}
	if !strings.Contains(out, "logged exchange errors") {
		t.Errorf("reason not logged: %s", out)
	}
	if f.head() != old {
		t.Error("tree advanced while unhealthy")
	}
	f.docker.botLog = "all fine\n"
	f.runOnce()
	if !f.docker.restarted() {
		t.Error("did not deploy on a healthy bot")
	}
}

// listed is the strategy with a multi-line grind_mode_coins whose second coin is coin.
func listed(coin string) string {
	s := strategy("True", "800", calcLine)
	return strings.Replace(s, "  startup_candle_count = 800\n",
		"  startup_candle_count = 800\n  grind_mode_coins = [\n    \"BTC\",\n    \""+coin+"\",\n  ]\n", 1)
}

func TestCase12eMultiLineSetting(t *testing.T) {
	f := newFixture(t)
	f.setRunning(listed("DOGE"))
	old := f.head()
	f.commit("NostalgiaForInfinityX7.py", listed("PEPE"))
	out := f.runOnce()
	if f.docker.restarted() {
		t.Error("deployed a changed grind_mode_coins list")
	}
	pending := readFile(t, f.statePath("pending_risk"))
	if !strings.Contains(pending, "PEPE") {
		t.Errorf("pending diff missing: %s", out)
	}
	if !strings.Contains(pending, `+ grind_mode_coins = [ "BTC", "PEPE", ]`) {
		t.Errorf("joined line not as the shell wrote it: %q", pending)
	}
	if f.head() != old {
		t.Error("tree advanced while held")
	}
}

// Checks the shell suite did not have.

func TestPendingRiskFormat(t *testing.T) {
	f := newFuseFixture(t)
	old := f.head()
	f.commit("NostalgiaForInfinityX7.py", strategy("False", "800", calcLine))
	newSHA := gitT(t, f.upstream, "rev-parse", "HEAD")
	f.runOnce()
	diff := []string{"+ system_v3_2_stops_enable = False", "- system_v3_2_stops_enable = True"}
	want := fmt.Sprintf("commit %s -> %s  digest %s\n%s\n", old[:8], newSHA[:8], riskDigest(diff), strings.Join(diff, "\n"))
	if got := readFile(t, f.statePath("pending_risk")); got != want {
		t.Errorf("pending_risk =\n%q\nwant\n%q", got, want)
	}
}

func TestApprovalWrittenByShellStillMatches(t *testing.T) {
	// A digest approved by bin/approve-update.sh before the port: the shell wrote it
	// with echo, so with a trailing newline.
	f := newFuseFixture(t)
	f.commit("NostalgiaForInfinityX7.py", strategy("False", "800", calcLine))
	// 42fc18ce542d59be is what update.sh's risk_diff | sha256sum printed for this change.
	writeFile(t, f.statePath("approved_risk"), "42fc18ce542d59be\n")
	f.runOnce()
	if !f.docker.restarted() {
		t.Error("an approval of this exact change (digest from the shell) did not deploy")
	}
}

func TestHeldPageFailureIsRetried(t *testing.T) {
	ft := newFakeTelegram(t)
	ft.fail = true
	f := newFuseFixture(t)
	f.commit("NostalgiaForInfinityX7.py", strategy("False", "800", calcLine))
	kv := []string{"FREQTRADE__TELEGRAM__TOKEN=maintok", "FREQTRADE__TELEGRAM__CHAT_ID=5", "TELEGRAM_API=" + ft.api()}
	out := f.runOnce(kv...)
	if exists(f.statePath("notified_risk")) {
		t.Error("recorded a page that did not go out")
	}
	if !strings.Contains(out, "telegram notify failed") {
		t.Errorf("failure not logged: %s", out)
	}
	ft.fail = false
	out = f.runOnce(kv...)
	if !strings.Contains(out, "paged about held update") || len(ft.callsOf("sendMessage")) != 2 {
		t.Errorf("did not retry the page: %s", out)
	}
	if strings.Contains(out, "maintok") {
		t.Error("token printed in the log")
	}
}

func TestDeployNotifications(t *testing.T) {
	tests := []struct {
		name        string
		importFail  bool
		restartFail bool
		want        string
		wantOut     outcome
		wantRecord  bool
	}{
		{"restarted", false, false, "bot restarted", outRestarted, true},
		{"guard failed", true, false, "failed to import - rolled back, bot untouched", outGuardFailed, false},
		{"restart failed", false, true, "applied but restart FAILED - check the host", outRestartFailed, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ft := newFakeTelegram(t)
			f := newFixture(t)
			f.docker.importFail, f.docker.restartFail = tt.importFail, tt.restartFail
			f.commit("NostalgiaForInfinityX7.py", "class X: pass  # n")
			u, logs := f.updater("FREQTRADE__TELEGRAM__TOKEN=maintok", "FREQTRADE__TELEGRAM__CHAT_ID=5", "TELEGRAM_API="+ft.api())
			if got := u.cycle(context.Background()); got != tt.wantOut {
				t.Fatalf("outcome %s, want %s\n%s", got, tt.wantOut, logs)
			}
			msgs := ft.callsOf("sendMessage")
			if len(msgs) != 1 || !strings.Contains(msgs[0].Params.Get("text"), tt.want) || msgs[0].Params.Get("chat_id") != "5" {
				t.Errorf("page = %+v, want text containing %q", msgs, tt.want)
			}
			if got := exists(f.statePath("restarts")); got != tt.wantRecord {
				t.Errorf("restart recorded = %v, want %v", got, tt.wantRecord)
			}
		})
	}
}

func TestFetchFailureChangesNothing(t *testing.T) {
	f := newFixture(t)
	old := f.head()
	f.commit("NostalgiaForInfinityX7.py", "class X: pass  # unreachable")
	gitT(t, f.nfi, "remote", "set-url", "origin", f.dir+"/nowhere")
	u, logs := f.updater()
	if got := u.cycle(context.Background()); got != outFetchFailed {
		t.Fatalf("outcome %s\n%s", got, logs)
	}
	if !strings.Contains(logs.String(), "fetch failed (network?), retrying next cycle") || f.head() != old || f.docker.log() != "" {
		t.Errorf("fetch failure was not a no-op: %s", logs)
	}
}

func TestDecisionOrder(t *testing.T) {
	// Every gate closed at once: the first in update.sh's order is the one that reports.
	f := newFixture(t)
	now := time.Now().Unix()
	writeFile(t, f.statePath("restarts"), fmt.Sprintf("%d\n%d\n", now-60, now-30))
	f.docker.botLog = "Could not load markets\n"
	f.commit("NostalgiaForInfinityX7.py", "class X: pass  # order")
	all := []string{"MAX_RESTARTS_PER_DAY=2", "MIN_RESTART_GAP=3600", "VOL_URL=" + f.url("wild.json"), "VOL_LONG_URL=" + f.url("wild.json")}
	steps := []struct {
		open string // the gate opened before this step
		want string
	}{
		{"", "already 2 restarts in 24h"},
		{"MAX_RESTARTS_PER_DAY=9", "minimum gap 3600s"},
		{"MIN_RESTART_GAP=0", "BTC 30m range"},
		{"VOL_URL=" + f.url("calm.json"), "BTC 4h range"},
		{"VOL_LONG_URL=" + f.url("calm.json"), "logged exchange errors"},
	}
	kv := append([]string{}, all...)
	for _, s := range steps {
		if s.open != "" {
			kv = append(kv, s.open)
		}
		u, logs := f.updater(kv...)
		if got := u.cycle(context.Background()); got != outDeferred {
			t.Fatalf("after opening %q: outcome %s", s.open, got)
		}
		if !strings.Contains(logs.String(), s.want) {
			t.Errorf("after opening %q: want %q first, got %s", s.open, s.want, logs)
		}
		if strings.Contains(f.docker.log(), "exec") {
			t.Fatal("a guard ran while a gate was closed")
		}
	}
	// The fuse comes before every gate: a held change logs HELD, not "deferred".
	f2 := newFuseFixture(t)
	writeFile(t, f2.statePath("restarts"), fmt.Sprintf("%d\n", now-60))
	f2.commit("NostalgiaForInfinityX7.py", strategy("False", "800", calcLine))
	u, logs := f2.updater("MAX_RESTARTS_PER_DAY=1")
	if got := u.cycle(context.Background()); got != outHeld || strings.Contains(logs.String(), "deferred") {
		t.Errorf("fuse did not come first: %s %s", got, logs)
	}
}

func TestRestartsToday(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	tests := []struct {
		name string
		log  string
		want int
	}{
		{"missing", "", 0},
		{"boundary counts", "1799913600\n1799913599\n", 1},
		{"blank lines skipped", "\n1799999000\n\n", 1},
		{"garbage counts", "abc\n1799999000\n", 2},
		{"leading spaces", "  1799999000\n", 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			if tt.log != "" {
				writeFile(t, dir+"/restarts", tt.log)
			}
			u := New(Config{StateDir: stateDir(dir)}, Deps{Now: func() time.Time { return now }})
			got, err := u.restartsToday()
			if err != nil || got != tt.want {
				t.Errorf("got %d, %v; want %d", got, err, tt.want)
			}
		})
	}
}

func TestSinceLastRestart(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	tests := []struct {
		name   string
		log    string
		want   int64
		wantOK bool
	}{
		{"missing", "", 0, false},
		{"last line", "1799990000\n1799996400\n", 3600, true},
		{"no final newline", "1799996400", 3600, true},
		{"garbage last line", "1799996400\nxyz\n", 0, false},
		{"future time", "1800000100\n", -100, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			if tt.log != "" {
				writeFile(t, dir+"/restarts", tt.log)
			}
			u := New(Config{StateDir: stateDir(dir)}, Deps{Now: func() time.Time { return now }, Log: discardLog()})
			got, ok := u.sinceLastRestart()
			if got != tt.want || ok != tt.wantOK {
				t.Errorf("got %d,%v; want %d,%v", got, ok, tt.want, tt.wantOK)
			}
		})
	}
}

func TestRangePctOverHTTP(t *testing.T) {
	tests := []struct {
		name   string
		status int
		body   string
		want   float64
		wantOK bool
	}{
		{"calm", 200, calmKlines, 0.4, true},
		{"wild", 200, wildKlines, 6, true},
		{"rounded before comparing", 200, `[[1,"0","101.996","100","100",0]]`, 2, true},
		{"http error", 500, calmKlines, 0, false},
		{"binance error body", 200, `{"code":-1121,"msg":"Invalid symbol."}`, 0, false},
		{"empty list", 200, `[]`, 0, false},
		{"zero close", 200, `[[1,"0","1","1","0",0]]`, 0, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tt.status)
				_, _ = w.Write([]byte(tt.body))
			}))
			defer srv.Close()
			u := New(Config{}, Deps{Log: discardLog()})
			got, ok := u.rangePct(context.Background(), srv.URL)
			if ok != tt.wantOK || got != tt.want {
				t.Errorf("got %v,%v; want %v,%v", got, ok, tt.want, tt.wantOK)
			}
		})
	}
}

func TestVolLimitComparesRoundedValue(t *testing.T) {
	// update.sh compared printf "%.2f" output: 1.996% is "2.00", which is >= 2.
	f := newFixture(t)
	writeFile(t, f.dir+"/edge.json", `[[1,"0","101.996","100","100",0]]`)
	f.commit("NostalgiaForInfinityX7.py", "class X: pass  # edge")
	out := f.runOnce("VOL_URL=" + f.url("edge.json"))
	if f.docker.restarted() || !strings.Contains(out, "BTC 30m range 2.00% >= 2%") {
		t.Errorf("1.996%% was not treated as 2.00%%: %s", out)
	}
}

func TestUnhealthyCheck(t *testing.T) {
	tests := []struct {
		name     string
		botLog   string
		logsFail bool
		pattern  string
		want     bool
	}{
		{"auth error", "ccxt.base.errors.AuthenticationError: binance\n", false, "", true},
		{"markets", "2026-11-17 08:27 Could not load markets.\n", false, "", true},
		{"healthy", "Bot heartbeat. PID=1, state='RUNNING'\n", false, "", false},
		{"logs fail", "", true, "", false},
		{"custom pattern", "boom\n", false, "UNHEALTHY_PATTERN=^boom$", true},
		{"anchors are per line", "x\nboom\ny\n", false, "UNHEALTHY_PATTERN=^boom$", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFixture(t)
			f.docker.botLog, f.docker.logsFail = tt.botLog, tt.logsFail
			var kv []string
			if tt.pattern != "" {
				kv = append(kv, tt.pattern)
			}
			u, _ := f.updater(kv...)
			if got := u.unhealthy(context.Background()); got != tt.want {
				t.Errorf("unhealthy = %v, want %v", got, tt.want)
			}
			if !strings.Contains(f.docker.log(), "logs --since 15m bottrade") {
				t.Errorf("docker call = %q", f.docker.log())
			}
		})
	}
}

func TestDeployFinishesAfterCancel(t *testing.T) {
	// Cancellation after the tree moved must not strand it: the guard and restart run
	// on a context that ignores cancellation.
	f := newFixture(t)
	f.commit("NostalgiaForInfinityX7.py", "class X: pass  # cancel")
	u, logs := f.updater()
	ctx, cancel := context.WithCancel(context.Background())
	old := f.head()
	newSHA := gitT(t, f.upstream, "rev-parse", "HEAD")
	gitT(t, f.nfi, "fetch", "-q", "origin", "main")
	cancel()
	if got := u.deploy(ctx, old, newSHA); got != outRestarted {
		t.Fatalf("outcome %s\n%s", got, logs)
	}
	if f.head() != newSHA {
		t.Error("tree not on the deployed commit")
	}
}

func TestStateFilesAreShellCompatible(t *testing.T) {
	f := newFixture(t)
	old := f.head()
	f.commit("NostalgiaForInfinityX7.py", "class X: pass  # files")
	f.runOnce()
	if got := readFile(t, f.statePath("previous_sha")); got != old+"\n" {
		t.Errorf("previous_sha = %q", got)
	}
	if !regexp.MustCompile(`^[0-9]{10}\n$`).MatchString(readFile(t, f.statePath("restarts"))) {
		t.Errorf("restarts = %q", readFile(t, f.statePath("restarts")))
	}
}

func TestRiskSnapshotsSkippedAndCached(t *testing.T) {
	f := newFuseFixture(t)
	f.commit("configs/blacklist-binance.json", `{"b":2}`)
	u, logs := f.updater("DRY_RUN=1")
	if got := u.cycle(context.Background()); got != outDryRun {
		t.Fatalf("outcome %s\n%s", got, logs)
	}
	if n := f.git.count("show"); n != 0 {
		t.Errorf("configs-only change read the strategy %d times", n)
	}

	// A held change is snapshotted once, not on every cycle while it waits.
	f.commit("NostalgiaForInfinityX7.py", strategy("False", "800", calcLine))
	u, logs = f.updater()
	for i := range 3 {
		if got := u.cycle(context.Background()); got != outHeld {
			t.Fatalf("cycle %d: outcome %s\n%s", i, got, logs)
		}
		if n, want := f.git.count("show"), map[bool]int{true: 2, false: 0}[i == 0]; n != want {
			t.Errorf("cycle %d: %d strategy reads, want %d", i, n, want)
		}
	}
}

func TestStrategyOutsideWatchPathsIsStillChecked(t *testing.T) {
	// With WATCH_PATHS=configs/ a strategy change is not listed, but a config change
	// deploys the whole tree, so the fuse must still compare the strategies.
	f := newFuseFixture(t)
	writeFile(t, filepath.Join(f.upstream, "NostalgiaForInfinityX7.py"), strategy("False", "800", calcLine)+"\n")
	f.commit("configs/blacklist-binance.json", `{"c":3}`)
	u, logs := f.updater("WATCH_PATHS=configs/")
	if got := u.cycle(context.Background()); got != outHeld {
		t.Errorf("outcome %s, want held\n%s", got, logs)
	}
}
