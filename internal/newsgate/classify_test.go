package newsgate

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"sync"
	"testing"
	"time"
)

type reply struct {
	status int
	header map[string]string
	body   string
}

// fakeOpenAI is a chat completions server that answers with the given replies in order,
// repeating the last, and records every request.
type fakeOpenAI struct {
	client  *OpenAI
	mu      sync.Mutex
	bodies  []map[string]any
	headers []http.Header
	paths   []string
	sleeps  []time.Duration
}

func newFakeOpenAI(t *testing.T, replies ...reply) *fakeOpenAI {
	t.Helper()
	f := &fakeOpenAI{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var body map[string]any
		if err := json.Unmarshal(raw, &body); err != nil {
			t.Errorf("request body is not JSON: %v", err)
		}
		f.mu.Lock()
		n := len(f.bodies)
		f.bodies = append(f.bodies, body)
		f.headers = append(f.headers, r.Header.Clone())
		f.paths = append(f.paths, r.URL.Path)
		f.mu.Unlock()
		rep := replies[min(n, len(replies)-1)]
		for k, v := range rep.header {
			w.Header().Set(k, v)
		}
		if rep.status != 0 {
			w.WriteHeader(rep.status)
		}
		io.WriteString(w, rep.body)
	}))
	t.Cleanup(srv.Close)
	f.client = NewOpenAI(srv.URL+"/v1/", "test-key")
	f.client.HTTP = srv.Client()
	f.client.Sleep = func(_ context.Context, d time.Duration) error {
		f.sleeps = append(f.sleeps, d)
		return nil
	}
	return f
}

func (f *fakeOpenAI) last() map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.bodies[len(f.bodies)-1]
}

// chatReply is a 200 chat completion; empty content is sent as null.
func chatReply(content, finish string, refusal *string) reply {
	msg := map[string]any{"role": "assistant", "content": nil, "refusal": refusal}
	if content != "" {
		msg["content"] = content
	}
	b, _ := json.Marshal(map[string]any{
		"id": "c1", "object": "chat.completion", "model": Model,
		"choices": []any{map[string]any{"index": 0, "finish_reason": finish, "message": msg}},
		"usage":   map[string]any{"prompt_tokens": 10, "completion_tokens": 5, "total_tokens": 15},
	})
	return reply{header: map[string]string{"Content-Type": "application/json"}, body: string(b)}
}

var twoItems = []Item{
	{ID: "a:1", Source: "coindesk", Published: "2026-11-16T10:00:00+00:00", Title: `Quote "x" & <b>`},
	{ID: "b:2", Source: "binance/Delisting", Published: "2026-11-16T11:00:00+00:00", Title: "Binance Will Delist FOO"},
}

func TestRender(t *testing.T) {
	want := "<headlines>\n" +
		`<item id="0" source="coindesk" published="2026-11-16T10:00:00+00:00">Quote "x" & <b></item>` + "\n" +
		`<item id="1" source="binance/Delisting" published="2026-11-16T11:00:00+00:00">Binance Will Delist FOO</item>` +
		"\n</headlines>"
	if got := render(twoItems); got != want {
		t.Errorf("render:\n%s\nwant\n%s", got, want)
	}
}

func TestClassifyRequest(t *testing.T) {
	fo := newFakeOpenAI(t, chatReply(`{"results": []}`, "stop", nil))
	res, meta, err := fo.client.Classify(context.Background(), twoItems)
	if err != nil {
		t.Fatal(err)
	}
	if res == nil || len(res) != 0 {
		t.Errorf("an empty results list is a call with nothing classified, not a no_result: %v", res)
	}
	if *meta.FinishReason != "stop" || *meta.Model != Model || *meta.OutputTokens != 5 {
		t.Errorf("meta = %+v", meta)
	}
	if fo.paths[0] != "/v1/chat/completions" {
		t.Errorf("path = %s", fo.paths[0])
	}
	h := fo.headers[0]
	if h.Get("Authorization") != "Bearer test-key" || h.Get("Content-Type") != "application/json" {
		t.Errorf("headers = %v", h)
	}
	body := fo.last()
	msgs := body["messages"].([]any)
	if len(msgs) != 2 ||
		!reflect.DeepEqual(msgs[0], map[string]any{"role": "system", "content": System}) ||
		!reflect.DeepEqual(msgs[1], map[string]any{"role": "user", "content": render(twoItems)}) {
		t.Errorf("messages = %v", msgs)
	}
	js := body["response_format"].(map[string]any)["json_schema"].(map[string]any)
	if js["name"] != "headline_events" || js["strict"] != true {
		t.Errorf("json_schema = %v", js)
	}
	b, _ := json.Marshal(schema())
	var want any
	json.Unmarshal(b, &want)
	if !reflect.DeepEqual(js["schema"], want) {
		t.Errorf("schema sent = %v", js["schema"])
	}
	if len(body) != 3 {
		t.Errorf("request has extra fields: %v", body)
	}
}

