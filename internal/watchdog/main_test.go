package watchdog

// Ports tests/test_watchdog.sh. The fake freqtrade API and the fake Telegram are httptest
// servers; time is a fake clock that the watchdog's sleep advances, so polls, debouncing
// and re-paging are exact instead of racing a wall-clock timeout.

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/beanbocchi/money-generator/internal/clock"
	"github.com/beanbocchi/money-generator/internal/telegram"
)

type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

type rig struct {
	t        *testing.T
	clk      *fakeClock
	state    atomic.Value // health body kind, see healthBody
	credsBad atomic.Bool
	tgBad    atomic.Bool
	balHits  atomic.Int32
	ft, tg   *httptest.Server

	mu   sync.Mutex
	sent []string
}

func newRig(t *testing.T) *rig {
	r := &rig{t: t, clk: &fakeClock{t: time.Date(2026, 11, 21, 5, 0, 0, 0, time.UTC)}}
	r.state.Store("fresh")
	r.ft = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if u, p, ok := req.BasicAuth(); !ok || u != "u" || p != "p" {
			http.Error(w, `{"detail":"Unauthorized"}`, http.StatusUnauthorized)
			return
		}
		switch req.URL.Path {
		case "/api/v1/balance":
			r.balHits.Add(1)
			if r.credsBad.Load() {
				http.Error(w, `{"error":"-2015"}`, http.StatusInternalServerError)
				return
			}
			fmt.Fprint(w, `{"total":100}`)
		case "/api/v1/health":
			fmt.Fprint(w, r.healthBody())
		default:
			http.NotFound(w, req)
		}
	}))
	r.tg = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if err := req.ParseForm(); err != nil || req.URL.Path != "/bott/sendMessage" {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		if r.tgBad.Load() {
			w.WriteHeader(http.StatusInternalServerError)
			fmt.Fprint(w, `{"ok":false,"description":"boom"}`)
			return
		}
		r.mu.Lock()
		r.sent = append(r.sent, req.PostForm.Get("text"))
		r.mu.Unlock()
		fmt.Fprint(w, `{"ok":true,"result":{}}`)
	}))
	t.Cleanup(r.ft.Close)
	t.Cleanup(r.tg.Close)
	return r
}

func (r *rig) healthBody() string {
	now := r.clk.Now().Unix()
	switch r.state.Load().(string) {
	case "garbage":
		return `{"unexpected":true}`
	case "starting": // restarted 60s ago, no process tick yet
		return fmt.Sprintf(`{"last_process_ts":null,"bot_startup_ts":%d}`, now-60)
	case "null_old": // null long past any plausible startup
		return fmt.Sprintf(`{"last_process_ts":null,"bot_startup_ts":%d}`, now-99999)
	case "stale":
		return fmt.Sprintf(`{"last_process_ts":%d}`, now-99999)
	default:
		return fmt.Sprintf(`{"last_process_ts":%d}`, now)
	}
}

func (r *rig) messages() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.sent...)
}

var envKeys = []string{"API", "FREQTRADE__API_SERVER__USERNAME", "FREQTRADE__API_SERVER__PASSWORD", "MAX_AGE",
	"INTERVAL", "FAILS_BEFORE_ALERT", "CREDS_CHECK_EVERY", "CREDS_FAILS_BEFORE_ALERT", "REALERT_EVERY", "RUN_ONCE",
	"TELEGRAM_API", "FREQTRADE__TELEGRAM__TOKEN", "FREQTRADE__TELEGRAM__CHAT_ID"}

// setEnv sets the watchdog's variables to kv and every other one to empty (unset).
func setEnv(t *testing.T, kv map[string]string) {
	t.Helper()
	for _, k := range envKeys {
		t.Setenv(k, kv[k])
	}
}

// cfg mirrors the bash test's env_common: MAX_AGE=300, Telegram off unless withTG.
func (r *rig) cfg(withTG bool, mod func(*Config)) Config {
	setEnv(r.t, map[string]string{"API": r.ft.URL, "FREQTRADE__API_SERVER__USERNAME": "u",
		"FREQTRADE__API_SERVER__PASSWORD": "p", "MAX_AGE": "300", "FAILS_BEFORE_ALERT": "3"})
	c, err := ConfigFromEnv()
	if err != nil {
		r.t.Fatal(err)
	}
	if withTG {
		c.Telegram, c.Chat = telegram.Client{API: r.tg.URL, Token: "t"}, "42"
	}
	if mod != nil {
		mod(&c)
	}
	return c
}

