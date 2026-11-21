// Package binance reads public Binance futures market data. No keys.
package binance

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"
)

// Kline is one candle.
type Kline struct {
	OpenTime               int64
	Open, High, Low, Close float64
}

// Klines fetches candles from url, e.g.
// https://fapi.binance.com/fapi/v1/klines?symbol=BTCUSDT&interval=5m&limit=6.
// A nil hc gets a 10s timeout. It fails when there is no candle.
func Klines(ctx context.Context, hc *http.Client, url string) ([]Kline, error) {
	if hc == nil {
		hc = &http.Client{Timeout: 10 * time.Second}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := hc.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("klines: HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	var raw [][]any
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("klines: %w", err)
	}
	out := make([]Kline, 0, len(raw))
	for _, r := range raw {
		if len(r) < 5 {
			continue
		}
		k := Kline{OpenTime: int64(num(r[0]))}
		k.Open, k.High, k.Low, k.Close = num(r[1]), num(r[2]), num(r[3]), num(r[4])
		out = append(out, k)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("klines: no candles")
	}
	return out, nil
}

func num(v any) float64 {
	switch x := v.(type) {
	case float64:
		return x
	case string:
		f, _ := strconv.ParseFloat(x, 64)
		return f
	}
	return 0
}
