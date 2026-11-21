package updater

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"
)

func discardLog() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// Cases 14-20 of tests/test_updater.sh.

func appEnv(ft *fakeTelegram) []string {
	return []string{"APPROVAL_TELEGRAM_TOKEN=apptok", "APPROVAL_CHAT_ID=111", "TELEGRAM_API=" + ft.api()}
}

func grpEnv(ft *fakeTelegram) []string {
	return []string{"APPROVAL_TELEGRAM_TOKEN=apptok", "APPROVAL_CHAT_ID=-500", "APPROVER_IDS=111", "TELEGRAM_API=" + ft.api()}
}

// heldWithButtons leaves a fixture held on a stop-disabling change and returns its
// digest.
func heldWithButtons(t *testing.T, kv []string, ft *fakeTelegram) (*fixture, string) {
	t.Helper()
	f := newFuseFixture(t)
	f.tg = ft
	ft.reset()
	f.commit("NostalgiaForInfinityX7.py", strategy("False", "800", calcLine))
	f.runOnce(kv...)
	dig := pendingDigest(readFile(t, f.statePath("pending_risk")))
	if dig == "" {
		t.Fatal("not held")
	}
	return f, dig
}

func TestCase14ButtonsSent(t *testing.T) {
	ft := newFakeTelegram(t)
	f, dig := heldWithButtons(t, appEnv(ft), ft)
	msgs := ft.callsOf("sendMessage")
	if len(msgs) == 0 {
		t.Fatal("no message sent")
	}
	m := msgs[0]
	if m.Token != "apptok" {
		t.Errorf("wrong bot: %+v", m)
	}
	if m.Params.Get("chat_id") != "111" {
		t.Errorf("wrong chat: %+v", m)
	}
	markup := m.Params.Get("reply_markup")
	if !strings.Contains(markup, "approve:"+dig) || !strings.Contains(markup, "reject:"+dig) {
		t.Errorf("buttons missing or wrong: %s", markup)
	}
	want := `{"inline_keyboard":[[{"text":"✅ Approve","callback_data":"approve:` + dig +
		`"},{"text":"❌ Keep old","callback_data":"reject:` + dig + `"}]]}`
	if markup != want {
		t.Errorf("markup = %s, want jq's %s", markup, want)
	}
	if !strings.Contains(m.Params.Get("text"), "+ system_v3_2_stops_enable = False") {
		t.Errorf("diff mangled: %s", m.Params.Get("text"))
	}
	if f.docker.restarted() {
		t.Error("restarted while waiting for a decision")
	}
	if readFile(t, f.statePath("notified_risk")) != dig+"\n" {
		t.Error("notified_risk not recorded")
	}
}

func TestCase15ApproveDeploysAtOnce(t *testing.T) {
	ft := newFakeTelegram(t)
	f, dig := heldWithButtons(t, appEnv(ft), ft)
	ft.press("approve:"+dig, 111, 111)
	start := time.Now()
	out := f.runLoop(8*time.Second, f.docker.restarted, appEnv(ft)...)
	if !f.docker.restarted() {
		t.Fatalf("did not deploy: %s", out)
	}
	if time.Since(start) > 6*time.Second {
		t.Errorf("deploy took %v; the press should cut the 600s wait short", time.Since(start))
	}
	if readFile(t, f.statePath("approved_risk")) != dig+"\n" {
		t.Error("approval not recorded")
	}
	answered := false
	for _, c := range ft.callsOf("answerCallbackQuery") {
		if c.Params.Get("callback_query_id") == "cq1" && c.Params.Get("text") == "✅ Approved" {
			answered = true
		}
	}
	if !answered {
		t.Error("never answered the callback")
	}
	if got := strings.TrimSpace(readFile(t, f.statePath("tg_offset"))); got != "8" {
		t.Errorf("offset is %q, want 8 (no replay after restart)", got)
	}
	edits := ft.callsOf("editMessageReplyMarkup")
	if len(edits) != 1 || edits[0].Params.Get("message_id") != "42" || edits[0].Params.Get("reply_markup") != `{"inline_keyboard":[]}` {
		t.Errorf("buttons not cleared: %+v", edits)
	}
}