// run polls `polls` times (or once with cfg.RunOnce). before(n) runs before sleep n
// returns, i.e. between poll n and poll n+1. It returns the log text.
func (r *rig) run(c Config, polls int, before func(n int)) string {
	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, nil))
	n := 0
	sleep := func(ctx context.Context, d time.Duration) error {
		n++
		if n >= polls {
			return errors.New("done")
		}
		if before != nil {
			before(n)
		}
		r.clk.advance(d)
		return nil
	}
	New(c, log, r.clk.Now, sleep).Run(context.Background())
	return buf.String()
}

func count(logs, sub string) int {
	n := 0
	for _, l := range strings.Split(logs, "\n") {
		if strings.Contains(l, sub) {
			n++
		}
	}
	return n
}

func firstLine(logs, sub string) int {
	for i, l := range strings.Split(logs, "\n") {
		if strings.Contains(l, sub) {
			return i
		}
	}
	return -1
}

func once(c *Config) { c.RunOnce = true }

func TestHealthyBotDoesNotAlert(t *testing.T) { // case 1
	r := newRig(t)
	out := r.run(r.cfg(false, once), 1, nil)
	if count(out, "ALERT") != 0 {
		t.Errorf("alerted on a healthy bot:\n%s", out)
	}
	if count(out, "msg=started") != 1 {
		t.Errorf("no startup line:\n%s", out)
	}
}

func TestStallAlertsOnceAfterDebounce(t *testing.T) { // case 2
	r := newRig(t)
	r.state.Store("stale")
	out := r.run(r.cfg(true, nil), 5, nil)
	first, alert := firstLine(out, `msg="check failed`), firstLine(out, "ALERT:")
	if first < 0 {
		t.Fatalf("did not detect a 99999s-old last_process:\n%s", out)
	}
	if alert < 0 {
		t.Fatalf("never alerted on a persistent stall:\n%s", out)
	}
	if count(out, `msg="check failed`) < 3 || alert <= first {
		t.Errorf("alerted before FAILS_BEFORE_ALERT=3 failures, so a routine restart would page:\n%s", out)
	}
	if !strings.Contains(out, `msg="check failed (3/3)"`) {
		t.Errorf("failure counter not logged as n/N:\n%s", out)
	}
	if n := count(out, "ALERT:"); n != 1 {
		t.Errorf("ALERT: logged %d times while still bad, want 1:\n%s", n, out)
	}
	msgs := r.messages()
	if len(msgs) != 1 || msgs[0] != "PROBLEM: bot has not processed for 99999s (limit 300s) -- loop stalled" {
		t.Errorf("telegram messages = %q", msgs)
	}
}

func TestRecoveryReportedOnce(t *testing.T) { // case 3
	r := newRig(t)
	r.state.Store("stale")
	c := r.cfg(true, func(c *Config) { c.FailsBeforeAlert = 2 })
	out := r.run(c, 8, func(n int) {
		if n == 4 {
			r.state.Store("fresh")
		}
	})
	if n := count(out, "RECOVERED"); n != 1 {
		t.Errorf("recovery reported %d times, want 1:\n%s", n, out)
	}
	msgs := r.messages()
	if len(msgs) != 2 || !strings.HasPrefix(msgs[0], "PROBLEM: ") ||
		msgs[1] != "OK: bot recovered - responding and processing again" {
		t.Errorf("telegram messages = %q", msgs)
	}
}

func TestRecoveryWithoutDeliveredPageIsSilent(t *testing.T) {
	r := newRig(t)
	r.state.Store("stale")
	c := r.cfg(true, func(c *Config) { c.FailsBeforeAlert = 10 })
	out := r.run(c, 6, func(n int) {
		if n == 3 {
			r.state.Store("fresh")
		}
	})
	if count(out, "RECOVERED") != 0 || len(r.messages()) != 0 {
		t.Errorf("announced recovery from an incident that never paged:\n%s\n%q", out, r.messages())
	}
}

func TestUnreachableAPIIsAFailureNotACrash(t *testing.T) { // case 4
	r := newRig(t)
	c := r.cfg(false, once)
	r.ft.Close()
	out := r.run(c, 1, nil)
	if count(out, "no response from "+c.API+"/api/v1/health") != 1 {
		t.Errorf("did not report an unreachable API:\n%s", out)
	}
	if count(out, "HTTP 000") != 1 {
		t.Errorf("balance check on an unreachable API should report HTTP 000:\n%s", out)
	}
}

