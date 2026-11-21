// Package status prints everything needed to judge whether the bot is healthy and what
// its positions are doing (was bin/status.sh). One call instead of six, and the knowledge
// lives here, not in a conversation.
//
// Grinding is counted from filled ENTRY ORDERS, never from max_stake_amount >
// stake_amount. That comparison is peak-versus-current stake, so it only fires when a
// position partially exits after growing; a trade that adds then closes in one exit looks
// like a single entry. Using it produced this project's worst error: a "grinding is
// capital-gated" conclusion built on 0 of 194 trades where the true count was 58 of 194.
// See docs/adr/rejected/0015-*.
//
// A human checklist reads these lines, so their formats are the ones bin/status.sh
// printed.
package status

import (
	"context"
	"database/sql"
	"flag"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/beanbocchi/money-generator/internal/envfile"
	"github.com/beanbocchi/money-generator/internal/fsx"
	"github.com/beanbocchi/money-generator/internal/ftapi"
	"github.com/beanbocchi/money-generator/internal/repo"
	"github.com/beanbocchi/money-generator/internal/runx"
	"github.com/beanbocchi/money-generator/internal/sqlitex"
)

// Reporter holds the dependencies of one report. Build it with New.
type Reporter struct {
	Root string // holds services/, user_data/logs/, backups/, lab/
	DB   string // the trade database
	API  ftapi.Client
	Out  io.Writer
	Now  func() time.Time
	Loc  *time.Location // "local" time in the header
	Run  runx.Runner    // returns stdout; stderr is dropped, as 2>/dev/null did
	Logs runx.Runner    // returns stdout and stderr merged, as 2>&1 did (docker logs)
}

// New builds the real reporter for the repository at root with its .env values.
func New(root string, env map[string]string) Reporter {
	port := env["FREQTRADE__API_SERVER__LISTEN_PORT"]
	if port == "" {
		port = "8989"
	}
	return Reporter{
		Root: root,
		// freqtrade names it <bot>_<exchange>_<mode>-tradesv3.sqlite.
		DB: filepath.Join(root, "user_data", fmt.Sprintf("%s_%s_%s-tradesv3.sqlite", env["FREQTRADE__BOT_NAME"],
			env["FREQTRADE__EXCHANGE__NAME"], env["FREQTRADE__TRADING_MODE"])),
		API: ftapi.Client{BaseURL: "http://127.0.0.1:" + port,
			User: env["FREQTRADE__API_SERVER__USERNAME"], Pass: env["FREQTRADE__API_SERVER__PASSWORD"]},
		Out: os.Stdout, Now: time.Now, Loc: time.Local,
		Run: runx.Exec{Dir: root}, Logs: runx.Exec{Combined: true},
	}
}

// Main prints the report for the repository holding this binary (or -root, or MG_ROOT).
func Main(ctx context.Context, args []string) int {
	fs := flag.NewFlagSet("status", flag.ContinueOnError)
	root := fs.String("root", "", "repository root (default: the repo holding bin/mg, or MG_ROOT)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *root == "" {
		r, err := repo.Root(".env.example")
		if err != nil {
			fmt.Fprintf(os.Stderr, "mg status: %v\n", err)
			return 2
		}
		*root = r
	}
	rc := 0
	env, err := envfile.Read(filepath.Join(*root, ".env"))
	if err != nil {
		fmt.Fprintf(os.Stderr, "mg status: %v\n", err)
		rc = 1
	}
	if err := New(*root, env).Report(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "mg status: %v\n", err)
		return 1
	}
	return rc
}

// Report writes the whole report. Every probe that fails becomes a line in the report;
// only a failure to write the output is returned.
func (r Reporter) Report(ctx context.Context) error {
	w := &errWriter{w: r.Out}
	now := r.Now()
	w.printf("=== %s UTC / %s local ===\n", now.UTC().Format(time.DateTime), now.In(r.Loc).Format(time.DateTime))
	resp := r.infrastructure(ctx, w, now)
	r.account(ctx, w, resp)
	r.lab(ctx, w)
	return w.err
}

