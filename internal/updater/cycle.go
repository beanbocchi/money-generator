package updater

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/beanbocchi/money-generator/internal/binance"
	"github.com/beanbocchi/money-generator/internal/clock"
	"github.com/beanbocchi/money-generator/internal/runx"
	"github.com/beanbocchi/money-generator/internal/telegram"
)

// Deps are the updater's side effects, injected so tests can replace each one.
type Deps struct {
	Git      runx.Runner     // runs "git"
	Docker   runx.Runner     // runs Config.Docker; output must include stderr (docker logs)
	Notify   telegram.Client // freqtrade's bot, plain-text pages
	Approval telegram.Client // the separate approval bot (buttons)
	Now      func() time.Time
	Sleep    func(ctx context.Context, d time.Duration) error
	Log      *slog.Logger
}

// Updater tracks upstream NostalgiaForInfinity and restarts the bot only when a file the
// bot loads has changed, the fuse allows it, and the new code imports cleanly.
type Updater struct {
	cfg   Config
	d     Deps
	snaps map[string][]string // risk snapshots by commit, at most the two in play
}

// New returns an Updater. Nil Now, Sleep or Log get real defaults.
func New(cfg Config, d Deps) *Updater {
	if d.Now == nil {
		d.Now = time.Now
	}
	if d.Sleep == nil {
		d.Sleep = clock.Sleep
	}
	if d.Log == nil {
		d.Log = slog.Default()
	}
	return &Updater{cfg: cfg, d: d, snaps: map[string][]string{}}
}

// outcome is what one cycle decided. update.sh only logged it; tests assert on it.
type outcome string

const (
	outFetchFailed   outcome = "fetch-failed"
	outError         outcome = "error"
	outUpToDate      outcome = "up-to-date"
	outAdvanced      outcome = "advanced" // unwatched files only, no restart
	outHeld          outcome = "held"
	outDeferred      outcome = "deferred"
	outGuardFailed   outcome = "guard-failed"
	outDryRun        outcome = "dry-run"
	outRestarted     outcome = "restarted"
	outRestartFailed outcome = "restart-failed"
)

// The guard imports the strategy inside the running container: that checks the syntax
// of upstream's 3MB generated strategy and our subclass, and that both class bodies
// execute, under the exact python and freqtrade that will run them. py_compile is not
// used because it writes __pycache__ next to the source, which is a read-only mount.
//
// python3 -P and the __file__ assertion are both load-bearing. Without -P, python puts
// the cwd (/freqtrade) on sys.path, so a stray NostalgiaForInfinity checkout or strategy
// file in /freqtrade can satisfy the import. That happened against the pre-cutover
// container, which had such a stray file.
func guardScript(module string) string {
	return "import sys; sys.path.insert(0,'/freqtrade/user_data/strategies'); import " + module +
		" as m; assert m.__file__.startswith('/freqtrade/user_data/strategies/'), 'guard imported the wrong file: '+str(m.__file__)"
}

func short(sha string) string {
	if len(sha) > 8 {
		return sha[:8]
	}
	return sha
}

func (u *Updater) git(ctx context.Context, args ...string) (string, error) {
	return u.d.Git.Run(ctx, "git", append([]string{"-C", u.cfg.NFIDir}, args...)...)
}

func (u *Updater) docker(ctx context.Context, args ...string) (string, error) {
	return u.d.Docker.Run(ctx, u.cfg.Docker, args...)
}