func TestMainRunOnceExitsCleanly(t *testing.T) { // case 4, exit code
	r := newRig(t)
	url := r.ft.URL
	r.ft.Close()
	t.Setenv("API", url)
	t.Setenv("FREQTRADE__API_SERVER__USERNAME", "u")
	t.Setenv("FREQTRADE__API_SERVER__PASSWORD", "p")
	t.Setenv("FREQTRADE__TELEGRAM__TOKEN", "")
	t.Setenv("RUN_ONCE", "1")
	if rc := Main(context.Background(), nil); rc != 0 {
		t.Errorf("Main exited %d", rc)
	}
}

func TestMainRequiresCredentials(t *testing.T) {
	t.Setenv("FREQTRADE__API_SERVER__USERNAME", "")
	t.Setenv("FREQTRADE__API_SERVER__PASSWORD", "")
	if rc := Main(context.Background(), nil); rc == 0 {
		t.Error("Main ran without API credentials")
	}
}

func TestBadAPICredentialsAreNoResponse(t *testing.T) {
	r := newRig(t)
	out := r.run(r.cfg(false, func(c *Config) { c.RunOnce, c.Pass = true, "wrong" }), 1, nil)
	if count(out, "no response from") != 1 {
		t.Errorf("a 401 from health should count as no response:\n%s", out)
	}
}

func TestNullLastProcessIsNotProcessing(t *testing.T) { // case 5
	for _, state := range []string{"starting", "null_old", "garbage"} {
		t.Run(state, func(t *testing.T) {
			r := newRig(t)
			r.state.Store(state)
			out := r.run(r.cfg(false, once), 1, nil)
			if count(out, "not processing yet (no last_process_ts)") != 1 {
				t.Errorf("did not treat %s as not-processing:\n%s", state, out)
			}
			if count(out, "ALERT:") != 0 {
				t.Errorf("paged on a single poll:\n%s", out)
			}
			want := "unexpected"
			if state != "garbage" {
				want = "bot_startup_ts"
			}
			if count(out, want) != 1 {
				t.Errorf("did not log the full body for diagnosis:\n%s", out)
			}
		})
	}
}

func TestDefaults(t *testing.T) { // case 5b, and every default from watch.sh
	setEnv(t, map[string]string{"FREQTRADE__API_SERVER__USERNAME": "x", "FREQTRADE__API_SERVER__PASSWORD": "x"})
	c, err := ConfigFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	want := Config{
		API: "http://freqtrade:8989", User: "x", Pass: "x", MaxAge: 900, Interval: time.Minute,
		FailsBeforeAlert: 15, CredsCheckEvery: 5, CredsFailsBeforeAlert: 3, RealertEvery: 10800,
		Telegram: telegram.Client{API: "https://api.telegram.org"},
	}
	if c != want {
		t.Errorf("defaults\n got %+v\nwant %+v", c, want)
	}
	// 24 sampled restarts took 301-331s. At 60s polls that is at most 6 failures.
	if polls := 331/60 + 1; c.FailsBeforeAlert <= polls {
		t.Errorf("default threshold %d does not cover a %d-poll restart", c.FailsBeforeAlert, polls)
	}
}

func TestConfigRejectsBadNumbers(t *testing.T) {
	for _, kv := range [][2]string{{"MAX_AGE", "abc"}, {"INTERVAL", "-1"}, {"CREDS_CHECK_EVERY", "0"}} {
		setEnv(t, map[string]string{kv[0]: kv[1], "FREQTRADE__API_SERVER__USERNAME": "x",
			"FREQTRADE__API_SERVER__PASSWORD": "x"})
		if _, err := ConfigFromEnv(); err == nil {
			t.Errorf("%s=%s accepted", kv[0], kv[1])
		}
	}
}

