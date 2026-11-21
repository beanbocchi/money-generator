package binance

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestKlines(t *testing.T) {
	tests := []struct {
		name    string
		status  int
		body    string
		wantErr bool
	}{
		{"ok", 200, `[[1,"100","100.2","99.8","100",0],[2,"100","101","99","100.5",0]]`, false},
		{"http error", 500, `[]`, true},
		{"not a list", 200, `{"code":-1121}`, true},
		{"no candles", 200, `[[1,2]]`, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tt.status)
				_, _ = w.Write([]byte(tt.body))
			}))
			defer srv.Close()
			ks, err := Klines(context.Background(), nil, srv.URL)
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tt.wantErr)
			}
			if !tt.wantErr && (len(ks) != 2 || ks[1].High != 101 || ks[1].Close != 100.5 || ks[0].OpenTime != 1) {
				t.Fatalf("parsed %+v", ks)
			}
		})
	}
}
