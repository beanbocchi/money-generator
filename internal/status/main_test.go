package status

import (
	"bytes"
	"context"
	"flag"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/beanbocchi/money-generator/internal/envfile"
	"github.com/beanbocchi/money-generator/internal/sqlitex"
)

var update = flag.Bool("update", false, "rewrite testdata/*.golden")

var fixedNow = time.Date(2026, 11, 21, 11, 38, 37, 0, time.UTC)

// fakeRunner answers the commands the report runs and fails the test on any other, so a
// stray docker call (restart, stop) cannot slip in.
type fakeRunner struct {
	t   *testing.T
	out map[string]string // command line -> stdout; a missing key exits non-zero
	ok  map[string]bool   // commands that are expected even though they fail
	mu  sync.Mutex
	ran []string
}

var knownCommands = []string{
	"docker compose ps --format {{.Name}}\t{{.Status}}",
	"docker logs bottrade-watchdog",
	"docker logs bottrade-updater",
	"docker inspect -f {{.State.Running}} bottrade-updater",
	"pgrep -f lab/queue\\.sh|(^|/)mg queue",
	"df -h /home",
}

func (f *fakeRunner) Run(_ context.Context, name string, args ...string) (string, error) {
	key := strings.Join(append([]string{name}, args...), " ")
	f.mu.Lock()
	f.ran = append(f.ran, key)
	f.mu.Unlock()
	known := false
	for _, k := range knownCommands {
		known = known || k == key
	}
	if !known {
		f.t.Errorf("unexpected command %q", key)
	}
	if out, ok := f.out[key]; ok {
		return out, nil
	}
	return "", fmt.Errorf("%s: exit status 1", key)
}

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// fixtureDB builds a trade database shaped like freqtrade's (the columns the report
// reads, with the same declared types).
func fixtureDB(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	db, err := sqlitex.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	stmts := []string{
		`create table trades (id INTEGER PRIMARY KEY, pair VARCHAR(25), is_open BOOLEAN NOT NULL,
			close_profit_abs FLOAT, open_date DATETIME, close_date DATETIME, exit_reason VARCHAR(255))`,
		`create index ix_trades_is_open on trades (is_open)`,
		`create table orders (id INTEGER PRIMARY KEY, ft_trade_id INTEGER, ft_order_side VARCHAR(25),
			status VARCHAR(255))`,
		`insert into trades values
			(1, 'ZEC/USDT:USDT',  0,   3.19,   '2026-11-01 10:00:00', '2026-11-15 00:05:12.123456', 'exit_long_tc_d_2_49 ( 141 142 143 144 )'),
			(2, 'APE/USDT:USDT',  0,  -32.05,  '2026-11-02 10:00:00', '2026-11-03 08:00:00', 'stop_loss'),
			(3, 'ASTER/USDT:USDT',0,   1.55,   '2026-11-03 10:00:00', '2026-11-15 20:17:00', 'exit_long_normal_d_1_63 ( 2 4 '),
			(4, 'SHELL/USDT:USDT',0,   0.0,    '2026-11-04 10:00:00', '2026-11-18 07:31:59', NULL),
			(5, 'ZIL/USDT:USDT',  0,   2.875,  '2026-11-05 10:00:00', '2026-11-20 06:32:00', 'exit_long_normal_d_2_96 ( 2 )'),
			(6, 'IRYS/USDT:USDT', 0,   1.70,   '2026-11-06 10:00:00', '2026-11-20 05:24:00', 'exit_long_scalp_d_1_60 ( 61 16 99 )'),
			(7, 'OLD/USDT:USDT',  0,   0.1,    '2026-01-01 10:00:00', '2026-01-02 00:00:00', 'roi'),
			(238, 'KAITO/USDT:USDT', 1, NULL, '2026-08-12 11:11:05.5', NULL, NULL),
			(401, 'UNI/USDT:USDT',   1, NULL, '2026-11-20 15:29:00', NULL, NULL),
			(402, 'BTC/USDT:USDT',   1, NULL, '2026-11-21 01:00:00', NULL, NULL),
			(403, 'ETH/USDT:USDT',   1, NULL, '2026-11-21 02:00:00', NULL, NULL),
			(404, '1000000BABYDOGE/USDT:USDT', 1, NULL, '2026-11-21 03:00:00', NULL, NULL)`,
		// #238 ground 28 times; canceled entries and exits do not count.
		`with recursive s(i) as (select 1 union all select i+1 from s where i < 28)
			insert into orders (ft_trade_id, ft_order_side, status) select 238, 'buy', 'closed' from s`,
		`insert into orders (ft_trade_id, ft_order_side, status) values
			(238, 'buy', 'canceled'), (238, 'sell', 'closed'),
			(401, 'buy', 'closed'),
			(402, 'sell', 'closed'), (402, 'sell', 'closed'), (402, 'buy', 'closed'),
			(403, 'buy', 'closed'), (403, 'buy', 'open')`,
	}
	for _, s := range stmts {
		if _, err := db.Exec(s); err != nil {
			t.Fatalf("%s: %v", s, err)
		}
	}
}

