package updater

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/beanbocchi/money-generator/internal/runx"
	"github.com/beanbocchi/money-generator/internal/telegram"
)

// 5m BTC klines: [openTime, open, high, low, close, ...]. calm ranges 0.4%, wild 6%.
const (
	calmKlines = `[[1,"100","100.2","99.8","100",0],[2,"100","100.1","99.9","100",0],[3,"100","100.2","99.9","100",0]]`
	wildKlines = `[[1,"100","100","99","100",0],[2,"100","103","99","98",0],[3,"98","99","97","100",0]]`
)

// fixture is tests/test_updater.sh's setup(): an upstream repo, a clone of it as the
// bot's NFI dir, a state dir, kline files served over HTTP and a fake docker.
type fixture struct {
	t        *testing.T
	dir      string
	upstream string
	nfi      string
	state    string
	docker   *fakeDocker
	git      *countingGit
	tg       *fakeTelegram    // nil unless started
	klines   *httptest.Server // serves the files in dir
}

// envKeys is every variable the updater and its host tools read. setEnv blanks them
// all first (empty means default), so the developer's own environment, a real bot
// token say, never reaches a test.
var envKeys = []string{
	"NFI_DIR", "STATE_DIR", "DOCKER", "TARGET_CONTAINER", "STRATEGY_MODULE", "STRATEGY_FILE",
	"UPDATE_INTERVAL", "DRY_RUN", "RUN_ONCE", "WATCH_PATHS", "RISK_PATTERN", "MAX_RESTARTS_PER_DAY",
	"MIN_RESTART_GAP", "VOL_LIMIT_PCT", "VOL_URL", "VOL_LONG_LIMIT_PCT", "VOL_LONG_URL",
	"UNHEALTHY_PATTERN", "UNHEALTHY_WINDOW", "TELEGRAM_API", "FREQTRADE__TELEGRAM__TOKEN",
	"FREQTRADE__TELEGRAM__CHAT_ID", "APPROVAL_TELEGRAM_TOKEN", "APPROVAL_CHAT_ID", "APPROVER_IDS",
	"APPROVAL_SELFTEST", "SELFTEST_SECONDS", "UPDATER_CONTAINER", "MG_ROOT",
}

func setEnv(t *testing.T, env map[string]string) {
	t.Helper()
	for _, k := range envKeys {
		t.Setenv(k, "")
	}
	for k, v := range env {
		t.Setenv(k, v)
	}
}

