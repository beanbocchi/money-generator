package updater

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/beanbocchi/money-generator/internal/telegram"
)

// Approve / Keep-old buttons for a held update. They come from a separate bot:
// Telegram allows one getUpdates consumer per token and freqtrade already polls its
// own, so sharing a token fails with 409 Conflict for both. The bot must also have no
// webhook, or Telegram refuses getUpdates and delivers the presses to the webhook.
//
// The buttons go to APPROVAL_CHAT_ID (default freqtrade's chat, a group). In a group
// anyone present can press Approve, so a press counts only when the presser's user id
// is in APPROVER_IDS.

// sendApproval posts the held-update message with buttons carrying digest.
func (u *Updater) sendApproval(ctx context.Context, digest, text string) error {
	kb := telegram.InlineKeyboard{Rows: [][]telegram.Button{{
		{Text: "✅ Approve", CallbackData: "approve:" + digest},
		{Text: "❌ Keep old", CallbackData: "reject:" + digest},
	}}}
	return u.send(ctx, u.d.Approval, u.cfg.ApprovalChat, text, kb, "approval message failed to send")
}

// listen long-polls the approval bot until deadline. It returns true as soon as an
// approval lands, so the caller runs a cycle at once instead of waiting out the rest of
// the interval.
func (u *Updater) listen(ctx context.Context, deadline time.Time) bool {
	st := u.cfg.StateDir
	off, _ := strconv.ParseInt(strings.TrimSpace(st.Read("tg_offset")), 10, 64)
	for {
		now := u.d.Now()
		if !now.Before(deadline) || ctx.Err() != nil {
			return false
		}
		t := max(min(deadline.Unix()-now.Unix(), 50), 1)
		ups, err := u.d.Approval.Updates(ctx, off, time.Duration(t)*time.Second)
		if err != nil {
			// Telegram unreachable: keep the deadline, retry.
			if u.d.Sleep(ctx, 5*time.Second) != nil {
				return false
			}
			continue
		}
		for _, up := range ups {
			off = up.UpdateID + 1
			if err := st.Write("tg_offset", strconv.FormatInt(off, 10)); err != nil {
				u.d.Log.Warn("cannot save the Telegram offset", "err", err)
			}
			if up.CallbackQuery != nil && u.handlePress(ctx, up.CallbackQuery) {
				return true
			}
		}
	}
}

// handlePress acts on one button press and reports whether it approved the pending
// change.
func (u *Updater) handlePress(ctx context.Context, cq *telegram.CallbackQuery) bool {
	log := u.d.Log
	st := u.cfg.StateDir
	tg := u.d.Approval
	from := strconv.FormatInt(cq.From.ID, 10)
	chat := ""
	if cq.Message != nil {
		chat = strconv.FormatInt(cq.Message.Chat.ID, 10)
	}
	// Acknowledgements and confirmations are best effort: their failure changes nothing.
	quiet := func(method string, err error) {
		if err != nil {
			log.Warn("telegram "+method+" failed", "err", err)
		}
	}
	answer := func(text string) { quiet("answerCallbackQuery", tg.AnswerCallback(ctx, cq.ID, text)) }
	clearButtons := func() {
		if cq.Message != nil {
			quiet("editMessageReplyMarkup", tg.EditReplyMarkup(ctx, cq.Message.Chat.ID, cq.Message.MessageID, telegram.InlineKeyboard{}))
		}
	}

	if chat != u.cfg.ApprovalChat || !u.cfg.isApprover(from) {
		log.Info(fmt.Sprintf("ignored a button press from user %s in chat %s (not an approver here)", from, chat))
		answer("Not authorised")
		return false
	}
	pending := pendingDigest(st.Read("pending_risk"))
	verb, digest, ok := strings.Cut(cq.Data, ":")
	if !ok || (verb != "approve" && verb != "reject") {
		answer("")
		return false
	}
	if pending == "" || digest != pending {
		shown := pending
		if shown == "" {
			shown = "none"
		}
		log.Info(fmt.Sprintf("ignored a stale button for %s (pending: %s)", digest, shown))
		answer("Outdated - this change is no longer pending")
		clearButtons()
		return false
	}

	var verdict string
	if verb == "approve" {
		if err := st.Write("approved_risk", pending); err != nil {
			log.Error("cannot record the approval", "err", err)
			answer("Could not record the approval - use bin/mg approve-update")
			return false
		}
		verdict = "✅ Approved - deploying on this check (restart cap and volatility gate still apply)"
		log.Info("approved " + pending + " via Telegram")
	} else {
		if err := st.Write("rejected_risk", pending); err != nil {
			log.Warn("cannot record the rejection", "err", err)
		}
		verdict = "❌ Kept the running version. Approve later with bin/mg approve-update"
		log.Info("kept old version for " + pending + " via Telegram")
	}
	toast, _, _ := strings.Cut(verdict, " -")
	answer(toast)
	clearButtons()
	_ = u.send(ctx, tg, u.cfg.ApprovalChat, verdict, nil, "telegram sendMessage failed")
	return verb == "approve"
}