const statusJSON = `[
 {"trade_id":238,"pair":"KAITO/USDT:USDT","is_short":false,"stake_amount":20.53,"profit_abs":-6.59,
  "profit_ratio":-0.3211,"liquidation_price":0.30,"current_rate":0.47,"realized_profit":-28.78,
  "open_date":"2026-08-12 11:11:05"},
 {"trade_id":401,"pair":"UNI/USDT:USDT","is_short":false,"stake_amount":65.67,"profit_abs":-1.25,
  "profit_ratio":-0.019,"liquidation_price":4.0,"current_rate":7.5,"realized_profit":0.004,
  "open_date":"2026-11-20 15:29:00"},
 {"trade_id":402,"pair":"BTC/USDT:USDT","is_short":true,"stake_amount":5,"profit_abs":-2.5,
  "profit_ratio":-0.5,"liquidation_price":105,"current_rate":100,"realized_profit":null,
  "open_date":"2026-11-21 01:00:00"},
 {"trade_id":403,"pair":"ETH/USDT:USDT","is_short":false,"stake_amount":10.005,"profit_abs":0.125,
  "profit_ratio":0.0125,"liquidation_price":null,"current_rate":3000,"realized_profit":1.5,
  "open_date":"2026-11-21 02:00:00"},
 {"trade_id":404,"pair":"1000000BABYDOGE/USDT:USDT","is_short":false,"stake_amount":7,"profit_abs":0.125,
  "profit_ratio":0.0179,"liquidation_price":0.0012,"current_rate":0.0013,"realized_profit":-0.009,
  "open_date":"2026-11-21 03:00:00"}
]`