func gitT(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return string(b)
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	d := t.TempDir()
	f := &fixture{t: t, dir: d, upstream: filepath.Join(d, "upstream"), nfi: filepath.Join(d, "nfi"),
		state: filepath.Join(d, "state"), docker: &fakeDocker{}, git: &countingGit{}}
	gitT(t, d, "init", "-q", "-b", "main", f.upstream)
	gitT(t, f.upstream, "config", "user.email", "t@example.com")
	gitT(t, f.upstream, "config", "user.name", "test")
	gitT(t, f.upstream, "config", "commit.gpgsign", "false")
	writeFile(t, filepath.Join(f.upstream, "NostalgiaForInfinityX7.py"), "class X: pass\n")
	writeFile(t, filepath.Join(f.upstream, "configs/blacklist-binance.json"), "{\"a\":1}\n")
	writeFile(t, filepath.Join(f.upstream, "README.md"), "docs\n")
	gitT(t, f.upstream, "add", "-A")
	gitT(t, f.upstream, "commit", "-qm", "init")
	gitT(t, d, "clone", "-q", f.upstream, f.nfi)
	for _, p := range []string{f.state} {
		if err := os.MkdirAll(p, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	writeFile(t, filepath.Join(d, "calm.json"), calmKlines+"\n")
	writeFile(t, filepath.Join(d, "wild.json"), wildKlines+"\n")
	f.klines = httptest.NewServer(http.FileServer(http.Dir(d)))
	t.Cleanup(f.klines.Close)
	return f
}

// newFuseFixture is fuse_setup(): the running commit already has the realistic
// strategy shape.
func newFuseFixture(t *testing.T) *fixture {
	f := newFixture(t)
	f.setRunning(strategy("True", "800", calcLine))
	return f
}

// setRunning commits content as the strategy upstream and pulls it into the clone.
func (f *fixture) setRunning(content string) {
	f.t.Helper()
	writeFile(f.t, filepath.Join(f.upstream, "NostalgiaForInfinityX7.py"), content+"\n")
	gitT(f.t, f.upstream, "commit", "-qam", "running strategy")
	gitT(f.t, f.nfi, "pull", "-q")
}

func (f *fixture) commit(file, content string) {
	f.t.Helper()
	writeFile(f.t, filepath.Join(f.upstream, file), content+"\n")
	gitT(f.t, f.upstream, "add", "-A")
	gitT(f.t, f.upstream, "commit", "-qm", "change "+file)
}

func (f *fixture) head() string { return gitT(f.t, f.nfi, "rev-parse", "HEAD") }

// url serves the file name in the fixture dir; a missing file is a 404.
func (f *fixture) url(name string) string { return f.klines.URL + "/" + name }

func (f *fixture) statePath(name string) string { return filepath.Join(f.state, name) }

const calcLine = `      "num_cores_indicators_calc",`

// strategy is a minimal strategy shaped like upstream's: class-level settings, an
// __init__ holding NFI_SAFE_PARAMETERS, and a second class whose attributes must be
// ignored.
func strategy(stops, startup, extraSafe string) string {
	return fmt.Sprintf(`class NostalgiaForInfinityX7(IStrategy):
  stoploss = -0.99
  system_v3_2_stops_enable = %s
  startup_candle_count = %s

  def __init__(self, config):
    NFI_SAFE_PARAMETERS = [
      "futures_mode_leverage",
%s
    ]

class Cache:
  stoploss = 1`, stops, startup, extraSafe)
}

// baseEnv is run_once()'s environment.
func (f *fixture) baseEnv() map[string]string {
	return map[string]string{
		"NFI_DIR":                    f.nfi,
		"STATE_DIR":                  f.state,
		"DOCKER":                     "docker",
		"TARGET_CONTAINER":           "bottrade",
		"STRATEGY_MODULE":            "NFIX7Risk",
		"WATCH_PATHS":                "NostalgiaForInfinityX7.py configs/",
		"RUN_ONCE":                   "1",
		"UPDATE_INTERVAL":            "1",
		"FREQTRADE__TELEGRAM__TOKEN": "",
		"VOL_URL":                    f.url("calm.json"),
		"VOL_LONG_URL":               f.url("calm.json"),
		"MIN_RESTART_GAP":            "0",
	}
}

// updater builds an Updater from the base environment plus kv pairs ("KEY=value").
func (f *fixture) updater(kv ...string) (*Updater, *syncBuffer) {
	f.t.Helper()
	env := f.baseEnv()
	for _, s := range kv {
		k, v, _ := strings.Cut(s, "=")
		env[k] = v
	}
	setEnv(f.t, env)
	cfg, err := LoadConfig()
	if err != nil {
		f.t.Fatal(err)
	}
	logs := &syncBuffer{}
	u := New(cfg, Deps{
		Git:      f.git,
		Docker:   f.docker,
		Notify:   telegram.Client{API: cfg.TelegramAPI, Token: cfg.NotifyToken},
		Approval: telegram.Client{API: cfg.TelegramAPI, Token: cfg.ApprovalToken},
		Log:      slog.New(slog.NewTextHandler(logs, nil)),
	})
	return u, logs
}

// runOnce is run_once(): one full Run with RUN_ONCE=1. It returns the log output.
func (f *fixture) runOnce(kv ...string) string {
	f.t.Helper()
	u, logs := f.updater(kv...)
	if rc := u.Run(context.Background()); rc != 0 {
		f.t.Fatalf("Run returned %d\n%s", rc, logs)
	}
	return logs.String()
}

// runLoop is run_loop(): the real loop with RUN_ONCE=0 and UPDATE_INTERVAL=600, stopped
// once until() is true or after limit. It fails the test if Run does not return
// promptly after its context is cancelled.
func (f *fixture) runLoop(limit time.Duration, until func() bool, kv ...string) string {
	f.t.Helper()
	u, logs := f.updater(append([]string{"RUN_ONCE=0", "UPDATE_INTERVAL=600"}, kv...)...)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan int, 1)
	go func() { done <- u.Run(ctx) }()
	deadline := time.Now().Add(limit)
	for time.Now().Before(deadline) && !until() {
		time.Sleep(20 * time.Millisecond)
	}
	cancel()
	select {
	case rc := <-done:
		if rc != 0 {
			f.t.Errorf("Run returned %d", rc)
		}
	case <-time.After(5 * time.Second):
		f.t.Fatal("Run did not return after cancel")
	}
	return logs.String()
}

// countingGit runs git for real and records each call's subcommand.
type countingGit struct {
	mu   sync.Mutex
	cmds []string
}

func (g *countingGit) Run(ctx context.Context, name string, args ...string) (string, error) {
	g.mu.Lock()
	if len(args) > 2 {
		g.cmds = append(g.cmds, args[2]) // after -C <dir>
	}
	g.mu.Unlock()
	return runx.Exec{}.Run(ctx, name, args...)
}

// count reports how many calls ran subcommand cmd, and forgets them all.
func (g *countingGit) count(cmd string) int {
	g.mu.Lock()
	defer g.mu.Unlock()
	n := 0
	for _, c := range g.cmds {
		if c == cmd {
			n++
		}
	}
	g.cmds = nil
	return n
}

// syncBuffer is a bytes.Buffer safe for concurrent use.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// fakeDocker is stub_docker(): it records every call as one line of space-joined
// arguments; the import guard, config guard and restart fail on demand, and logs
// returns botLog.
type fakeDocker struct {
	mu          sync.Mutex
	calls       []string
	importFail  bool
	configFail  bool
	restartFail bool
	stopFail    bool
	logsFail    bool
	botLog      string
}

func (d *fakeDocker) Run(_ context.Context, name string, args ...string) (string, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	line := strings.Join(args, " ")
	d.calls = append(d.calls, line)
	fail := func() (string, error) { return "", fmt.Errorf("%s %s: exit status 1", name, line) }
	switch {
	case len(args) > 0 && args[0] == "logs":
		if d.logsFail {
			return "Error response from daemon: No such container: bottrade\n", fmt.Errorf("logs: exit status 1")
		}
		return d.botLog, nil
	case len(args) > 0 && args[0] == "exec" && strings.Contains(line, "show-config"):
		if d.configFail {
			return fail()
		}
	case len(args) > 0 && args[0] == "exec":
		if d.importFail {
			return fail()
		}
	case len(args) > 0 && args[0] == "restart":
		if d.restartFail {
			return fail()
		}
	case len(args) > 0 && args[0] == "stop":
		if d.stopFail {
			return fail()
		}
	}
	return "", nil
}

func (d *fakeDocker) log() string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return strings.Join(d.calls, "\n")
}