func TestCase16StrangerIgnored(t *testing.T) {
	ft := newFakeTelegram(t)
	f, dig := heldWithButtons(t, appEnv(ft), ft)
	ft.press("approve:"+dig, 222, 111)
	f.runLoop(5*time.Second, f.consumedPress(0), appEnv(ft)...)
	if f.docker.restarted() {
		t.Error("deployed on a stranger's press")
	}
	if exists(f.statePath("approved_risk")) {
		t.Error("recorded a stranger's approval")
	}
	if !hasAnswer(ft, "Not authorised") {
		t.Error("no refusal sent")
	}
}

func TestCase17StaleButton(t *testing.T) {
	ft := newFakeTelegram(t)
	f, _ := heldWithButtons(t, appEnv(ft), ft)
	ft.press("approve:0123456789abcdef", 111, 111)
	out := f.runLoop(5*time.Second, f.consumedPress(0), appEnv(ft)...)
	if f.docker.restarted() {
		t.Error("a stale button deployed the current change")
	}
	if !hasAnswer(ft, "Outdated - this change is no longer pending") {
		t.Error("stale press not reported")
	}
	if !strings.Contains(out, "ignored a stale button for 0123456789abcdef") {
		t.Errorf("stale press not logged: %s", out)
	}
}

func TestCase18KeepOld(t *testing.T) {
	ft := newFakeTelegram(t)
	f, dig := heldWithButtons(t, appEnv(ft), ft)
	old := f.head()
	ft.press("reject:"+dig, 111, 111)
	f.runLoop(5*time.Second, f.consumedPress(0), appEnv(ft)...)
	if f.docker.restarted() {
		t.Error("deployed after Keep old")
	}
	if f.head() != old {
		t.Error("tree moved")
	}
	if exists(f.statePath("approved_risk")) {
		t.Error("Keep old recorded an approval")
	}
	if readFile(t, f.statePath("rejected_risk")) != dig+"\n" {
		t.Error("rejected_risk not recorded")
	}
	confirmed := false
	for _, c := range ft.callsOf("sendMessage") {
		if strings.Contains(c.Params.Get("text"), "Kept the running version") {
			confirmed = true
		}
	}
	if !confirmed {
		t.Error("no confirmation")
	}
}

func TestCase19PlainTextWithoutApprovalBot(t *testing.T) {
	ft := newFakeTelegram(t)
	f := newFuseFixture(t)
	f.commit("NostalgiaForInfinityX7.py", strategy("False", "800", calcLine))
	f.runOnce("FREQTRADE__TELEGRAM__TOKEN=maintok", "TELEGRAM_API="+ft.api())
	msgs := ft.callsOf("sendMessage")
	if len(msgs) != 1 {
		t.Fatalf("messages: %+v", msgs)
	}
	m := msgs[0]
	if m.Token != "maintok" || !strings.Contains(m.Params.Get("text"), "Approve on the host: bin/mg approve-update") || m.Params.Get("reply_markup") != "" {
		t.Errorf("unexpected page: %+v", m)
	}
}

func TestCase20GroupApprovers(t *testing.T) {
	ft := newFakeTelegram(t)
	f, dig := heldWithButtons(t, grpEnv(ft), ft)
	if msgs := ft.callsOf("sendMessage"); len(msgs) == 0 || msgs[0].Params.Get("chat_id") != "-500" {
		t.Error("not posted in the group")
	}
	ft.press("approve:"+dig, 333, -500)
	f.runLoop(5*time.Second, f.consumedPress(ft.pollCount()), grpEnv(ft)...)
	if f.docker.restarted() {
		t.Error("a group member who is not an approver deployed it")
	}
	// An approver pressing a forwarded copy in another chat does not count either.
	ft.press("approve:"+dig, 111, 999)
	writeFile(t, f.statePath("tg_offset"), "7\n")
	f.runLoop(5*time.Second, f.consumedPress(ft.pollCount()), grpEnv(ft)...)
	if f.docker.restarted() || exists(f.statePath("approved_risk")) {
		t.Error("an approver's press outside APPROVAL_CHAT_ID deployed it")
	}
	ft.press("approve:"+dig, 111, -500)
	writeFile(t, f.statePath("tg_offset"), "7\n")
	f.runLoop(8*time.Second, f.docker.restarted, grpEnv(ft)...)
	if !f.docker.restarted() {
		t.Error("approver's press did not deploy")
	}

	t.Run("no APPROVER_IDS in a group keeps the buttons off", func(t *testing.T) {
		ft.reset()
		f := newFuseFixture(t)
		f.commit("NostalgiaForInfinityX7.py", strategy("False", "800", calcLine))
		out := f.runOnce("APPROVAL_TELEGRAM_TOKEN=apptok", "APPROVAL_CHAT_ID=-500", "TELEGRAM_API="+ft.api())
		if !strings.Contains(out, "approval buttons OFF") {
			t.Errorf("enabled buttons anyone could press: %s", out)
		}
		for _, c := range ft.callsOf("sendMessage") {
			if c.Token == "apptok" {
				t.Error("sent buttons anyway")
			}
		}
	})
}