// cycle is update.sh's one_cycle. The order of the checks is the order of the shell's.
func (u *Updater) cycle(ctx context.Context) outcome {
	log := u.d.Log
	if _, err := u.git(ctx, "fetch", "-q", "origin", "main"); err != nil {
		log.Warn("fetch failed (network?), retrying next cycle", "err", err)
		return outFetchFailed
	}
	old, err := u.git(ctx, "rev-parse", "HEAD")
	if err != nil {
		log.Error("cannot read the running commit, skipping this cycle", "err", err)
		return outError
	}
	newSHA, err := u.git(ctx, "rev-parse", "origin/main")
	if err != nil {
		log.Error("cannot read origin/main, skipping this cycle", "err", err)
		return outError
	}
	old, newSHA = strings.TrimSpace(old), strings.TrimSpace(newSHA)
	if old == newSHA {
		log.Info("up to date at " + short(old))
		return outUpToDate
	}

	diffArgs := append([]string{"diff", "--name-only", old, newSHA, "--"}, u.cfg.WatchPaths...)
	changedOut, err := u.git(ctx, diffArgs...)
	if err != nil {
		// update.sh treated a failed diff as "nothing watched changed" and advanced the
		// tree without the guard. This version logs the error and skips the cycle.
		log.Error("cannot list changed files, skipping this cycle", "err", err)
		return outError
	}
	changed := strings.Fields(changedOut)
	if len(changed) == 0 {
		if _, err := u.git(ctx, "reset", "--hard", "-q", "origin/main"); err != nil {
			log.Error("advancing the clone failed", "err", err)
			return outError
		}
		log.Info(fmt.Sprintf("advanced %s->%s, no watched file changed, not restarting", short(old), short(newSHA)))
		return outAdvanced
	}
	log.Info("watched files changed: " + strings.Join(changed, " "))

	// Everything from here until the reset decides whether to deploy at all, and reads
	// only git objects. The working tree is what the bot loads on its next start,
	// including one nobody planned (an OOM kill, a host reboot), so it must not move
	// until the update is going out.
	if err := os.MkdirAll(string(u.cfg.StateDir), 0o755); err != nil {
		log.Error("cannot create the state dir, skipping this cycle", "err", err)
		return outError
	}
	// The strategy cannot have changed when it is itself a watch path and git did not
	// list it. Otherwise (say WATCH_PATHS=configs/) both snapshots run, as in update.sh.
	strategyChanged := !slices.Contains(u.cfg.WatchPaths, u.cfg.StrategyFile) || slices.Contains(changed, u.cfg.StrategyFile)
	if strategyChanged && u.checkRisk(ctx, old, newSHA) {
		return outHeld
	}
	if err := os.Remove(u.cfg.StateDir.Path("pending_risk")); err != nil && !errors.Is(err, fs.ErrNotExist) {
		log.Warn("cannot remove pending_risk", "err", err)
	}

	if u.deferred(ctx, newSHA) {
		return outDeferred
	}
	return u.deploy(ctx, old, newSHA)
}

// snapshot reads the strategy at rev from git objects, so it needs no checkout. A
// missing file gives an empty snapshot, as `git show ... 2>/dev/null` did. A commit's
// snapshot never changes, so it is kept while a held or deferred update waits; a failed
// read is not kept.
func (u *Updater) snapshot(ctx context.Context, rev string) []string {
	if s, ok := u.snaps[rev]; ok {
		return s
	}
	src, err := u.git(ctx, "show", rev+":"+u.cfg.StrategyFile)
	if err != nil {
		u.d.Log.Warn("cannot read the strategy at "+short(rev)+", treating its risk settings as empty", "err", err)
		return nil
	}
	s := riskSnapshot(src, strings.TrimSuffix(u.cfg.StrategyFile, ".py"), u.cfg.RiskPattern)
	u.snaps[rev] = s
	return s
}

