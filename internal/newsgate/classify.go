package newsgate

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/beanbocchi/money-generator/internal/clock"
)

// Result is the model's verdict on one item. Tickers is never nil.
type Result struct {
	EventType string   `json:"event_type"`
	Scope     string   `json:"scope"`
	Tickers   []string `json:"tickers"`
	Severity  int      `json:"severity"`
	Reason    string   `json:"reason"`
}

// Meta describes the model response. Pointers are fields the API may send as null.
type Meta struct {
	FinishReason *string `json:"finish_reason"`
	Model        *string `json:"model"`
	InputTokens  *int    `json:"input_tokens"`
	OutputTokens *int    `json:"output_tokens"`
	Refusal      *string `json:"-"` // logged only on no_result
}

// Classifier turns a batch of items into results keyed by the item's index in the
// batch. A nil map with a nil error means the model gave no usable answer (refusal,
// truncation, empty content); the items are then marked seen. An error means the call
// failed; the items stay unseen and are asked about again next cycle.
type Classifier interface {
	Classify(ctx context.Context, items []Item) (map[int]Result, Meta, error)
}

// render is the user message: one <item> line per headline, nothing escaped.
func render(items []Item) string {
	var b strings.Builder
	b.WriteString("<headlines>\n")
	for i, it := range items {
		if i > 0 {
			b.WriteByte('\n')
		}
		fmt.Fprintf(&b, `<item id="%d" source="%s" published="%s">%s</item>`,
			i, it.Source, it.Published, it.Title)
	}
	b.WriteString("\n</headlines>")
	return b.String()
}

// OpenAI calls an OpenAI-compatible chat completions endpoint.
type OpenAI struct {
	BaseURL string // e.g. http://127.0.0.1:8317/v1
	Key     string
	HTTP    *http.Client // its Timeout bounds each attempt (CallTimeout)
	Sleep   func(ctx context.Context, d time.Duration) error
}

// NewOpenAI returns a client with the production timeout and clock.
func NewOpenAI(baseURL, key string) *OpenAI {
	return &OpenAI{BaseURL: baseURL, Key: key, HTTP: &http.Client{Timeout: CallTimeout}, Sleep: clock.Sleep}
}

type chatResponse struct {
	Model   *string `json:"model"`
	Choices []struct {
		FinishReason *string `json:"finish_reason"`
		Message      *struct {
			Content *string `json:"content"`
			Refusal *string `json:"refusal"`
		} `json:"message"`
	} `json:"choices"`
	Usage *struct {
		PromptTokens     *int `json:"prompt_tokens"`
		CompletionTokens *int `json:"completion_tokens"`
	} `json:"usage"`
}

// modelReply is the content the strict schema asks for.
type modelReply struct {
	Results []struct {
		ID string `json:"id"`
		Result
	} `json:"results"`
}

// Classify sends one batch with the strict schema and validates the reply. Content
// that does not decode as the schema is an error, so the batch is asked about again.
func (c *OpenAI) Classify(ctx context.Context, items []Item) (map[int]Result, Meta, error) {
	body, err := requestBody(items)
	if err != nil {
		return nil, Meta{}, err
	}
	raw, err := c.post(ctx, body)
	if err != nil {
		return nil, Meta{}, err
	}
	var resp chatResponse
	if err := json.Unmarshal(raw, &resp); err != nil {
		return nil, Meta{}, fmt.Errorf("chat/completions: decode response: %w", err)
	}
	if len(resp.Choices) == 0 || resp.Choices[0].Message == nil {
		return nil, Meta{}, errors.New("chat/completions: response has no choice message")
	}
	choice := resp.Choices[0]
	meta := Meta{FinishReason: choice.FinishReason, Model: resp.Model}
	if resp.Usage != nil {
		meta.InputTokens, meta.OutputTokens = resp.Usage.PromptTokens, resp.Usage.CompletionTokens
	}
	msg := choice.Message
	if choice.FinishReason == nil || *choice.FinishReason != "stop" ||
		(msg.Refusal != nil && *msg.Refusal != "") || msg.Content == nil || *msg.Content == "" {
		meta.Refusal = msg.Refusal
		return nil, meta, nil
	}
	var rep modelReply
	if err := json.Unmarshal([]byte(*msg.Content), &rep); err != nil {
		return nil, meta, fmt.Errorf("decode model reply: %w", err)
	}
	return validate(rep, len(items)), meta, nil
}