func (r Reporter) path(p ...string) string {
	return filepath.Join(append([]string{r.Root}, p...)...)
}

type response struct {
	code int // 0 when no response at all (printed as 000, like curl)
	body []byte
}

func (r Reporter) infrastructure(ctx context.Context, w *errWriter, now time.Time) map[string]response {
	w.printf("-- infrastructure\n")
	ps, _ := r.Run.Run(ctx, "docker", "compose", "ps", "--format", "{{.Name}}\t{{.Status}}")
	for _, l := range lines(ps) {
		w.printf("   %s\n", l)
	}
	// The probes are independent; /balance reaches the exchange and can take seconds.
	endpoints := []string{"health", "status", "balance", "profit"}
	got := make([]response, len(endpoints))
	var wg sync.WaitGroup
	for i, e := range endpoints {
		wg.Go(func() {
			ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
			defer cancel()
			code, body, err := r.API.Get(ctx, e)
			if err != nil {
				body = nil
			}
			got[i] = response{code, body}
		})
	}
	wg.Wait()
	resp := map[string]response{}
	for i, e := range endpoints {
		resp[e] = got[i]
		w.printf("   %-8s HTTP %03d\n", e, got[i].code)
	}
	w.printf("%s\n", lastProcessLine(resp["health"].body, now))

	alerts, _ := r.Logs.Run(ctx, "docker", "logs", "bottrade-watchdog")
	restarts, _ := r.Logs.Run(ctx, "docker", "logs", "bottrade-updater")
	w.printf("   watchdog alerts: %d   updater restarts: %d\n",
		countLines(alerts, "ALERT:"), countLines(restarts, "restarted "))
	if st, err := os.Stat(r.path("services", "updater", "state", "pending_risk")); err == nil && st.Size() > 0 {
		w.printf("   updater: HOLDING an update that changes risk settings -> review with bin/mg approve-update\n")
	}
	if running, _ := r.Run.Run(ctx, "docker", "inspect", "-f", "{{.State.Running}}", "bottrade-updater"); !strings.Contains(running, "true") {
		w.printf("   updater: NOT RUNNING (auto-updates off)\n")
	}
	tail, _ := fsx.TailLines(r.path("user_data", "logs", "bot.log"), 500)
	w.printf("   bot ERROR lines (last 500): %d\n", countLines(tail, " - ERROR - "))
	n, newest := snapshots(r.path("backups", "db"))
	w.printf("   db backups: %d   newest: %s\n", n, newest)
	return resp
}

// lastProcessLine prints None when last_process_ts is missing, null or 0, and
// "unreadable" when the body is not a JSON object with a numeric last_process_ts.
func lastProcessLine(body []byte, now time.Time) string {
	ts, err := ftapi.LastProcessTS(body)
	if err != nil {
		return "   last_process: unreadable"
	}
	if ts == 0 {
		return "   last_process age Nones"
	}
	age := now.Unix() - ts
	line := fmt.Sprintf("   last_process age %ds", age)
	if age > 900 {
		line += "   <-- STALE, expect a pairlist refresh or a real stall"
	}
	return line
}

// account prints balance, P&L and the open positions. resp holds the bodies fetched by
// the HTTP-code probes; one request per endpoint is enough, and /balance reaches the
// exchange.
func (r Reporter) account(ctx context.Context, w *errWriter, resp map[string]response) {
	w.printf("-- account\n")
	if total, err := ftapi.ParseBalance(resp["balance"].body); err == nil {
		w.printf("   balance %.2f USDT\n", total)
	} else {
		w.printf("   balance: unavailable (check exchange credentials / IP allow-list)\n")
	}
	// As before, an unreadable /status shows as no open positions.
	pos, _ := ftapi.ParseStatus(resp["status"].body)

	db, err := sqlitex.OpenReadOnly(r.DB)
	if err != nil {
		w.printf("   trade database unreadable: %v\n", err)
		return
	}
	defer db.Close()
	closed, err := closedTrades(ctx, db)
	if err != nil {
		w.printf("   trade database unreadable: %v\n", err)
		return
	}
	entries := map[int64]int{}
	for _, p := range pos {
		if entries[p.TradeID], err = entryOrders(ctx, db, p); err != nil {
			w.printf("   trade database unreadable: %v\n", err)
			return
		}
	}
	writeAccount(w, closed, pos, entries)
}