// checkRisk holds the update when its risk settings differ from the running commit's
// and that exact difference has not been approved. It reports whether it held.
func (u *Updater) checkRisk(ctx context.Context, old, newSHA string) bool {
	log := u.d.Log
	for rev := range u.snaps {
		if rev != old && rev != newSHA {
			delete(u.snaps, rev)
		}
	}
	diff := riskDiff(u.snapshot(ctx, old), u.snapshot(ctx, newSHA))
	if len(diff) == 0 {
		return false
	}
	digest := riskDigest(diff)
	if u.cfg.StateDir.Read("approved_risk") == digest {
		log.Info("risk settings change " + digest + " was approved, deploying")
		return false
	}
	text := strings.Join(diff, "\n")
	pending := fmt.Sprintf("commit %s -> %s  digest %s\n%s", short(old), short(newSHA), digest, text)
	if err := u.cfg.StateDir.Write("pending_risk", pending); err != nil {
		log.Error("cannot record the held update", "err", err)
	}
	log.Warn(fmt.Sprintf("HELD at %s: %s changes %d risk setting line(s), approve with bin/mg approve-update",
		short(old), short(newSHA), len(diff)), "diff", strings.Join(head(diff, 20), "\n"))

	// Page once per distinct change, not every cycle while it waits.
	if u.cfg.StateDir.Read("notified_risk") == digest {
		return true
	}
	msg := fmt.Sprintf("NFI update HELD: %s changes risk settings. The bot stays on %s until you decide.\n"+
		"Lines with - are what runs now, + is upstream's new value:\n%s",
		short(newSHA), short(old), strings.Join(head(diff, 25), "\n"))
	if u.cfg.ApprovalEnabled() {
		if u.sendApproval(ctx, digest, msg) == nil && u.cfg.StateDir.Write("notified_risk", digest) == nil {
			log.Info("paged about held update " + digest + " (buttons)")
		}
	} else if u.notify(ctx, msg+"\nApprove on the host: bin/mg approve-update") == nil &&
		u.cfg.StateDir.Write("notified_risk", digest) == nil {
		log.Info("paged about held update " + digest)
	}
	return true
}

func head(lines []string, n int) []string {
	if len(lines) > n {
		return lines[:n]
	}
	return lines
}

// deferred runs the gates between the fuse and the deploy: daily cap, minimum gap, 30m
// and 4h volatility, and the bot's recent exchange errors. It reports whether one of
// them said to wait.
func (u *Updater) deferred(ctx context.Context, newSHA string) bool {
	log := u.d.Log
	c := u.cfg
	s := short(newSHA)

	// Each restart is minutes of the bot not processing (301-331s measured, 540s once).
	// Upstream pushes in bursts: 6 restarts on 2026-10-23, 4 between 01:21 and 02:36 UTC
	// on the night of the Binance hack.
	n, err := u.restartsToday()
	if err != nil {
		log.Error("deferred "+s+": cannot read the restart log", "err", err)
		return true
	}
	if n >= c.MaxRestartsPerDay {
		log.Info(fmt.Sprintf("deferred %s: already %d restarts in 24h (limit %d)", s, n, c.MaxRestartsPerDay))
		return true
	}

	// The daily cap alone never spaces restarts: on 2026-11-21 three landed within 46
	// minutes, each 4-6 minutes after an older one aged out of the 24h window.
	if gap := int64(c.MinRestartGap / time.Second); gap != 0 {
		if since, ok := u.sinceLastRestart(); ok && since < gap {
			log.Info(fmt.Sprintf("deferred %s: last restart %ds ago (minimum gap %ds)", s, since, gap))
			return true
		}
	}

	// No restart into a fast market. No price data means no restart. The 4h window is
	// there because on 2026-11-21 BTC fell 5.9% in the two hours before a 05:08 restart
	// while every 30-minute range stayed under 2%.
	gates := []struct {
		window, data, verdict, url string
		limit                      float64
	}{
		{"30m", "BTC", "market moving", c.VolURL, c.VolLimit},
		{"4h", "4h BTC", "market trending", c.VolLongURL, c.VolLongLimit},
	}
	for _, g := range gates {
		if g.limit == 0 {
			continue
		}
		vol, ok := u.rangePct(ctx, g.url)
		if !ok {
			log.Info(fmt.Sprintf("deferred %s: no %s price data, not restarting blind", s, g.data))
			return true
		}
		if vol >= g.limit {
			log.Info(fmt.Sprintf("deferred %s: BTC %s range %.2f%% >= %g%%, %s", s, g.window, vol, g.limit, g.verdict))
			return true
		}
	}

	// On 2026-11-17 the home IP changed, Binance answered -2015, and a restart at 08:27
	// turned a running bot into one that could not load markets for five hours.
	// show-config never contacts the exchange, so the config guard cannot see this.
	if u.unhealthy(ctx) {
		log.Info(fmt.Sprintf("deferred %s: %s logged exchange errors in the last %s; a restart now may not come back",
			s, c.Target, c.UnhealthyWindow))
		return true
	}
	return false
}

