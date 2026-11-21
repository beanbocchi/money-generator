// Package ftapi is a small client for freqtrade's REST API (/api/v1) with basic auth.
// It covers what the watchdog and the status report read: health, balance and the open
// positions.
package ftapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// Client talks to one bot. BaseURL is the server root, e.g. http://freqtrade:8989; the
// /api/v1/ prefix is added per call. Callers bound each request with a context timeout.
type Client struct {
	BaseURL    string
	User, Pass string
}

// Get fetches /api/v1/<endpoint> and returns the HTTP status and body whatever the status.
// A transport failure (refused, timeout) is an error with status 0.
func (c Client) Get(ctx context.Context, endpoint string) (int, []byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		strings.TrimRight(c.BaseURL, "/")+"/api/v1/"+endpoint, nil)
	if err != nil {
		return 0, nil, fmt.Errorf("ftapi %s: %w", endpoint, err)
	}
	req.SetBasicAuth(c.User, c.Pass)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, nil, fmt.Errorf("ftapi %s: %w", endpoint, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return resp.StatusCode, body, fmt.Errorf("ftapi %s: read body: %w", endpoint, err)
	}
	return resp.StatusCode, body, nil
}

// LastProcessTS reads last_process_ts (epoch seconds) from a /health body. It is 0 when
// the field is absent or null, which is how freqtrade reports a bot that has not
// completed a loop since starting, and an error when the body is not a JSON object or
// the field is not a number.
func LastProcessTS(body []byte) (int64, error) {
	var h struct {
		TS float64 `json:"last_process_ts"`
	}
	if err := json.Unmarshal(body, &h); err != nil {
		return 0, fmt.Errorf("health: %w", err)
	}
	return int64(h.TS), nil
}

// ParseBalance decodes a /balance body. It fails when the body is not JSON or has no
// numeric total, e.g. the error object freqtrade returns when exchange access is broken.
func ParseBalance(body []byte) (float64, error) {
	var b struct {
		Total *float64 `json:"total"`
	}
	if err := json.Unmarshal(body, &b); err != nil {
		return 0, fmt.Errorf("balance: %w", err)
	}
	if b.Total == nil {
		return 0, errors.New("balance: no total")
	}
	return *b.Total, nil
}

// Position is one entry of /status, an open trade. A null number decodes as 0.
type Position struct {
	TradeID          int64   `json:"trade_id"`
	Pair             string  `json:"pair"`
	IsShort          bool    `json:"is_short"`
	StakeAmount      float64 `json:"stake_amount"`
	ProfitAbs        float64 `json:"profit_abs"`
	ProfitRatio      float64 `json:"profit_ratio"`
	LiquidationPrice float64 `json:"liquidation_price"`
	CurrentRate      float64 `json:"current_rate"`
	RealizedProfit   float64 `json:"realized_profit"`
	OpenDate         string  `json:"open_date"`
}

// ParseStatus decodes a /status body. Anything other than a JSON array of positions
// (empty body, an error object) is an error.
func ParseStatus(body []byte) ([]Position, error) {
	var ps []Position
	if err := json.Unmarshal(body, &ps); err != nil {
		return nil, fmt.Errorf("status: %w", err)
	}
	return ps, nil
}