// selftest is the setup check run by hand with APPROVAL_SELFTEST=1. It posts a button
// to APPROVAL_CHAT_ID and waits for a press. With APPROVER_IDS unset it logs who
// pressed, so the user id can be copied into .env; with it set, it confirms that an
// approver's press reaches the updater.
func (u *Updater) selftest(ctx context.Context) error {
	log := u.d.Log
	c := u.cfg
	tg := u.d.Approval
	fail := func(msg string) error {
		log.Error("selftest: " + msg)
		return fmt.Errorf("selftest: %s", msg)
	}
	if c.ApprovalToken == "" {
		return fail("APPROVAL_TELEGRAM_TOKEN is empty")
	}
	if c.ApprovalChat == "" {
		return fail("APPROVAL_CHAT_ID is empty")
	}
	var me struct {
		Username string `json:"username"`
	}
	if err := tg.Call(ctx, "getMe", nil, &me); err != nil {
		return fail("getMe failed - wrong token or Telegram unreachable")
	}
	log.Info("selftest: bot is @" + me.Username)
	var hook struct {
		URL string `json:"url"`
	}
	if err := tg.Call(ctx, "getWebhookInfo", nil, &hook); err == nil && hook.URL != "" {
		return fail("FAILED - this bot has a webhook, so Telegram sends its button presses to that URL instead. Use a bot of its own.")
	}
	kb := telegram.InlineKeyboard{Rows: [][]telegram.Button{{{Text: "✅ Test", CallbackData: "selftest"}}}}
	if u.send(ctx, tg, c.ApprovalChat, "Updater approval test: press the button.", kb, "telegram sendMessage failed") != nil {
		return fail("could not post in " + c.ApprovalChat + " - is the bot in that chat?")
	}
	log.Info(fmt.Sprintf("selftest: test button posted, press it (%ds)...", int(c.SelftestWait.Seconds())))

	deadline := u.d.Now().Add(c.SelftestWait)
	var off int64
	for u.d.Now().Before(deadline) {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		ups, err := tg.Updates(ctx, off, 25*time.Second)
		if err != nil {
			if err := u.d.Sleep(ctx, 3*time.Second); err != nil {
				return err
			}
			continue
		}
		for _, up := range ups {
			off = up.UpdateID + 1
			cq := up.CallbackQuery
			if cq == nil || cq.Data != "selftest" {
				continue
			}
			from := strconv.FormatInt(cq.From.ID, 10)
			username := cq.From.Username
			if username == "" {
				username = "-"
			}
			who := cq.From.FirstName + " @" + username
			_ = tg.AnswerCallback(ctx, cq.ID, "Received")
			_, _ = tg.Updates(ctx, off, 0) // confirm the offset so the press is not redelivered
			switch {
			case len(c.Approvers) == 0:
				log.Info(fmt.Sprintf("selftest: pressed by %s, user id %s. If that is you: set APPROVER_IDS=%s in .env", who, from, from))
				return nil
			case c.isApprover(from):
				log.Info(fmt.Sprintf("selftest: PASSED - %s (%s) is an approver and the press reached the updater", who, from))
				return nil
			default:
				log.Warn(fmt.Sprintf("selftest: pressed by %s (%s), who is NOT in APPROVER_IDS - waiting for an approver", who, from))
			}
		}
	}
	return fail("no approver press arrived")
}