// restartsToday counts restart times in the last 24h. A line whose first field is not
// a number counts as a restart, so a damaged log defers rather than allows.
func (u *Updater) restartsToday() (int, error) {
	f, err := os.Open(u.cfg.StateDir.Path("restarts"))
	if errors.Is(err, fs.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	defer f.Close()
	cutoff := float64(u.d.Now().Unix() - 86400)
	n := 0
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) == 0 {
			continue
		}
		t, err := strconv.ParseFloat(fields[0], 64)
		if err != nil || t >= cutoff {
			n++
		}
	}
	return n, sc.Err()
}

// sinceLastRestart is seconds since the last line of the restart log. ok is false when
// there is no log or its last line is not a whole number; update.sh then printed
// nothing and the gap did not apply.
func (u *Updater) sinceLastRestart() (int64, bool) {
	b, err := os.ReadFile(u.cfg.StateDir.Path("restarts"))
	if err != nil || len(b) == 0 {
		return 0, false
	}
	lines := records(string(b))
	last := strings.TrimSpace(lines[len(lines)-1])
	t, err := strconv.ParseInt(last, 10, 64)
	if err != nil {
		u.d.Log.Warn("last line of the restart log is not a timestamp, minimum gap not applied", "line", last)
		return 0, false
	}
	return u.d.Now().Unix() - t, true
}

// rangePct is BTC's high-low range over the klines at url as a percent of the last
// close, rounded to two decimals as update.sh's printf "%.2f" did before comparing.
func (u *Updater) rangePct(ctx context.Context, url string) (float64, bool) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	ks, err := binance.Klines(ctx, nil, url)
	if err != nil {
		u.d.Log.Warn("klines unavailable", "err", err)
		return 0, false
	}
	last := ks[len(ks)-1].Close
	if last <= 0 {
		u.d.Log.Warn("klines unusable: last close is not positive")
		return 0, false
	}
	hi, lo := ks[0].High, ks[0].Low
	for _, k := range ks[1:] {
		hi, lo = max(hi, k.High), min(lo, k.Low)
	}
	rounded, err := strconv.ParseFloat(fmt.Sprintf("%.2f", (hi-lo)/last*100), 64)
	return rounded, err == nil
}

// unhealthy reports whether the bot's output in the last UNHEALTHY_WINDOW has a line
// matching UNHEALTHY_PATTERN. If docker logs fails the check passes, as in update.sh;
// the guard's docker exec fails next in that case anyway.
func (u *Updater) unhealthy(ctx context.Context) bool {
	out, err := u.docker(ctx, "logs", "--since", u.cfg.UnhealthyWindow, u.cfg.Target)
	if u.cfg.Unhealthy.MatchString(out) {
		return true
	}
	if err != nil {
		u.d.Log.Warn("cannot read the bot's logs, exchange-error check skipped", "err", err)
	}
	return false
}

