// Package updater tracks upstream NostalgiaForInfinity for the live bot (ADR-0002,
// ADR-0010): it restarts the bot only when a watched file changed, the fuse allows it,
// and the new code imports and its config loads. Approve and Rollback are the host
// tools that go with it.
package updater

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strings"

	"github.com/beanbocchi/money-generator/internal/runx"
	"github.com/beanbocchi/money-generator/internal/telegram"
)

// Main runs the upstream NFI updater loop (was services/updater/update.sh). It takes
// no arguments; everything comes from the environment.
func Main(ctx context.Context, args []string) int {
	log := slog.Default().With("svc", "updater")
	if len(args) > 0 {
		fmt.Fprintln(os.Stderr, "usage: mg updater   (configured by environment, see internal/updater/config.go)")
		return 2
	}
	cfg, err := LoadConfig()
	if err != nil {
		log.Error("bad configuration, not starting", "err", err)
		return 2
	}
	u := New(cfg, Deps{
		Git: runx.Exec{},
		// freqtrade logs to stderr, and the exchange-error check reads docker logs 2>&1.
		Docker:   runx.Exec{Combined: true},
		Notify:   telegram.Client{API: cfg.TelegramAPI, Token: cfg.NotifyToken},
		Approval: telegram.Client{API: cfg.TelegramAPI, Token: cfg.ApprovalToken},
		Log:      log,
	})
	return u.Run(ctx)
}

// Run logs the configuration, then runs a cycle every UPDATE_INTERVAL until ctx is
// done (or once, with RUN_ONCE=1). Between cycles it listens for button presses
// instead of sleeping when the approval bot is configured.
func (u *Updater) Run(ctx context.Context) int {
	log := u.d.Log
	c := u.cfg
	dry := "0"
	if c.DryRun {
		dry = "1"
	}
	log.Info(fmt.Sprintf("started: interval=%ds target=%s dry_run=%s watch='%s' max_restarts_per_day=%d min_gap=%ds vol_limit=%g%%/30m %g%%/4h",
		int(c.Interval.Seconds()), c.Target, dry, strings.Join(c.WatchPaths, " "), c.MaxRestartsPerDay,
		int(c.MinRestartGap.Seconds()), c.VolLimit, c.VolLongLimit))
	if c.Selftest {
		if u.selftest(ctx) != nil {
			return 1
		}
		return 0
	}
	switch {
	case c.ApprovalEnabled():
		log.Info(fmt.Sprintf("approval buttons on: chat %s, approvers: %s", c.ApprovalChat, strings.Join(c.Approvers, " ")))
	case c.ApprovalToken != "":
		log.Warn("approval buttons OFF: APPROVER_IDS is not set (required for a group chat); approve with bin/mg approve-update")
	default:
		log.Info("approval buttons off (no APPROVAL_TELEGRAM_TOKEN); approve with bin/mg approve-update")
	}
	for {
		u.cycle(ctx)
		if c.RunOnce {
			return 0
		}
		// An approval cuts the wait short, so a deploy does not sit for up to
		// UPDATE_INTERVAL after the press.
		if c.ApprovalEnabled() {
			u.listen(ctx, u.d.Now().Add(c.Interval))
		} else {
			_ = u.d.Sleep(ctx, c.Interval)
		}
		if ctx.Err() != nil {
			log.Info("stopping")
			return 0
		}
	}
}
