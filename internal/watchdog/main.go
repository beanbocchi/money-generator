// Package watchdog pages on Telegram when the bot stops responding or processing. (Was
// services/watchdog/watch.sh, ADR-0009.)
//
// One request to /api/v1/health shows whether the API responds (container up, port
// reachable), whether the API credentials work, and whether last_process_ts is recent
// (the loop is running). freqtrade's own Telegram reports trades only, so a quiet day and
// a bot dead for three days look the same; this check tells them apart.
//
// Log parsing, a rule engine, Prometheus metrics and per-signal analysis are left out.
// The old setup's 736-line watchdog had all of them and still could not say whether the
// bot was alive.
package watchdog

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/beanbocchi/money-generator/internal/clock"
	"github.com/beanbocchi/money-generator/internal/envfile"
	"github.com/beanbocchi/money-generator/internal/ftapi"
	"github.com/beanbocchi/money-generator/internal/telegram"
)

// Config holds the knobs, all read from the same environment variables as watch.sh.
type Config struct {
	API, User, Pass string

	// One rule: N consecutive polls where the bot is not demonstrably processing.
	//
	// Earlier versions special-cased the health fields (null last_process_ts means
	// starting, unless bot_startup_ts is old, unless it is absent...). That produced
	// three wrong versions and 15 false pages in two days, every one of them landing
	// within seconds of an updater restart. The failure was never the threshold, it was
	// reasoning about which field means what during a restart, when the API answers
	// before the worker is ready.
	//
	// So: not-processing is not-processing, whatever the reason. Restarts take a
	// measured 301-331s (24 restarts sampled), so 15 polls at 60s cannot page on one,
	// while a bot that has genuinely died pages within 15 minutes. No startup grace, no
	// field semantics.
	MaxAge           int // seconds since last_process_ts before the loop counts as stalled
	Interval         time.Duration
	FailsBeforeAlert int

	// /api/v1/health proves the bot's own loop is alive. It says nothing about whether
	// the bot can still reach the exchange with working credentials, and those are
	// different failures with different remedies. On 2026-08-05 the Binance key started
	// returning -2015 "Invalid API-key, IP, or permissions": the loop kept ticking,
	// health kept returning 200, last_process_ts stayed fresh, and the watchdog reported
	// everything fine while the bot could not fetch a balance or place an order.
	// /api/v1/balance is the cheapest endpoint that exercises signed exchange access.
	//
	// Polled every CredsCheckEvery iterations rather than every one, because it reaches
	// the exchange and there is no reason to spend rate limit on a 60s cadence.
	CredsCheckEvery       int
	CredsFailsBeforeAlert int

	// While still broken, page again every RealertEvery seconds. One page per incident
	// let the 2026-10-05 IP allow-list outage run 4.6 days on a single message.
	RealertEvery int

	RunOnce bool

	Telegram telegram.Client // zero Token: not configured, every send counts as sent
	Chat     string
}

// ConfigFromEnv reads the environment with watch.sh's names and defaults.
func ConfigFromEnv() (Config, error) {
	c := Config{
		API:     envfile.Or("API", "http://freqtrade:8989"),
		User:    envfile.Or("FREQTRADE__API_SERVER__USERNAME", ""),
		Pass:    envfile.Or("FREQTRADE__API_SERVER__PASSWORD", ""),
		RunOnce: envfile.Or("RUN_ONCE", "0") == "1",
		Telegram: telegram.Client{
			API:   envfile.Or("TELEGRAM_API", "https://api.telegram.org"),
			Token: envfile.Or("FREQTRADE__TELEGRAM__TOKEN", ""),
		},
		Chat: envfile.Or("FREQTRADE__TELEGRAM__CHAT_ID", ""),
	}
	if c.User == "" || c.Pass == "" {
		return c, errors.New("FREQTRADE__API_SERVER__USERNAME and FREQTRADE__API_SERVER__PASSWORD must be set")
	}
	var errs []error
	num := func(key string, def, least int) int {
		n, err := envfile.Int(key, def)
		if err == nil && n < least {
			err = fmt.Errorf("%s must be at least %d, got %d", key, least, n)
		}
		errs = append(errs, err)
		return n
	}
	c.MaxAge = num("MAX_AGE", 900, 0)
	c.Interval = time.Duration(num("INTERVAL", 60, 0)) * time.Second
	c.FailsBeforeAlert = num("FAILS_BEFORE_ALERT", 15, 0)
	c.CredsCheckEvery = num("CREDS_CHECK_EVERY", 5, 1)
	c.CredsFailsBeforeAlert = num("CREDS_FAILS_BEFORE_ALERT", 3, 0)
	c.RealertEvery = num("REALERT_EVERY", 10800, 0)
	return c, errors.Join(errs...)
}

