package newsgate

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"
)

// testdata/log.jsonl is a sample of the live services/newsgate/state/log.jsonl (every
// record kind present on 2026-11-21, every window, non-ASCII titles) and
// testdata/seen.json a copy of the live seen.json, both written by the Python version.

var liveState = filepath.Join("..", "..", "services", "newsgate", "state")

// copyFile copies src into dir and returns the new path. The source is only read.
func copyFile(t *testing.T, src, dir string) string {
	t.Helper()
	b, err := os.ReadFile(src)
	if err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(dir, filepath.Base(src))
	if err := os.WriteFile(dst, b, 0o644); err != nil {
		t.Fatal(err)
	}
	return dst
}

// decodeRecord parses one log line into its record type, rejecting unknown fields, so
// the test can tell exactly what the gate's types hold.
func decodeRecord(line []byte) (any, error) {
	var head struct {
		Kind string `json:"kind"`
	}
	if err := json.Unmarshal(line, &head); err != nil {
		return nil, err
	}
	rec, ok := map[string]any{
		"start": &StartRecord{}, "cycle": &CycleRecord{}, "call": &CallRecord{},
		"no_result": &NoResultRecord{}, "item": &ItemRecord{}, "skipped_old": &SkippedOldRecord{},
		"fetch_error": &FetchErrorRecord{}, "api_error": &APIErrorRecord{}, "cycle_error": &CycleErrorRecord{},
	}[head.Kind]
	if !ok {
		return nil, fmt.Errorf("unknown kind %q", head.Kind)
	}
	dec := json.NewDecoder(bytes.NewReader(line))
	dec.DisallowUnknownFields()
	return rec, dec.Decode(rec)
}

// tokens is a JSON document as its token stream: key names and order, values, and
// nesting, without whitespace or escaping.
func tokens(t *testing.T, b []byte) []json.Token {
	t.Helper()
	dec := json.NewDecoder(bytes.NewReader(b))
	var out []json.Token
	for {
		tok, err := dec.Token()
		if errors.Is(err, io.EOF) {
			return out
		}
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, tok)
	}
}

// roundTripLog decodes every line into its record type and encodes it again: the
// fields, their order and their values must survive.
func roundTripLog(t *testing.T, path string) map[string]int {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	kinds := map[string]int{}
	for i, line := range bytes.Split(bytes.TrimSuffix(b, []byte("\n")), []byte("\n")) {
		rec, err := decodeRecord(line)
		if err != nil {
			t.Fatalf("line %d: %v", i+1, err)
		}
		out, err := encode(rec)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(tokens(t, out), tokens(t, line)) {
			t.Fatalf("line %d does not round-trip:\n got %s\nwant %s", i+1, out, line)
		}
		kinds[reflect.ValueOf(rec).Elem().FieldByName("Kind").String()]++
	}
	return kinds
}

// roundTripSeen loads seen.json with the gate, saves it again and reloads it.
func roundTripSeen(t *testing.T, path string) []string {
	t.Helper()
	seen := loadSeen(t, &Gate{StateDir: filepath.Dir(path)})
	out := &Gate{StateDir: t.TempDir()}
	if err := out.saveSeen(seen); err != nil {
		t.Fatal(err)
	}
	if again := loadSeen(t, out); !slices.Equal(again, seen) {
		t.Fatalf("seen.json does not round-trip (%d vs %d ids)", len(again), len(seen))
	}
	return seen
}

func TestStateSampleRoundTrips(t *testing.T) {
	kinds := roundTripLog(t, filepath.Join("testdata", "log.jsonl"))
	for _, k := range []string{"start", "cycle", "call", "item", "skipped_old", "fetch_error"} {
		if kinds[k] == 0 {
			t.Errorf("sample has no %s record", k)
		}
	}
	if seen := roundTripSeen(t, filepath.Join("testdata", "seen.json")); len(seen) == 0 {
		t.Error("sample seen.json is empty")
	}
}

func TestLiveStateRoundTrips(t *testing.T) {
	if _, err := os.Stat(filepath.Join(liveState, "log.jsonl")); err != nil {
		t.Skipf("no live state: %v", err)
	}
	dir := t.TempDir()
	log := copyFile(t, filepath.Join(liveState, "log.jsonl"), dir)
	seen := copyFile(t, filepath.Join(liveState, "seen.json"), dir)
	kinds := roundTripLog(t, log)
	n := roundTripSeen(t, seen)
	t.Logf("live copy: %v records, %d seen ids", kinds, len(n))
}