// closedTrade is the part of a closed trades row the report prints. Dates stay the text
// freqtrade stored ("2026-11-20 06:32:10.123456"); the cast stops the driver from
// parsing DATETIME columns into time.Time.
type closedTrade struct {
	ID         int64
	Pair       string
	ProfitAbs  float64
	CloseDate  string
	ExitReason string
}

func closedTrades(ctx context.Context, db *sql.DB) ([]closedTrade, error) {
	rows, err := db.QueryContext(ctx, `select id, coalesce(pair, ''), coalesce(close_profit_abs, 0),
		coalesce(cast(close_date as text), ''), coalesce(exit_reason, '') from trades where is_open=0`)
	if err != nil {
		return nil, fmt.Errorf("closed trades: %w", err)
	}
	defer rows.Close()
	var out []closedTrade
	for rows.Next() {
		var t closedTrade
		if err := rows.Scan(&t.ID, &t.Pair, &t.ProfitAbs, &t.CloseDate, &t.ExitReason); err != nil {
			return nil, fmt.Errorf("closed trades: %w", err)
		}
		out = append(out, t)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("closed trades: %w", err)
	}
	return out, nil
}

// entryOrders counts the filled entry orders of an open trade: buys for a long, sells for
// a short. More than one means the position was ground.
func entryOrders(ctx context.Context, db *sql.DB, p ftapi.Position) (int, error) {
	side := "buy"
	if p.IsShort {
		side = "sell"
	}
	var n int
	err := db.QueryRowContext(ctx, `select count(*) from orders where ft_trade_id=? and status='closed' `+
		`and ft_order_side=?`, p.TradeID, side).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("entry orders of #%d: %w", p.TradeID, err)
	}
	return n, nil
}

func writeAccount(w *errWriter, closed []closedTrade, pos []ftapi.Position, entries map[int64]int) {
	var realised, worst float64
	wins := 0
	for i, t := range closed {
		realised += t.ProfitAbs
		if t.ProfitAbs > 0 {
			wins++
		}
		if i == 0 || t.ProfitAbs < worst {
			worst = t.ProfitAbs
		}
	}
	w.printf("   closed %d  realised %+.2f  wins %d/%d (%.1f%%)  worst %+.2f\n",
		len(closed), realised, wins, len(closed), float64(wins)/float64(max(1, len(closed)))*100, worst)

	var booked, openLeg float64
	for _, t := range pos {
		booked += t.RealizedProfit
		openLeg += t.ProfitAbs
	}
	w.printf("   open %d  open-leg %+.2f  booked-inside-open %+.2f  ALL-IN %+.2f   <- matches freqtrade profit_all_coin\n",
		len(pos), openLeg, booked, realised+booked+openLeg)

	sort.SliceStable(pos, func(i, j int) bool { return pos[i].ProfitAbs < pos[j].ProfitAbs })
	for _, t := range pos {
		w.printf("%s\n", positionLine(t, entries[t.TradeID]))
	}

	w.printf("   last 5 closed:\n")
	sort.SliceStable(closed, func(i, j int) bool { return closed[i].CloseDate < closed[j].CloseDate })
	for _, t := range closed[max(0, len(closed)-5):] {
		w.printf("     #%d %-18s %s %+7.2f  %s\n", t.ID, t.Pair, prefix(t.CloseDate, 16), t.ProfitAbs, prefix(t.ExitReason, 30))
	}
}