// fixtureRoot lays out a repo root with everything the report reads.
func fixtureRoot(t *testing.T) string {
	root := t.TempDir()
	write(t, filepath.Join(root, ".env"), "FREQTRADE__BOT_NAME=testbot\nFREQTRADE__EXCHANGE__NAME=binance\n"+
		"FREQTRADE__TRADING_MODE=futures\nFREQTRADE__API_SERVER__USERNAME=u\nFREQTRADE__API_SERVER__PASSWORD='p w'\n")
	fixtureDB(t, filepath.Join(root, "user_data", "testbot_binance_futures-tradesv3.sqlite"))

	var log strings.Builder
	for i := range 600 {
		level := "INFO"
		if i%37 == 0 { // 0, 37, 74 fall before the last 500; 111..592 inside
			level = "ERROR"
		}
		fmt.Fprintf(&log, "2026-11-21 10:00:%02d,000 - freqtrade.worker - %s - line %d\n", i%60, level, i)
	}
	write(t, filepath.Join(root, "user_data", "logs", "bot.log"), log.String())

	for _, d := range []string{"20261120T175530Z", "20261121T055530Z", ".tmp-1-20261121T115530Z"} {
		if err := os.MkdirAll(filepath.Join(root, "backups", "db", d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	write(t, filepath.Join(root, "backups", "db", "README"), "not a snapshot")
	write(t, filepath.Join(root, "services", "updater", "state", "pending_risk"), "stoploss\n")

	write(t, filepath.Join(root, "lab", "logs", "queue-h9-stop.log"), "[queue] first\n[queue] older run done\n")
	write(t, filepath.Join(root, "lab", "logs", "queue-h10.log"), "[queue] start\n[queue 2026-11-21T09:00:00Z] job 3/8 running")
	old := fixedNow.Add(-6 * time.Hour)
	if err := os.Chtimes(filepath.Join(root, "lab", "logs", "queue-h9-stop.log"), old, old); err != nil {
		t.Fatal(err)
	}
	return root
}

func fakeAPI(t *testing.T, health, balance, status string) *httptest.Server {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if u, p, ok := r.BasicAuth(); !ok || u != "u" || p != "p w" {
			http.Error(w, `{"detail":"Unauthorized"}`, http.StatusUnauthorized)
			return
		}
		switch r.URL.Path {
		case "/api/v1/health":
			fmt.Fprint(w, health)
		case "/api/v1/balance":
			if balance == "" {
				http.Error(w, `{"error":"Error querying /api/v1/balance: Invalid API-key"}`, http.StatusInternalServerError)
				return
			}
			fmt.Fprint(w, balance)
		case "/api/v1/status":
			fmt.Fprint(w, status)
		case "/api/v1/profit":
			fmt.Fprint(w, `{"profit_all_coin":-1}`)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// report runs the reporter for root with its .env, the fake runner for every command,
// and api as the freqtrade server unless api is empty.
func report(t *testing.T, root, api string, run *fakeRunner) string {
	t.Helper()
	env, _ := envfile.Read(filepath.Join(root, ".env"))
	r := New(root, env)
	if api != "" {
		r.API.BaseURL = api
	}
	var buf bytes.Buffer
	r.Out, r.Now, r.Loc = &buf, func() time.Time { return fixedNow }, time.FixedZone("ICT", 7*3600)
	r.Run, r.Logs = run, run
	if err := r.Report(context.Background()); err != nil {
		t.Fatal(err)
	}
	return buf.String()
}

func golden(t *testing.T, name, got string) {
	t.Helper()
	path := filepath.Join("testdata", name+".golden")
	if *update {
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v (run go test -update)", err)
	}
	if got != string(want) {
		t.Errorf("%s differs from %s:\n--- got\n%s\n--- want\n%s", name, path, got, want)
	}
}

func TestReportGoldenFull(t *testing.T) {
	root := fixtureRoot(t)
	health := fmt.Sprintf(`{"last_process":"2026-11-21 11:18:03+00:00","last_process_ts":%d,"bot_start_ts":1}`,
		fixedNow.Unix()-1234)
	api := fakeAPI(t, health, `{"currencies":[],"total":838.895,"symbol":"USDT"}`, statusJSON)
	run := &fakeRunner{t: t, out: map[string]string{
		"docker compose ps --format {{.Name}}\t{{.Status}}": "bottrade\tUp 6 hours (healthy)\nbottrade-watchdog\tUp 4 days\n",
		"docker logs bottrade-watchdog": `time=x level=ERROR msg="ALERT: no response from http://freqtrade:8989/api/v1/health"` + "\n" +
			`time=x level=ERROR msg="ALERT REPEAT (failing for 180m): x"` + "\n" +
			`time=x level=ERROR msg="ALERT NOT DELIVERED (retrying next poll): x"` + "\n" +
			"[watchdog 2026-10-12T00:00:00Z] ALERT: bot has not processed for 1776s\n",
		"docker logs bottrade-updater":                          "restarted bot\nno change\nrestarted bot\nrestarted bot\n",
		"docker inspect -f {{.State.Running}} bottrade-updater": "false\n",
		"df -h /home": "Filesystem      Size  Used Avail Use% Mounted on\n/dev/nvme0n1p2  468G  368G   77G  83% /home\n",
	}}
	golden(t, "full", report(t, root, api.URL, run))
}

func TestReportGoldenDegraded(t *testing.T) {
	// Nothing answers: API down, docker absent, no backups, no lab logs, empty database.
	root := t.TempDir()
	write(t, filepath.Join(root, ".env"), "FREQTRADE__BOT_NAME=b\nFREQTRADE__EXCHANGE__NAME=x\nFREQTRADE__TRADING_MODE=futures\n")
	dbPath := filepath.Join(root, "user_data", "b_x_futures-tradesv3.sqlite")
	if err := os.MkdirAll(filepath.Dir(dbPath), 0o755); err != nil {
		t.Fatal(err)
	}
	db, err := sqlitex.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`create table trades (id INTEGER PRIMARY KEY, pair TEXT, is_open BOOLEAN,
		close_profit_abs FLOAT, close_date DATETIME, exit_reason TEXT);
		create table orders (ft_trade_id INTEGER, ft_order_side TEXT, status TEXT)`); err != nil {
		t.Fatal(err)
	}
	db.Close()
	api := httptest.NewServer(http.NotFoundHandler())
	url := api.URL
	api.Close()
	run := &fakeRunner{t: t, out: map[string]string{knownCommands[4]: "4242\n"}}
	golden(t, "degraded", report(t, root, url, run))
}

func TestReportMissingDatabase(t *testing.T) {
	api := fakeAPI(t, `{"last_process_ts":null}`, `{"total":1}`, `[]`)
	root := t.TempDir()
	write(t, filepath.Join(root, ".env"), "FREQTRADE__API_SERVER__USERNAME=u\nFREQTRADE__API_SERVER__PASSWORD=p w\n")
	got := report(t, root, api.URL, &fakeRunner{t: t})
	for _, want := range []string{"   last_process age Nones\n", "   balance 1.00 USDT\n", "   trade database unreadable: ",
		"-- lab\n   queue: idle\n"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
	if strings.Contains(got, "updater: HOLDING") {
		t.Errorf("reported a held update without pending_risk:\n%s", got)
	}
}

func TestReportUsesListenPortAndNeverPrintsSecrets(t *testing.T) {
	root := fixtureRoot(t)
	api := fakeAPI(t, `{}`, `{"total":2}`, `[]`)
	port := api.URL[strings.LastIndex(api.URL, ":")+1:]
	f, err := os.OpenFile(filepath.Join(root, ".env"), os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	fmt.Fprintf(f, "FREQTRADE__API_SERVER__LISTEN_PORT=%s\n", port)
	f.Close()
	got := report(t, root, "", &fakeRunner{t: t})
	if !strings.Contains(got, "   health   HTTP 200\n") || !strings.Contains(got, "   balance 2.00 USDT\n") {
		t.Errorf("did not reach the API on 127.0.0.1:%s:\n%s", port, got)
	}
	if strings.Contains(got, "p w") {
		t.Error("printed the API password")
	}
}

func TestMainUsesRepoRoot(t *testing.T) {
	t.Setenv("MG_ROOT", t.TempDir()) // no .env.example: refuse instead of reporting on the wrong tree
	if rc := Main(context.Background(), nil); rc != 2 {
		t.Errorf("rc = %d", rc)
	}
}

func TestMainBadFlag(t *testing.T) {
	if rc := Main(context.Background(), []string{"-nope"}); rc != 2 {
		t.Errorf("rc = %d", rc)
	}
}

func TestLastProcessLine(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	for body, want := range map[string]string{
		`{"last_process_ts":999996}`:     "   last_process age 4s",
		`{"last_process_ts":999000}`:     "   last_process age 1000s   <-- STALE, expect a pairlist refresh or a real stall",
		`{"last_process_ts":999099.5}`:   "   last_process age 901s   <-- STALE, expect a pairlist refresh or a real stall",
		`{"last_process_ts":1000000}`:    "   last_process age 0s",
		`{"last_process_ts":null}`:       "   last_process age Nones",
		`{"last_process_ts":0}`:          "   last_process age Nones",
		`{}`:                             "   last_process age Nones",
		`{"last_process_ts":"999996"}`:   "   last_process: unreadable",
		`[1]`:                            "   last_process: unreadable",
		``:                               "   last_process: unreadable",
		`{"detail":"Not authenticated"}`: "   last_process age Nones",
	} {
		if got := lastProcessLine([]byte(body), now); got != want {
			t.Errorf("%s: got %q, want %q", body, got, want)
		}
	}
}

func TestNewestFileTiesByName(t *testing.T) {
	dir := t.TempDir()
	ts := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for _, n := range []string{"queue-b.log", "queue-a.log", "queue-c.log"} {
		write(t, filepath.Join(dir, n), "x\n")
		if err := os.Chtimes(filepath.Join(dir, n), ts, ts); err != nil {
			t.Fatal(err)
		}
	}
	if got := filepath.Base(newestFile(filepath.Join(dir, "queue-*.log"))); got != "queue-a.log" {
		t.Errorf("tie went to %s, want queue-a.log like ls -t", got)
	}
	if newestFile(filepath.Join(dir, "none-*.log")) != "" {
		t.Error("no match should be empty")
	}
}