// TestContinuesFromPythonState runs a Go cycle on a copy of the Python state against
// feeds that serve the already-seen headlines again: nothing may be classified,
// skipped or re-logged, and seen.json must not be rewritten.
func TestContinuesFromPythonState(t *testing.T) {
	dir := t.TempDir()
	logPath := copyFile(t, filepath.Join("testdata", "log.jsonl"), dir)
	seenPath := copyFile(t, filepath.Join("testdata", "seen.json"), dir)
	seenBefore, _ := os.ReadFile(seenPath)
	logBefore, _ := os.ReadFile(logPath)
	seen := map[string]bool{}
	for _, id := range loadSeen(t, &Gate{StateDir: dir}) {
		seen[id] = true
	}

	items := seenItems(t, logBefore, seen)
	binance, rss := feedsFor(items)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/binance" {
			fmt.Fprint(w, binance)
			return
		}
		fmt.Fprint(w, rss[strings.TrimPrefix(r.URL.Path, "/")])
	}))
	defer srv.Close()
	g := testGate(t, srv, funcClassifier(func(_ context.Context, b []Item) (map[int]Result, Meta, error) {
		t.Errorf("classifier called for %d already-seen items, first %s", len(b), b[0].ID)
		return nil, Meta{}, nil
	}))
	g.StateDir = dir
	g.Now = func() time.Time { return time.Date(2026, 11, 21, 12, 0, 0, 0, time.UTC) }
	if err := g.Run(context.Background(), true); err != nil {
		t.Fatal(err)
	}

	logAfter, _ := os.ReadFile(logPath)
	if !bytes.HasPrefix(logAfter, logBefore) {
		t.Fatal("existing log lines were changed")
	}
	added := strings.Split(strings.TrimSpace(string(logAfter[len(logBefore):])), "\n")
	want := []string{
		`{"kind":"start","model":"gpt-6.1-sol","cycle":300,"ts":"2026-11-21T12:00:00+00:00"}`,
		fmt.Sprintf(`{"kind":"cycle","fetched":%d,"new":0,"old":0,"classified":0,"fetch_errors":0,"api_errors":0,"ts":"2026-11-21T12:00:00+00:00"}`, len(items)),
	}
	if strings.Join(added, "\n") != strings.Join(want, "\n") {
		t.Errorf("appended:\n%s\nwant:\n%s", strings.Join(added, "\n"), strings.Join(want, "\n"))
	}
	if seenAfter, _ := os.ReadFile(seenPath); !bytes.Equal(seenAfter, seenBefore) {
		t.Error("seen.json changed")
	}
	if len(items) < 20 {
		t.Errorf("only %d seen items reconstructed; the test is too weak", len(items))
	}
}

// seenItems rebuilds the headlines behind the item and skipped_old records whose ids
// are in seen.
func seenItems(t *testing.T, log []byte, seen map[string]bool) []Item {
	t.Helper()
	var items []Item
	for _, line := range bytes.Split(bytes.TrimSpace(log), []byte("\n")) {
		rec, err := decodeRecord(line)
		if err != nil {
			t.Fatal(err)
		}
		var it Item
		switch r := rec.(type) {
		case *ItemRecord:
			it = r.Item
		case *SkippedOldRecord:
			it = r.Item
		default:
			continue
		}
		if seen[it.ID] {
			items = append(items, it)
		}
	}
	return items
}

// feedsFor renders items back into a Binance response and one RSS document per feed.
func feedsFor(items []Item) (string, map[string]string) {
	catalogID := map[string]int{}
	for id, name := range binanceCatalogs {
		catalogID["binance/"+name] = id
	}
	type article struct {
		Code        string `json:"code"`
		Title       string `json:"title"`
		ReleaseDate int64  `json:"releaseDate"`
	}
	byCatalog := map[int][]article{}
	rss := map[string]*strings.Builder{}
	for _, f := range DefaultFeeds() {
		rss[f.Name] = &strings.Builder{}
		rss[f.Name].WriteString(`<?xml version="1.0" encoding="UTF-8"?><rss><channel>`)
	}
	for _, it := range items {
		pub, _ := time.Parse(time.RFC3339, it.Published)
		if code, ok := strings.CutPrefix(it.ID, "binance:"); ok {
			id := catalogID[it.Source]
			byCatalog[id] = append(byCatalog[id], article{code, it.Title, pub.UnixMilli()})
			continue
		}
		b := rss[it.Source]
		guid := strings.TrimPrefix(it.ID, it.Source+":")
		b.WriteString("<item><title>")
		xml.EscapeText(b, []byte(it.Title))
		b.WriteString("</title><link>")
		xml.EscapeText(b, []byte(it.Link))
		b.WriteString("</link><guid>")
		xml.EscapeText(b, []byte(guid))
		b.WriteString("</guid><pubDate>" + pub.Format(time.RFC1123Z) + "</pubDate></item>")
	}
	var cats []map[string]any
	for id, arts := range byCatalog {
		cats = append(cats, map[string]any{"catalogId": id, "articles": arts})
	}
	bin, _ := json.Marshal(map[string]any{"data": map[string]any{"catalogs": cats}})
	out := map[string]string{}
	for name, b := range rss {
		out[name] = b.String() + "</channel></rss>"
	}
	return string(bin), out
}