// positionLine prints one open position. liq is the distance from the current price to
// the liquidation price, in percent of the current price, positive while safe.
//
// realized_profit is money ALREADY LOST OR BANKED inside a still-open position, from
// partial exits during grinding. profit_abs covers only the open leg and profit_closed
// only closed trades, so printing profit_abs alone hides it: on 2026-08-19 trade #238
// showed an open leg of -1.63 while having already booked -17.91.
func positionLine(t ftapi.Position, entries int) string {
	lp, cr := t.LiquidationPrice, t.CurrentRate
	gap := 0.0
	if lp != 0 && cr != 0 {
		if t.IsShort {
			gap = (lp - cr) / cr * 100
		} else {
			gap = (cr - lp) / cr * 100
		}
	}
	flag := ""
	if gap != 0 && math.Abs(gap) < 15 {
		flag += "  <-- LIQUIDATION NEAR"
	}
	if entries > 1 {
		flag += fmt.Sprintf("  ground(%d entries)", entries)
	}
	bk := ""
	if math.Abs(t.RealizedProfit) >= 0.01 {
		bk = fmt.Sprintf(" booked %+.2f", t.RealizedProfit)
	}
	side := "L"
	if t.IsShort {
		side = "S"
	}
	return fmt.Sprintf("     #%d %-18s %s stake %6.2f %+8.2f (%+6.1f%%) liq %+.0f%%%s open %s%s",
		t.TradeID, t.Pair, side, t.StakeAmount, t.ProfitAbs, t.ProfitRatio*100, gap, bk, prefix(t.OpenDate, 16), flag)
}

func (r Reporter) lab(ctx context.Context, w *errWriter) {
	w.printf("-- lab\n")
	// Tail whichever queue log is newest, not a hardcoded one: the first version pointed
	// at one log and kept reporting a six-hour-old line while a different queue ran.
	if q := newestFile(r.path("lab", "logs", "queue-*.log")); q != "" {
		last, _ := fsx.TailLines(q, 1)
		w.printf("   %s: %s\n", filepath.Base(q), strings.TrimSuffix(last, "\n"))
	}
	// A queue runs as the old lab/queue.sh or as `mg queue` (bin/mg, or a go run binary).
	if _, err := r.Run.Run(ctx, "pgrep", "-f", `lab/queue\.sh|(^|/)mg queue`); err == nil {
		w.printf("   queue: RUNNING\n")
	} else {
		w.printf("   queue: idle\n")
	}
	df, _ := r.Run.Run(ctx, "df", "-h", "/home")
	if ls := lines(df); len(ls) > 0 {
		f := strings.Fields(ls[len(ls)-1])
		free := ""
		if len(f) > 3 {
			free = f[3]
		}
		w.printf("   disk free: %s\n", free)
	}
}

// snapshots counts the directories in dir and names the lexically last one.
func snapshots(dir string) (int, string) {
	ents, _ := os.ReadDir(dir)
	var names []string
	for _, e := range ents {
		if e.IsDir() {
			names = append(names, e.Name())
		}
	}
	if len(names) == 0 {
		return 0, ""
	}
	sort.Strings(names)
	return len(names), names[len(names)-1]
}

// newestFile is `ls -t pattern | head -1`: latest mtime, ties by name.
func newestFile(pattern string) string {
	matches, _ := filepath.Glob(pattern)
	best, bestT := "", time.Time{}
	for _, m := range matches {
		st, err := os.Stat(m)
		if err != nil {
			continue
		}
		if best == "" || st.ModTime().After(bestT) || (st.ModTime().Equal(bestT) && m < best) {
			best, bestT = m, st.ModTime()
		}
	}
	return best
}

// lines splits command output into lines, dropping the final newline.
func lines(s string) []string {
	if s == "" {
		return nil
	}
	return strings.Split(strings.TrimSuffix(s, "\n"), "\n")
}

// countLines is grep -c.
func countLines(s, sub string) int {
	n := 0
	for _, l := range lines(s) {
		if strings.Contains(l, sub) {
			n++
		}
	}
	return n
}

// prefix is Python's s[:n], in code points.
func prefix(s string, n int) string {
	r := []rune(s)
	return string(r[:min(n, len(r))])
}

// errWriter keeps the first write error so the report code stays linear.
type errWriter struct {
	w   io.Writer
	err error
}

func (e *errWriter) printf(format string, a ...any) {
	if e.err == nil {
		_, e.err = fmt.Fprintf(e.w, format, a...)
	}
}
