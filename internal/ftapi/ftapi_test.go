package ftapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestGetSendsBasicAuthAndReturnsAnyStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if u, p, ok := r.BasicAuth(); !ok || u != "u" || p != "p" || r.URL.Path != "/api/v1/balance" {
			http.Error(w, "nope", http.StatusUnauthorized)
			return
		}
		http.Error(w, `{"error":"-2015"}`, http.StatusInternalServerError)
	}))
	defer srv.Close()
	code, body, err := Client{BaseURL: srv.URL + "/", User: "u", Pass: "p"}.Get(context.Background(), "balance")
	if err != nil || code != 500 || string(body) != "{\"error\":\"-2015\"}\n" {
		t.Errorf("got %d %q %v", code, body, err)
	}
	code, _, _ = Client{BaseURL: srv.URL, User: "u", Pass: "bad"}.Get(context.Background(), "balance")
	if code != 401 {
		t.Errorf("wrong password: %d", code)
	}
	url := srv.URL
	srv.Close()
	if code, _, err := (Client{BaseURL: url}).Get(context.Background(), "health"); err == nil || code != 0 {
		t.Errorf("closed server: %d %v", code, err)
	}
}

func TestLastProcessTS(t *testing.T) {
	for body, want := range map[string]int64{
		`{"last_process_ts":1763700000}`:           1763700000,
		`{"last_process_ts":1763700000.9}`:         1763700000,
		`{"last_process_ts":null}`:                 0,
		`{"bot_startup_ts":1}`:                     0,
		`{"last_process":"x","last_process_ts":5}`: 5,
		`{"last_process_ts":"1763700000"}`:         -1,
		`not json`:                                 -1,
		``:                                         -1,
		`[1]`:                                      -1,
	} {
		ts, err := LastProcessTS([]byte(body))
		if (want < 0 && err == nil) || (want >= 0 && (err != nil || ts != want)) {
			t.Errorf("%s: got %d %v, want %d", body, ts, err, want)
		}
	}
}

func TestParseBalanceAndStatus(t *testing.T) {
	if v, err := ParseBalance([]byte(`{"total":838.9,"symbol":"USDT"}`)); err != nil || v != 838.9 {
		t.Errorf("balance: %v %v", v, err)
	}
	for _, bad := range []string{``, `{"error":"x"}`, `{"total":null}`, `[1]`} {
		if _, err := ParseBalance([]byte(bad)); err == nil {
			t.Errorf("balance %q accepted", bad)
		}
	}
	ps, err := ParseStatus([]byte(` [{"trade_id":7,"pair":"A/USDT:USDT","is_short":true,"liquidation_price":null,"realized_profit":-1.5}]`))
	if err != nil || len(ps) != 1 || ps[0].TradeID != 7 || !ps[0].IsShort || ps[0].LiquidationPrice != 0 ||
		ps[0].RealizedProfit != -1.5 {
		t.Errorf("status: %+v %v", ps, err)
	}
	for _, bad := range []string{``, `{"detail":"Unauthorized"}`, `[{"trade_id":"x"}]`} {
		if _, err := ParseStatus([]byte(bad)); err == nil {
			t.Errorf("status %q accepted", bad)
		}
	}
}