func requestBody(items []Item) ([]byte, error) {
	req := map[string]any{
		"model": Model,
		"messages": []map[string]string{
			{"role": "system", "content": System},
			{"role": "user", "content": render(items)},
		},
		"response_format": map[string]any{
			"type": "json_schema",
			"json_schema": map[string]any{
				"name": "headline_events", "strict": true, "schema": schema(),
			},
		},
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(req); err != nil {
		return nil, fmt.Errorf("encode request: %w", err)
	}
	return buf.Bytes(), nil
}

// attempts is how many times a call is tried. A transport error, 408, 429 or 5xx is
// retried after attempt×1s, or after an integer or date Retry-After of up to
// maxRetryAfter.
const (
	attempts      = 3
	maxRetryAfter = 2 * time.Minute
)

// post sends the request, retrying as above, and returns a 2xx body.
func (c *OpenAI) post(ctx context.Context, body []byte) ([]byte, error) {
	url := strings.TrimRight(c.BaseURL, "/") + "/chat/completions"
	for attempt := 1; ; attempt++ {
		raw, retry, delay, err := c.once(ctx, url, body)
		if err == nil || !retry || attempt == attempts || ctx.Err() != nil {
			return raw, err
		}
		if delay <= 0 {
			delay = time.Duration(attempt) * time.Second
		}
		if c.Sleep(ctx, min(delay, maxRetryAfter)) != nil {
			return nil, err
		}
	}
}

// once makes one attempt. retry says whether the failure may be retried, and delay is
// the server's Retry-After, zero if it gave none.
func (c *OpenAI) once(ctx context.Context, url string, body []byte) (raw []byte, retry bool, delay time.Duration, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, false, 0, fmt.Errorf("chat/completions: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.Key)
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, true, 0, fmt.Errorf("chat/completions: %w", err)
	}
	defer resp.Body.Close()
	raw, err = io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if err != nil {
		return nil, true, 0, fmt.Errorf("chat/completions: read body: %w", err)
	}
	if s := resp.StatusCode; s/100 != 2 {
		retry = s == http.StatusRequestTimeout || s == http.StatusTooManyRequests || s >= 500
		return nil, retry, retryAfter(resp.Header.Get("Retry-After")),
			fmt.Errorf("chat/completions: HTTP %d: %s", s, truncateRunes(string(raw), 200))
	}
	return raw, false, 0, nil
}

// retryAfter reads a Retry-After header in seconds or as an HTTP date; zero if absent
// or unreadable.
func retryAfter(v string) time.Duration {
	if n, err := strconv.Atoi(strings.TrimSpace(v)); err == nil {
		return time.Duration(n) * time.Second
	}
	if t, err := http.ParseTime(v); err == nil {
		return time.Until(t)
	}
	return 0
}

// validate indexes results by item: a result whose id is not an integer in range or
// is already taken, or whose event_type is not in the enum, is dropped. Tickers are
// trimmed, uppercased, deduplicated and sorted.
func validate(rep modelReply, n int) map[int]Result {
	out := map[int]Result{}
	for _, r := range rep.Results {
		i, err := strconv.Atoi(strings.TrimSpace(r.ID))
		if err != nil || i < 0 || i >= n {
			continue
		}
		if _, dup := out[i]; dup || !slices.Contains(eventTypes, r.EventType) {
			continue
		}
		tickers := []string{}
		for _, t := range r.Tickers {
			if t = strings.ToUpper(strings.TrimSpace(t)); t != "" {
				tickers = append(tickers, t)
			}
		}
		slices.Sort(tickers)
		r.Tickers = slices.Compact(tickers)
		out[i] = r.Result
	}
	return out
}

// Window is a would-block window.
type Window struct {
	Targets []string `json:"targets"`
	From    string   `json:"from"`
	Until   string   `json:"until"`
}

// windowFor opens a window for a severity 2+ event on MARKET or on named tickers.
func windowFor(r Result, decided time.Time) *Window {
	if r.Severity < BlockSeverity || r.EventType == "none" {
		return nil
	}
	var targets []string
	switch {
	case r.Scope == "MARKET":
		targets = []string{"MARKET"}
	case r.Scope == "TICKERS" && len(r.Tickers) > 0:
		targets = r.Tickers
	default:
		return nil
	}
	return &Window{Targets: targets, From: isoUTC(decided),
		Until: isoUTC(decided.Add(WindowHours * time.Hour))}
}

// truncateRunes keeps the first n characters of s.
func truncateRunes(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n])
}