func hasAnswer(ft *fakeTelegram, text string) bool {
	for _, c := range ft.callsOf("answerCallbackQuery") {
		if c.Params.Get("text") == text {
			return true
		}
	}
	return false
}

// Checks the shell suite did not have.

func TestApproverConfig(t *testing.T) {
	tests := []struct {
		name    string
		env     map[string]string
		enabled bool
		ids     []string
	}{
		{"private chat defaults to itself", map[string]string{"APPROVAL_TELEGRAM_TOKEN": "x", "APPROVAL_CHAT_ID": "111"}, true, []string{"111"}},
		{"chat from freqtrade", map[string]string{"APPROVAL_TELEGRAM_TOKEN": "x", "FREQTRADE__TELEGRAM__CHAT_ID": "222"}, true, []string{"222"}},
		{"group needs ids", map[string]string{"APPROVAL_TELEGRAM_TOKEN": "x", "APPROVAL_CHAT_ID": "-500"}, false, nil},
		{"comma list", map[string]string{"APPROVAL_TELEGRAM_TOKEN": "x", "APPROVAL_CHAT_ID": "-500", "APPROVER_IDS": "1,2, 3"}, true, []string{"1", "2", "3"}},
		{"spaces only", map[string]string{"APPROVAL_TELEGRAM_TOKEN": "x", "APPROVAL_CHAT_ID": "-500", "APPROVER_IDS": "  "}, false, nil},
		{"no token", map[string]string{"APPROVAL_CHAT_ID": "111"}, false, []string{"111"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			setEnv(t, tt.env)
			c, err := LoadConfig()
			if err != nil {
				t.Fatal(err)
			}
			if c.ApprovalEnabled() != tt.enabled || strings.Join(c.Approvers, ",") != strings.Join(tt.ids, ",") {
				t.Errorf("enabled=%v ids=%v; want %v %v", c.ApprovalEnabled(), c.Approvers, tt.enabled, tt.ids)
			}
			if c.isApprover("11") && len(tt.ids) > 0 && tt.ids[0] == "111" {
				t.Error("prefix of an id matched")
			}
		})
	}
}

func TestListenSkipsNonButtonsAndKeepsOffset(t *testing.T) {
	ft := newFakeTelegram(t)
	f, dig := heldWithButtons(t, appEnv(ft), ft)
	ft.mu.Lock()
	ft.updates = []map[string]any{
		{"update_id": 3, "message": map[string]any{"text": "hi"}},
		{"update_id": 4, "callback_query": map[string]any{"id": "a", "from": map[string]any{"id": 111}, "data": "other",
			"message": map[string]any{"message_id": 1, "chat": map[string]any{"id": 111}}}},
		{"update_id": 5, "callback_query": map[string]any{"id": "b", "from": map[string]any{"id": 111}, "data": "approve:" + dig,
			"message": map[string]any{"message_id": 2, "chat": map[string]any{"id": 111}}}},
		{"update_id": 6, "callback_query": map[string]any{"id": "c", "from": map[string]any{"id": 111}, "data": "reject:" + dig,
			"message": map[string]any{"message_id": 3, "chat": map[string]any{"id": 111}}}},
	}
	ft.mu.Unlock()
	u, _ := f.updater(appEnv(ft)...)
	if !u.listen(context.Background(), time.Now().Add(5*time.Second)) {
		t.Fatal("approval did not end the wait")
	}
	if got := strings.TrimSpace(readFile(t, f.statePath("tg_offset"))); got != "6" {
		t.Errorf("offset %q, want 6: the update after the approval must be read again next time", got)
	}
	if exists(f.statePath("rejected_risk")) {
		t.Error("handled an update after the approval")
	}
	var plain bool
	for _, c := range ft.callsOf("answerCallbackQuery") {
		if c.Params.Get("callback_query_id") == "a" && c.Params.Get("text") == "" {
			plain = true
		}
	}
	if !plain {
		t.Error("unknown button data not acknowledged")
	}
}