// deploy advances the tree, runs both guards, and restarts the bot. Once the tree has
// moved it finishes on a context that ignores cancellation: a shutdown halfway would
// leave the bot's next start loading code the guard never passed.
func (u *Updater) deploy(ctx context.Context, old, newSHA string) outcome {
	log := u.d.Log
	c := u.cfg
	ctx = context.WithoutCancel(ctx)

	// The import guard runs against the working tree inside the bot container, so the
	// tree advances here, after every reason not to deploy has been checked.
	if _, err := u.git(ctx, "reset", "--hard", "-q", newSHA); err != nil {
		log.Error("advancing the clone to "+short(newSHA)+" failed, bot untouched", "err", err)
		u.resetTo(ctx, old)
		return outError
	}

	// Both outcomes are logged. With only failures logged, "guard ran and passed" looks
	// the same as "guard was skipped" in the audit trail.
	if err := u.guard(ctx); err != nil {
		u.resetTo(ctx, old)
		log.Error(fmt.Sprintf("guard FAILED for %s, rolled back to %s, bot untouched", short(newSHA), short(old)), "err", err)
		_ = u.notify(ctx, fmt.Sprintf("NFI %s failed to import - rolled back, bot untouched", short(newSHA)))
		return outGuardFailed
	}
	log.Info(fmt.Sprintf("guard passed for %s (%s imports from the strategy dir)", short(newSHA), c.StrategyModule))

	if c.DryRun {
		// The tree goes back. Left advanced, the bot's next start of any kind would run
		// code this dry run had only looked at.
		u.resetTo(ctx, old)
		log.Info(fmt.Sprintf("DRY_RUN: would restart %s for %s->%s, tree left at %s", c.Target, short(old), short(newSHA), short(old)))
		return outDryRun
	}

	if err := os.MkdirAll(string(c.StateDir), 0o755); err != nil {
		log.Error("cannot create the state dir", "err", err)
	}
	if err := u.cfg.StateDir.Write("previous_sha", old); err != nil {
		log.Error("cannot record previous_sha", "err", err)
	}
	if _, err := u.docker(ctx, "restart", c.Target); err != nil {
		log.Error("restart FAILED after applying "+short(newSHA), "err", err)
		_ = u.notify(ctx, fmt.Sprintf("NFI %s applied but restart FAILED - check the host", short(newSHA)))
		return outRestartFailed
	}
	if err := u.recordRestart(); err != nil {
		log.Error("cannot record the restart time", "err", err)
	}
	log.Info(fmt.Sprintf("restarted %s at %s", c.Target, short(newSHA)))
	_ = u.notify(ctx, fmt.Sprintf("NFI %s->%s, bot restarted", short(old), short(newSHA)))
	return outRestarted
}

// guard is the import guard, then the config guard. config.json includes five files
// from NostalgiaForInfinity/configs/, and a malformed one takes the bot down at the next
// restart while the import guard passes. show-config runs the whole merge chain in the
// live container, read-only, in about two seconds.
func (u *Updater) guard(ctx context.Context) error {
	c := u.cfg
	if _, err := u.docker(ctx, "exec", c.Target, "python3", "-P", "-c", guardScript(c.StrategyModule)); err != nil {
		return fmt.Errorf("import guard: %w", err)
	}
	if _, err := u.docker(ctx, "exec", c.Target, "freqtrade", "show-config", "--config", "user_data/config.json"); err != nil {
		return fmt.Errorf("config guard: %w", err)
	}
	return nil
}

func (u *Updater) resetTo(ctx context.Context, sha string) {
	if _, err := u.git(ctx, "reset", "--hard", "-q", sha); err != nil {
		u.d.Log.Error("CANNOT put the clone back on "+short(sha)+", check the host", "err", err)
	}
}

// recordRestart is `date -u +%s >> restarts`.
func (u *Updater) recordRestart() error {
	f, err := os.OpenFile(u.cfg.StateDir.Path("restarts"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintf(f, "%d\n", u.d.Now().Unix()); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// notify pages freqtrade's chat. It returns an error only when the message did not go
// out, so "paged once" is recorded only when true. With no token it is a no-op that
// succeeds, as in update.sh.
func (u *Updater) notify(ctx context.Context, text string) error {
	return u.send(ctx, u.d.Notify, u.cfg.NotifyChat, text, nil, "telegram notify failed")
}

// send posts one message with a 10s deadline and logs failMsg when it did not go out.
// A client with no token sends nothing and succeeds.
func (u *Updater) send(ctx context.Context, tg telegram.Client, chat, text string, markup any, failMsg string) error {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	err := tg.Send(ctx, chat, text, markup)
	if err != nil {
		u.d.Log.Warn(failMsg, "err", err)
	}
	return err
}
