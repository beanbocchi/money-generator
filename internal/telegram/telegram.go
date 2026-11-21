// Package telegram is a minimal Bot API client: send messages, inline buttons, and
// long-poll callback queries.
package telegram

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// DefaultTimeout bounds a call whose context has no deadline. A long poll sets its own.
const DefaultTimeout = 15 * time.Second

// Client talks to one bot. A zero Token makes every call a no-op that reports success,
// which is how "Telegram not configured" is expressed.
type Client struct {
	API   string // default https://api.telegram.org
	Token string
	HTTP  *http.Client // default http.DefaultClient; deadlines come from the context
}

func (c Client) Enabled() bool { return c.Token != "" }

// Call posts form params to method and decodes the "result" field into out (may be nil).
func (c Client) Call(ctx context.Context, method string, params url.Values, out any) error {
	if !c.Enabled() {
		return nil
	}
	api := c.API
	if api == "" {
		api = "https://api.telegram.org"
	}
	hc := c.HTTP
	if hc == nil {
		hc = http.DefaultClient
	}
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, DefaultTimeout)
		defer cancel()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		strings.TrimRight(api, "/")+"/bot"+c.Token+"/"+method, strings.NewReader(params.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := hc.Do(req)
	if err != nil {
		return fmt.Errorf("telegram %s: %w", method, redact(err, c.Token))
	}
	defer resp.Body.Close()
	var env struct {
		OK          bool            `json:"ok"`
		Result      json.RawMessage `json:"result"`
		Description string          `json:"description"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&env); err != nil {
		return fmt.Errorf("telegram %s: HTTP %d: %w", method, resp.StatusCode, err)
	}
	if !env.OK {
		return fmt.Errorf("telegram %s: HTTP %d: %s", method, resp.StatusCode, env.Description)
	}
	if out != nil && len(env.Result) > 0 {
		return json.Unmarshal(env.Result, out)
	}
	return nil
}

// Send posts text to chat. markup, if non-nil, is sent as reply_markup JSON.
func (c Client) Send(ctx context.Context, chat, text string, markup any) error {
	p := url.Values{"chat_id": {chat}, "text": {text}}
	if markup != nil {
		b, err := json.Marshal(markup)
		if err != nil {
			return err
		}
		p.Set("reply_markup", string(b))
	}
	return c.Call(ctx, "sendMessage", p, nil)
}

// Button is an inline keyboard button that sends CallbackData back when pressed.
type Button struct {
	Text         string `json:"text"`
	CallbackData string `json:"callback_data"`
}

// InlineKeyboard is reply_markup with rows of buttons. A nil Rows is sent as no
// buttons, which removes them.
type InlineKeyboard struct {
	Rows [][]Button `json:"inline_keyboard"`
}

// Update is the part of a Bot API Update this client reads.
type Update struct {
	UpdateID      int64          `json:"update_id"`
	CallbackQuery *CallbackQuery `json:"callback_query"`
}

// CallbackQuery is a button press.
type CallbackQuery struct {
	ID   string `json:"id"`
	Data string `json:"data"`
	From struct {
		ID        int64  `json:"id"`
		FirstName string `json:"first_name"`
		Username  string `json:"username"`
	} `json:"from"`
	Message *struct {
		MessageID int64 `json:"message_id"`
		Chat      struct {
			ID int64 `json:"id"`
		} `json:"chat"`
	} `json:"message"`
}

// Updates long-polls getUpdates for callback queries from offset, letting Telegram hold
// the request up to timeout. The call's deadline is timeout plus 15s. Each update is
// decoded on its own, so one odd update keeps its id and cannot stop the caller's
// offset from moving past it.
func (c Client) Updates(ctx context.Context, offset int64, timeout time.Duration) ([]Update, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout+15*time.Second)
	defer cancel()
	p := url.Values{
		"offset":          {strconv.FormatInt(offset, 10)},
		"timeout":         {strconv.Itoa(int(timeout / time.Second))},
		"allowed_updates": {`["callback_query"]`},
	}
	var raws []json.RawMessage
	if err := c.Call(ctx, "getUpdates", p, &raws); err != nil {
		return nil, err
	}
	ups := make([]Update, 0, len(raws))
	for _, r := range raws {
		var up Update
		if err := json.Unmarshal(r, &up); err != nil {
			var id struct {
				UpdateID int64 `json:"update_id"`
			}
			if json.Unmarshal(r, &id) != nil {
				continue
			}
			up = Update{UpdateID: id.UpdateID}
		}
		ups = append(ups, up)
	}
	return ups, nil
}

// AnswerCallback acknowledges a button press; a non-empty text shows as a toast.
func (c Client) AnswerCallback(ctx context.Context, id, text string) error {
	p := url.Values{"callback_query_id": {id}}
	if text != "" {
		p.Set("text", text)
	}
	return c.Call(ctx, "answerCallbackQuery", p, nil)
}

// EditReplyMarkup replaces the buttons under a message.
func (c Client) EditReplyMarkup(ctx context.Context, chat, messageID int64, kb InlineKeyboard) error {
	if kb.Rows == nil {
		kb.Rows = [][]Button{}
	}
	b, err := json.Marshal(kb)
	if err != nil {
		return err
	}
	return c.Call(ctx, "editMessageReplyMarkup", url.Values{
		"chat_id":      {strconv.FormatInt(chat, 10)},
		"message_id":   {strconv.FormatInt(messageID, 10)},
		"reply_markup": {string(b)},
	}, nil)
}

// redact keeps the bot token out of error strings that end up in logs.
func redact(err error, token string) error {
	if token == "" {
		return err
	}
	return fmt.Errorf("%s", strings.ReplaceAll(err.Error(), token, "<token>"))
}