func (d *fakeDocker) restarted() bool { return strings.Contains(d.log(), "restart bottrade") }

func (d *fakeDocker) reset() {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.calls = nil
}

// fakeTelegram is tests/fake_telegram.py: it records every call and serves queued
// updates from getUpdates, honouring the offset.
type fakeTelegram struct {
	srv     *httptest.Server
	mu      sync.Mutex
	calls   []tgCall
	updates []map[string]any
	polls   int // getUpdates calls
	fail    bool
}

type tgCall struct {
	Method string
	Token  string
	Params url.Values
}

func newFakeTelegram(t *testing.T) *fakeTelegram {
	ft := &fakeTelegram{}
	ft.srv = httptest.NewServer(http.HandlerFunc(ft.handle))
	t.Cleanup(ft.srv.Close)
	return ft
}

func (ft *fakeTelegram) handle(w http.ResponseWriter, r *http.Request) {
	_ = r.ParseForm()
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/"), "/")
	token := strings.TrimPrefix(parts[0], "bot")
	method := parts[len(parts)-1]
	ft.mu.Lock()
	ft.calls = append(ft.calls, tgCall{Method: method, Token: token, Params: r.Form})
	fail := ft.fail
	var result any = true
	var wait time.Duration
	switch method {
	case "getUpdates":
		ft.polls++
		off, _ := strconv.ParseInt(r.Form.Get("offset"), 10, 64)
		ups := []map[string]any{}
		for _, u := range ft.updates {
			if int64(u["update_id"].(int)) >= off {
				ups = append(ups, u)
			}
		}
		if len(ups) == 0 {
			if s, _ := strconv.ParseFloat(r.Form.Get("timeout"), 64); s > 0 {
				wait = 100 * time.Millisecond
			}
		}
		result = ups
	case "sendMessage":
		result = map[string]any{"message_id": 42}
	case "getMe":
		result = map[string]any{"username": "approvalbot"}
	case "getWebhookInfo":
		result = map[string]any{"url": ""}
	}
	ft.mu.Unlock()
	if fail {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte(`{"ok":false,"description":"down"}`))
		return
	}
	time.Sleep(wait)
	_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "result": result})
}

func (ft *fakeTelegram) api() string { return ft.srv.URL }

func (ft *fakeTelegram) reset() {
	ft.mu.Lock()
	defer ft.mu.Unlock()
	ft.calls, ft.updates, ft.polls = nil, nil, 0
}

func (ft *fakeTelegram) callsOf(method string) []tgCall {
	ft.mu.Lock()
	defer ft.mu.Unlock()
	var out []tgCall
	for _, c := range ft.calls {
		if c.Method == method {
			out = append(out, c)
		}
	}
	return out
}

func (ft *fakeTelegram) pollCount() int {
	ft.mu.Lock()
	defer ft.mu.Unlock()
	return ft.polls
}

// press queues one button press as update 7, like press() in the shell test.
func (ft *fakeTelegram) press(data string, from, chat int64) {
	ft.mu.Lock()
	defer ft.mu.Unlock()
	ft.updates = []map[string]any{{
		"update_id": 7,
		"callback_query": map[string]any{
			"id": "cq1", "from": map[string]any{"id": from}, "data": data,
			"message": map[string]any{"message_id": 42, "chat": map[string]any{"id": chat}},
		},
	}}
}

// consumedPress reports that the queued press was taken (offset saved as 8) and the
// listener has polled again since, so it has finished handling it.
func (f *fixture) consumedPress(pollsBefore int) func() bool {
	return func() bool {
		return strings.TrimSpace(readFile(f.t, f.statePath("tg_offset"))) == "8" && f.tg.pollCount() >= pollsBefore+2
	}
}