func TestDeadExchangeCredentialsAlertSeparately(t *testing.T) { // case 6
	r := newRig(t)
	r.credsBad.Store(true)
	c := r.cfg(false, func(c *Config) { c.RunOnce, c.FailsBeforeAlert, c.CredsFailsBeforeAlert = true, 15, 1 })
	out := r.run(c, 1, nil)
	if count(out, "no working exchange access") < 1 {
		t.Errorf("missed dead credentials, the 2026-08-05 -2015 incident would pass silently:\n%s", out)
	}
	if count(out, "HTTP 500") < 1 {
		t.Errorf("did not report the balance status code:\n%s", out)
	}
	if count(out, "ALERT:") != 1 {
		t.Errorf("did not page on dead credentials:\n%s", out)
	}
	if count(out, `msg="check failed`) != 0 {
		t.Errorf("conflated it with the loop being stalled:\n%s", out)
	}
	r.credsBad.Store(false)
	if out := r.run(c, 1, nil); count(out, "ALERT") != 0 {
		t.Errorf("still paging with working credentials:\n%s", out)
	}
}

func TestCredsRecovery(t *testing.T) {
	r := newRig(t)
	r.credsBad.Store(true)
	c := r.cfg(true, func(c *Config) { c.CredsCheckEvery, c.CredsFailsBeforeAlert = 1, 2 })
	out := r.run(c, 4, func(n int) {
		if n == 2 {
			r.credsBad.Store(false)
		}
	})
	if count(out, "RECOVERED: exchange access works again") != 1 {
		t.Errorf("no creds recovery line:\n%s", out)
	}
	msgs := r.messages()
	if len(msgs) != 2 || !strings.HasPrefix(msgs[0], "PROBLEM: cannot query balance (HTTP 500)") ||
		msgs[1] != "OK: exchange access recovered" {
		t.Errorf("telegram messages = %q", msgs)
	}
}

func TestCredsCheckCadence(t *testing.T) {
	for _, tc := range []struct{ every, polls, want int }{{5, 11, 3}, {5, 5, 1}, {1, 4, 4}} {
		r := newRig(t)
		r.run(r.cfg(false, func(c *Config) { c.CredsCheckEvery = tc.every }), tc.polls, nil)
		if got := int(r.balHits.Load()); got != tc.want {
			t.Errorf("every=%d polls=%d: balance polled %d times, want %d", tc.every, tc.polls, got, tc.want)
		}
	}
}

func TestFailedPageIsRetried(t *testing.T) { // case 7
	r := newRig(t)
	r.state.Store("stale")
	r.tgBad.Store(true)
	c := r.cfg(true, func(c *Config) { c.FailsBeforeAlert, c.CredsCheckEvery = 2, 1000 })
	out := r.run(c, 9, func(n int) {
		if n == 5 {
			r.tgBad.Store(false)
		}
	})
	if n := count(out, "ALERT NOT DELIVERED"); n < 2 {
		t.Errorf("did not retry the failed send (%d attempts):\n%s", n, out)
	}
	if n := count(out, "ALERT:"); n != 1 {
		t.Errorf("ALERT: logged %d times, want once after delivery:\n%s", n, out)
	}
	if count(out, "ALERT REPEAT") != 0 {
		t.Errorf("repeated without REALERT_EVERY elapsing:\n%s", out)
	}
	if strings.Contains(out, "/bott/") {
		t.Errorf("token leaked into logs:\n%s", out)
	}
}

func TestPersistentFailureRepagesEveryRealert(t *testing.T) { // case 8
	r := newRig(t)
	r.state.Store("stale")
	c := r.cfg(true, func(c *Config) { c.FailsBeforeAlert, c.RealertEvery, c.CredsCheckEvery = 1, 120, 1000 })
	out := r.run(c, 8, nil) // polls at 0, 60, ... 420s
	if n := count(out, "ALERT REPEAT"); n != 3 {
		t.Errorf("ALERT REPEAT %d times in 7 minutes at REALERT_EVERY=120s, want 3:\n%s", n, out)
	}
	msgs := r.messages()
	want := []string{"PROBLEM: ", "STILL BROKEN for 2m: ", "STILL BROKEN for 4m: ", "STILL BROKEN for 6m: "}
	if len(msgs) != len(want) {
		t.Fatalf("telegram messages = %q", msgs)
	}
	for i := range want {
		if !strings.HasPrefix(msgs[i], want[i]) {
			t.Errorf("message %d = %q, want prefix %q", i, msgs[i], want[i])
		}
	}
}

func TestRunStopsOnCancel(t *testing.T) {
	r := newRig(t)
	c := r.cfg(false, func(c *Config) { c.Interval = time.Hour })
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		New(c, slog.New(slog.DiscardHandler), time.Now, clock.Sleep).Run(ctx)
		close(done)
	}()
	time.Sleep(50 * time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run ignored context cancellation")
	}
}