func TestClassifyNoResult(t *testing.T) {
	for name, rep := range map[string]reply{
		"refusal":       chatReply(`{"results": []}`, "stop", ptr("I can't")),
		"length":        chatReply(`{"res`, "length", nil),
		"content_null":  chatReply("", "stop", nil),
		"content_empty": {body: `{"model": "m", "choices": [{"finish_reason": "stop", "message": {"content": ""}}]}`},
		"finish_null":   {body: `{"model": "m", "choices": [{"finish_reason": null, "message": {"content": "{}"}}]}`},
	} {
		t.Run(name, func(t *testing.T) {
			fo := newFakeOpenAI(t, rep)
			res, meta, err := fo.client.Classify(context.Background(), twoItems)
			if err != nil || res != nil {
				t.Fatalf("got %v, %v; want no result", res, err)
			}
			if name == "refusal" && (meta.Refusal == nil || *meta.Refusal != "I can't") {
				t.Errorf("refusal not kept: %+v", meta)
			}
		})
	}
}

func TestClassifyErrors(t *testing.T) {
	for name, rep := range map[string]reply{
		"reply not json":   chatReply(`not json`, "stop", nil),
		"extra data":       chatReply(`{"results": []} {}`, "stop", nil),
		"reply a list":     chatReply(`[1]`, "stop", nil),
		"id not a string":  chatReply(`{"results": [{"id": 0}]}`, "stop", nil),
		"results a string": chatReply(`{"results": "x"}`, "stop", nil),
		"no choices":       {body: `{"choices": []}`},
		"body not json":    {body: `<html>proxy</html>`},
		"message missing":  {body: `{"choices": [{"finish_reason": "stop"}]}`},
		"bad request":      {status: 400, body: `{"error": "bad"}`},
	} {
		t.Run(name, func(t *testing.T) {
			fo := newFakeOpenAI(t, rep)
			if _, _, err := fo.client.Classify(context.Background(), twoItems); err == nil {
				t.Fatal("want an error")
			}
			if name == "bad request" && len(fo.bodies) != 1 {
				t.Errorf("400 was retried: %d requests", len(fo.bodies))
			}
		})
	}
}

func TestClassifyRetries(t *testing.T) {
	ok := chatReply(`{"results": []}`, "stop", nil)
	tests := []struct {
		name     string
		replies  []reply
		requests int
		sleeps   []time.Duration
		wantErr  bool
	}{
		{"5xx twice then ok", []reply{{status: 502}, {status: 500}, ok}, 3, []time.Duration{time.Second, 2 * time.Second}, false},
		{"gives up after three attempts", []reply{{status: 503}}, 3, []time.Duration{time.Second, 2 * time.Second}, true},
		{"429 honours retry-after", []reply{{status: 429, header: map[string]string{"Retry-After": "3"}}, ok}, 2,
			[]time.Duration{3 * time.Second}, false},
		{"retry-after is capped", []reply{{status: 503, header: map[string]string{"Retry-After": "500"}}, ok}, 2,
			[]time.Duration{maxRetryAfter}, false},
		{"408 retries", []reply{{status: 408}, ok}, 2, []time.Duration{time.Second}, false},
		{"400 does not", []reply{{status: 400}}, 1, nil, true},
		{"404 does not", []reply{{status: 404}}, 1, nil, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fo := newFakeOpenAI(t, tt.replies...)
			_, _, err := fo.client.Classify(context.Background(), twoItems)
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tt.wantErr)
			}
			if len(fo.bodies) != tt.requests || !slices.Equal(fo.sleeps, tt.sleeps) {
				t.Errorf("requests %d sleeps %v; want %d %v", len(fo.bodies), fo.sleeps, tt.requests, tt.sleeps)
			}
		})
	}
}