func TestListenRetriesWhenTelegramIsDown(t *testing.T) {
	ft := newFakeTelegram(t)
	ft.fail = true
	f := newFixture(t)
	u, _ := f.updater(appEnv(ft)...)
	var sleeps []time.Duration
	u.d.Sleep = func(ctx context.Context, d time.Duration) error {
		sleeps = append(sleeps, d)
		if len(sleeps) == 3 {
			ft.mu.Lock()
			ft.fail = false
			ft.mu.Unlock()
		}
		return nil
	}
	if u.listen(context.Background(), time.Now().Add(1500*time.Millisecond)) {
		t.Error("approved with nothing pressed")
	}
	if len(sleeps) < 3 || sleeps[0] != 5*time.Second {
		t.Errorf("sleeps %v, want 5s back-offs", sleeps)
	}
}

func TestListenLongPollTimeout(t *testing.T) {
	ft := newFakeTelegram(t)
	f := newFixture(t)
	u, _ := f.updater(appEnv(ft)...)
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	u.listen(ctx, time.Now().Add(600*time.Second))
	polls := ft.callsOf("getUpdates")
	if len(polls) == 0 {
		t.Fatal("no poll")
	}
	p := polls[0].Params
	if p.Get("timeout") != "50" || p.Get("offset") != "0" || p.Get("allowed_updates") != `["callback_query"]` {
		t.Errorf("poll params %v", p)
	}
}

func TestLoopExitsOnCancel(t *testing.T) {
	for _, withButtons := range []bool{false, true} {
		ft := newFakeTelegram(t)
		f := newFixture(t)
		var kv []string
		if withButtons {
			kv = appEnv(ft)
		}
		start := time.Now()
		f.runLoop(300*time.Millisecond, func() bool { return false }, kv...)
		if time.Since(start) > 3*time.Second {
			t.Errorf("buttons=%v: loop took %v to stop", withButtons, time.Since(start))
		}
	}
}

func TestSelftest(t *testing.T) {
	press := func(from int64) []map[string]any {
		return []map[string]any{{"update_id": 1, "callback_query": map[string]any{
			"id": "s", "data": "selftest", "from": map[string]any{"id": from, "first_name": "Khoa"}}}}
	}
	tests := []struct {
		name    string
		kv      []string
		updates []map[string]any
		wantRC  int
		wantLog string
	}{
		{"no approvers prints the id", []string{"APPROVAL_CHAT_ID=-500"}, press(777), 0, "set APPROVER_IDS=777 in .env"},
		{"approver passes", []string{"APPROVAL_CHAT_ID=-500", "APPROVER_IDS=777"}, press(777), 0, "PASSED - Khoa @- (777) is an approver"},
		{"non-approver times out", []string{"APPROVAL_CHAT_ID=-500", "APPROVER_IDS=1", "SELFTEST_SECONDS=1"}, press(777), 1, "no approver press arrived"},
		{"no chat", []string{"APPROVAL_CHAT_ID="}, nil, 1, "APPROVAL_CHAT_ID is empty"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ft := newFakeTelegram(t)
			ft.updates = tt.updates
			f := newFixture(t)
			kv := append([]string{"APPROVAL_SELFTEST=1", "APPROVAL_TELEGRAM_TOKEN=apptok", "TELEGRAM_API=" + ft.api()}, tt.kv...)
			u, logs := f.updater(kv...)
			if rc := u.Run(context.Background()); rc != tt.wantRC {
				t.Errorf("rc %d, want %d\n%s", rc, tt.wantRC, logs)
			}
			if !strings.Contains(logs.String(), tt.wantLog) {
				t.Errorf("log missing %q:\n%s", tt.wantLog, logs)
			}
			if f.docker.log() != "" {
				t.Error("selftest ran a cycle")
			}
		})
	}
}

func TestSendApprovalFailureIsNotRecorded(t *testing.T) {
	ft := newFakeTelegram(t)
	ft.fail = true
	f := newFuseFixture(t)
	f.commit("NostalgiaForInfinityX7.py", strategy("False", "800", calcLine))
	out := f.runOnce(appEnv(ft)...)
	if exists(f.statePath("notified_risk")) || !strings.Contains(out, "approval message failed to send") {
		t.Errorf("failed button message treated as sent: %s", out)
	}
	if strings.Contains(out, "apptok") {
		t.Error("token printed in the log")
	}
}
