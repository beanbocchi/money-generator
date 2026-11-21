// Package newsgate is ADR-0027's LLM news-event gate in shadow mode (was
// services/newsgate/newsgate.py and bin/newsgate.sh).
//
// Every cycle it fetches Binance announcements and a few crypto RSS feeds, sends the
// new headlines to an OpenAI-compatible model for classification, and appends every
// item, decision and would-block window to STATE/log.jsonl. Nothing here talks to the
// bot. The prompt, schema, model, feeds, thresholds and log records are frozen for the
// shadow period, and the gate continues from the Python version's log.jsonl and
// seen.json.
package newsgate

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/beanbocchi/money-generator/internal/clock"
)

// Gate is one shadow gate. All dependencies are injected so a test can run a whole
// cycle against fake feeds and a fake model.
type Gate struct {
	HTTP       *http.Client // for the feeds; its Timeout bounds each request
	BinanceURL string
	Feeds      []Feed
	Classifier Classifier
	StateDir   string
	Now        func() time.Time
	Sleep      func(ctx context.Context, d time.Duration) error

	seen []string // seen.json as last saved; nil until the first cycle reads it
}

// New returns a gate with the production feeds and clock.
func New(stateDir string, c Classifier) *Gate {
	return &Gate{
		HTTP:       &http.Client{Timeout: FetchTimeout},
		BinanceURL: BinanceURL,
		Feeds:      DefaultFeeds(),
		Classifier: c,
		StateDir:   stateDir,
		Now:        time.Now,
		Sleep:      clock.Sleep,
	}
}

// Run writes the start record and runs cycles until ctx ends, or once. A failed cycle
// is logged and the loop goes on; only a log that cannot be written stops it.
func (g *Gate) Run(ctx context.Context, once bool) error {
	if err := os.MkdirAll(g.StateDir, 0o777); err != nil {
		return fmt.Errorf("state dir: %w", err)
	}
	if err := g.log(&StartRecord{Kind: "start", Model: Model, Cycle: CycleSeconds}); err != nil {
		return err
	}
	for {
		started := time.Now()
		if err := g.Cycle(ctx); err != nil {
			if ctx.Err() != nil {
				return nil
			}
			if lerr := g.log(&CycleErrorRecord{Kind: "cycle_error", Error: errorText(err)}); lerr != nil {
				return fmt.Errorf("cycle failed (%v) and could not be logged: %w", err, lerr)
			}
		}
		if once {
			return nil
		}
		if err := g.Sleep(ctx, pause(time.Since(started))); err != nil {
			return nil
		}
	}
}

// pause is how long to wait after a cycle that took elapsed: the rest of CYCLE, and
// never less than MinSleep.
func pause(elapsed time.Duration) time.Duration {
	return max(MinSleep, Cycle-elapsed)
}

// Cycle fetches, classifies what is new and logs it. Items the model did not answer
// for stay unseen and are asked about again next cycle. seen.json is written only
// when the cycle completes and added ids.
func (g *Gate) Cycle(ctx context.Context) error {
	fetchedAt := g.Now()
	items, fetchErrs, err := g.fetchAll(ctx)
	if err != nil {
		return err
	}
	for _, e := range fetchErrs {
		if err := g.log(&FetchErrorRecord{Kind: "fetch_error", Source: e.source, Error: errorText(e.err)}); err != nil {
			return err
		}
	}
	if g.seen == nil {
		if g.seen, err = g.loadSeen(); err != nil {
			return err
		}
	}
	seen := g.seen
	fresh, stale, err := split(items, seen, fetchedAt)
	if err != nil {
		return err
	}
	for _, it := range stale {
		if err := g.log(&SkippedOldRecord{Kind: "skipped_old", Fetched: isoUTC(fetchedAt), Item: it}); err != nil {
			return err
		}
		seen = append(seen, it.ID)
	}
	classified, apiErrors := 0, 0
	for k := 0; k < len(fresh); k += Batch {
		batch := fresh[k:min(k+Batch, len(fresh))]
		n, ok, err := g.classifyBatch(ctx, batch, fetchedAt, &seen)
		if err != nil {
			return err
		}
		if !ok {
			apiErrors++
		}
		classified += n
	}
	if len(seen) > len(g.seen) {
		seen = seen[max(0, len(seen)-SeenKeep):]
		if err := g.saveSeen(seen); err != nil {
			return err
		}
		g.seen = seen
	}
	return g.log(&CycleRecord{Kind: "cycle", Fetched: len(items), New: len(fresh), Old: len(stale),
		Classified: classified, FetchErrors: len(fetchErrs), APIErrors: apiErrors})
}