type failingTransport struct{ n int }

func (f *failingTransport) RoundTrip(*http.Request) (*http.Response, error) {
	f.n++
	return nil, errors.New("connection refused")
}

func TestClassifyRetriesConnectionErrors(t *testing.T) {
	tr := &failingTransport{}
	c := NewOpenAI("http://proxy.invalid/v1", "k")
	c.HTTP = &http.Client{Transport: tr}
	var sleeps []time.Duration
	c.Sleep = func(_ context.Context, d time.Duration) error { sleeps = append(sleeps, d); return nil }
	if _, _, err := c.Classify(context.Background(), twoItems); err == nil {
		t.Fatal("want an error")
	}
	if tr.n != 3 || !slices.Equal(sleeps, []time.Duration{time.Second, 2 * time.Second}) {
		t.Errorf("attempts %d sleeps %v", tr.n, sleeps)
	}
}

func TestRetryAfter(t *testing.T) {
	if got := retryAfter(" 7 "); got != 7*time.Second {
		t.Errorf("seconds: %v", got)
	}
	if got := retryAfter(time.Now().Add(30 * time.Second).UTC().Format(http.TimeFormat)); got < 28*time.Second || got > 30*time.Second {
		t.Errorf("date: %v, want about 30s", got)
	}
	if got := retryAfter("soon"); got != 0 {
		t.Errorf("garbage: %v", got)
	}
}

func TestValidate(t *testing.T) {
	r := func(id, et string) string {
		return `{"id": "` + id + `", "event_type": "` + et + `", "scope": "TICKERS", "tickers": ["x"], "severity": 2, "reason": "` + id + `"}`
	}
	tests := []struct {
		name  string
		reply string
		want  []int
	}{
		{"padded and signed ids", `{"results": [` + r(" 1 ", "hack") + `, ` + r("+2", "hack") + `]}`, []int{1, 2}},
		{"bad and out-of-range ids are skipped", `{"results": [` + r("x", "hack") + `, ` + r("-1", "hack") + `, ` + r("3", "hack") + `, ` + r("1.0", "hack") + `]}`, nil},
		{"bad enum is skipped and does not take the id", `{"results": [` + r("0", "rugpull") + `, ` + r("0", "hack") + `]}`, []int{0}},
		{"no results key", `{}`, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := validate(decodeReply(t, tt.reply), 3)
			if got == nil {
				t.Fatal("nil map: an empty reply is a call with nothing classified")
			}
			keys := slices.Sorted(maps.Keys(got))
			if !slices.Equal(keys, tt.want) {
				t.Errorf("indexes %v, want %v", keys, tt.want)
			}
		})
	}
	first := validate(decodeReply(t, `{"results": [`+r("0", "hack")+`, `+r(" 0", "depeg")+`]}`), 1)
	if first[0].Reason != "0" {
		t.Errorf("duplicate id: kept %+v, want the first", first[0])
	}
}

func TestValidateTickers(t *testing.T) {
	got := validate(decodeReply(t, `{"results": [{"id": "0", "event_type": "hack", "scope": "TICKERS",
		"tickers": [" zec ", "ZEC", "", "  ", "btc"], "severity": 2, "reason": "r"}]}`), 1)
	if !slices.Equal(got[0].Tickers, []string{"BTC", "ZEC"}) {
		t.Errorf("tickers = %v", got[0].Tickers)
	}
	got = validate(decodeReply(t, `{"results": [{"id": "0", "event_type": "none", "scope": "NONE", "severity": 0, "reason": "r"}]}`), 1)
	if got[0].Tickers == nil || len(got[0].Tickers) != 0 {
		t.Errorf("missing tickers must be [], not null: %#v", got[0].Tickers)
	}
}

func TestWindowForMarket(t *testing.T) {
	w := windowFor(Result{Severity: 2, EventType: "exchange_incident", Scope: "MARKET", Tickers: []string{"BNB"}}, t0)
	if w == nil || !slices.Equal(w.Targets, []string{"MARKET"}) {
		t.Errorf("MARKET window = %+v", w)
	}
	if windowFor(Result{Severity: 3, EventType: "hack", Scope: "NONE", Tickers: []string{"BNB"}}, t0) != nil {
		t.Error("scope NONE opens nothing even with tickers")
	}
}