// Main runs the liveness watchdog until ctx is cancelled (or once, with RUN_ONCE=1).
func Main(ctx context.Context, args []string) int {
	log := slog.Default().With("svc", "watchdog")
	if len(args) > 0 {
		log.Error("watchdog takes no arguments; configure it with environment variables")
		return 2
	}
	cfg, err := ConfigFromEnv()
	if err != nil {
		log.Error("bad configuration", "err", err)
		return 2
	}
	New(cfg, log, time.Now, clock.Sleep).Run(ctx)
	return 0
}

// Watchdog polls one bot. Build it with New.
type Watchdog struct {
	cfg   Config
	log   *slog.Logger
	now   func() time.Time
	sleep func(ctx context.Context, d time.Duration) error
	ft    ftapi.Client

	loop, creds incident
}

// incident is the state of one check that can fail, page and recover.
type incident struct {
	what      string // log prefix of a failed check
	threshold int    // consecutive failures before the first page
	recovered string // log line on recovery after a delivered page
	ok        string // Telegram message on that recovery

	fails     int
	since     int64 // epoch the current run of failures began
	alertedAt int64 // epoch of the last page that actually went out; 0 = none
}

// New wires a watchdog. sleep is clock.Sleep outside tests.
func New(cfg Config, log *slog.Logger, now func() time.Time, sleep func(context.Context, time.Duration) error) *Watchdog {
	return &Watchdog{
		cfg: cfg, log: log, now: now, sleep: sleep,
		ft: ftapi.Client{BaseURL: cfg.API, User: cfg.User, Pass: cfg.Pass},
		loop: incident{what: "check failed", threshold: cfg.FailsBeforeAlert,
			recovered: "RECOVERED: bot is responding and processing again",
			ok:        "OK: bot recovered - responding and processing again"},
		creds: incident{what: "creds check failed", threshold: cfg.CredsFailsBeforeAlert,
			recovered: "RECOVERED: exchange access works again",
			ok:        "OK: exchange access recovered"},
	}
}

// Run polls until ctx is done, or once when cfg.RunOnce.
func (w *Watchdog) Run(ctx context.Context) {
	w.log.Info("started", "api", w.cfg.API, "max_age_s", w.cfg.MaxAge, "interval", w.cfg.Interval,
		"fails_before_alert", w.cfg.FailsBeforeAlert, "realert_every_s", w.cfg.RealertEvery)
	for iter := 1; ; iter++ {
		w.Poll(ctx, iter)
		if w.cfg.RunOnce || w.sleep(ctx, w.cfg.Interval) != nil {
			return
		}
	}
}

// Poll runs one iteration: the health check always, the credentials check on iterations
// 1, 1+N, 1+2N... (and always when running once).
func (w *Watchdog) Poll(ctx context.Context, iter int) {
	// Debounced on purpose. The updater restarts the bot whenever upstream changes a
	// watched file, so a single failed poll is expected routine, not an incident.
	w.track(ctx, &w.loop, w.checkHealth(ctx))
	if w.cfg.RunOnce || (iter-1)%w.cfg.CredsCheckEvery == 0 {
		w.track(ctx, &w.creds, w.checkCreds(ctx))
	}
}