// split drops seen items, keeps the first of each id and separates fresh from stale.
func split(items []Item, seen []string, fetchedAt time.Time) (fresh, stale []Item, err error) {
	done := make(map[string]bool, len(seen)+len(items))
	for _, id := range seen {
		done[id] = true
	}
	for _, it := range items {
		if done[it.ID] {
			continue
		}
		done[it.ID] = true
		pub, err := time.Parse(time.RFC3339, it.Published)
		if err != nil {
			return nil, nil, fmt.Errorf("item %s: published: %w", it.ID, err)
		}
		if fetchedAt.Sub(pub) > MaxAgeHours*time.Hour {
			stale = append(stale, it)
		} else {
			fresh = append(fresh, it)
		}
	}
	return fresh, stale, nil
}

// classifyBatch asks the model about one batch and logs the outcome. It returns how
// many items were classified, false if the call failed, and an error that ends the cycle.
func (g *Gate) classifyBatch(ctx context.Context, batch []Item, fetchedAt time.Time, seen *[]string) (int, bool, error) {
	results, meta, err := g.Classifier.Classify(ctx, batch)
	if ctx.Err() != nil {
		return 0, false, ctx.Err()
	}
	if err != nil {
		return 0, false, g.log(&APIErrorRecord{Kind: "api_error", Error: errorText(err), Items: len(batch)})
	}
	decided := g.Now()
	if results == nil {
		ids := make([]string, len(batch))
		for i, it := range batch {
			ids[i] = it.ID
		}
		*seen = append(*seen, ids...)
		return 0, true, g.log(&NoResultRecord{Kind: "no_result", Items: ids, Meta: meta, Refusal: meta.Refusal})
	}
	if err := g.log(&CallRecord{Kind: "call", Items: len(batch), Classified: len(results), Meta: meta}); err != nil {
		return 0, true, err
	}
	n := 0
	for i, it := range batch {
		r, ok := results[i]
		if !ok { // left unseen, so the next cycle asks again
			continue
		}
		rec := &ItemRecord{Kind: "item", Item: it, Fetched: isoUTC(fetchedAt), Decided: isoUTC(decided),
			EventType: r.EventType, Scope: r.Scope, Tickers: r.Tickers, Severity: r.Severity,
			Reason: r.Reason, Window: windowFor(r, decided)}
		if err := g.log(rec); err != nil {
			return n, true, err
		}
		*seen = append(*seen, it.ID)
		n++
	}
	return n, true, nil
}

// isoUTC is the log's time format: UTC, whole seconds, "+00:00".
func isoUTC(t time.Time) string {
	return t.UTC().Format("2006-01-02T15:04:05") + "+00:00"
}

// encode is JSON without HTML escaping, newline-terminated.
func encode(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, fmt.Errorf("encode json: %w", err)
	}
	return buf.Bytes(), nil
}

// log appends one record to log.jsonl in a single write, stamped with the current time.
func (g *Gate) log(rec record) error {
	rec.setTS(isoUTC(g.Now()))
	b, err := encode(rec)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(filepath.Join(g.StateDir, "log.jsonl"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o666)
	if err != nil {
		return fmt.Errorf("open log: %w", err)
	}
	_, err = f.Write(b)
	if err = errors.Join(err, f.Close()); err != nil {
		return fmt.Errorf("write log: %w", err)
	}
	return nil
}

// loadSeen reads seen.json, the ids already logged; a missing file is an empty list.
func (g *Gate) loadSeen() ([]string, error) {
	b, err := os.ReadFile(filepath.Join(g.StateDir, "seen.json"))
	if errors.Is(err, fs.ErrNotExist) {
		return []string{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read seen.json: %w", err)
	}
	var seen []string
	if err := json.Unmarshal(b, &seen); err != nil {
		return nil, fmt.Errorf("parse seen.json: %w", err)
	}
	return seen, nil
}

// saveSeen replaces seen.json atomically.
func (g *Gate) saveSeen(seen []string) error {
	b, err := encode(seen)
	if err != nil {
		return err
	}
	p := filepath.Join(g.StateDir, "seen.json")
	tmp := p + ".tmp"
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o666)
	if err != nil {
		return fmt.Errorf("write seen.json: %w", err)
	}
	_, err = f.Write(b)
	if err = errors.Join(err, f.Sync(), f.Close()); err != nil {
		return fmt.Errorf("write seen.json: %w", err)
	}
	return os.Rename(tmp, p)
}