// track advances one incident by a check result: reason "" is a pass.
func (w *Watchdog) track(ctx context.Context, inc *incident, reason string) {
	if reason == "" {
		// Recovery is worth one message: an alert with no all-clear trains you to ignore it.
		if inc.alertedAt != 0 {
			w.log.Info(inc.recovered)
			w.notify(ctx, inc.ok)
			inc.alertedAt = 0
		}
		inc.fails = 0
		return
	}
	if inc.fails == 0 {
		inc.since = w.now().Unix()
	}
	inc.fails++
	w.log.Warn(fmt.Sprintf("%s (%d/%d)", inc.what, inc.fails, inc.threshold), "reason", reason)
	if inc.fails >= inc.threshold {
		w.page(ctx, inc, reason)
	}
}

// page sends the first page for an incident, or a repeat once RealertEvery has passed
// since the last one that went out. alertedAt only moves when Telegram accepted the
// message, so a failed send is retried on the next poll instead of being forgotten: on
// 2026-10-12 the only page for an outage failed to send and nothing ever retried it.
//
// "ALERT:" appears only in the log line of a delivered first page; `mg status` counts
// those lines.
func (w *Watchdog) page(ctx context.Context, inc *incident, reason string) {
	now := w.now().Unix()
	switch {
	case inc.alertedAt == 0:
		if w.notify(ctx, "PROBLEM: "+reason) {
			w.log.Error("ALERT: " + reason)
			inc.alertedAt = now
			return
		}
	case now-inc.alertedAt >= int64(w.cfg.RealertEvery):
		m := (now - inc.since) / 60
		if w.notify(ctx, fmt.Sprintf("STILL BROKEN for %dm: %s", m, reason)) {
			w.log.Error(fmt.Sprintf("ALERT REPEAT (failing for %dm): %s", m, reason))
			inc.alertedAt = now
			return
		}
	default:
		return
	}
	w.log.Error("ALERT NOT DELIVERED (retrying next poll): " + reason)
}

// checkHealth returns a reason on failure, "" on success.
func (w *Watchdog) checkHealth(ctx context.Context) string {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	code, body, err := w.ft.Get(ctx, "health")
	if err != nil || code >= 400 {
		return "no response from " + w.cfg.API + "/api/v1/health (down, unreachable, or bad credentials)"
	}
	ts, err := ftapi.LastProcessTS(body)
	if err != nil || ts <= 0 {
		// Null during a restart, null while broken: same signal, and the
		// consecutive-failure count is what tells them apart. Body logged in full because
		// truncating it to 100 chars is what blocked diagnosing the false pages.
		return "not processing yet (no last_process_ts): " + string(body)
	}
	if age := w.now().Unix() - ts; age > int64(w.cfg.MaxAge) {
		return fmt.Sprintf("bot has not processed for %ds (limit %ds) -- loop stalled", age, w.cfg.MaxAge)
	}
	return ""
}

// checkCreds returns a reason when /api/v1/balance does not answer 200.
func (w *Watchdog) checkCreds(ctx context.Context) string {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	code, _, err := w.ft.Get(ctx, "balance")
	if err == nil && code == http.StatusOK {
		return ""
	}
	return fmt.Sprintf("cannot query balance (HTTP %03d) -- the bot is running but has no working "+
		"exchange access; check the API key, its IP allow-list, and its futures permission", code)
}

// notify reports whether the message went out. Telegram not configured counts as sent,
// as in watch.sh, so the incident state still advances.
func (w *Watchdog) notify(ctx context.Context, text string) bool {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := w.cfg.Telegram.Send(ctx, w.cfg.Chat, text, nil); err != nil {
		w.log.Warn("telegram send failed", "err", err)
		return false
	}
	return true
}
