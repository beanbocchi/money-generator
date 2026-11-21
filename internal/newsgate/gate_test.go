package newsgate

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// funcClassifier adapts a function to Classifier.
type funcClassifier func(ctx context.Context, batch []Item) (map[int]Result, Meta, error)

func (f funcClassifier) Classify(ctx context.Context, batch []Item) (map[int]Result, Meta, error) {
	return f(ctx, batch)
}

func TestNoResultMarksBatchSeen(t *testing.T) {
	srv := feedServer(t, selfTestBinance(), selfTestRSS())
	g := testGate(t, srv, funcClassifier(func(context.Context, []Item) (map[int]Result, Meta, error) {
		return nil, Meta{FinishReason: ptr("stop"), Model: ptr(Model), Refusal: ptr("no")}, nil
	}))
	if err := g.Cycle(context.Background()); err != nil {
		t.Fatal(err)
	}
	recs := readLog(t, g.StateDir)
	var nr map[string]any
	for _, r := range recs {
		if r["kind"] == "no_result" {
			nr = r
		}
	}
	if nr == nil || len(nr["items"].([]any)) != 7 || nr["refusal"] != "no" || nr["input_tokens"] != nil {
		t.Fatalf("no_result record = %v", nr)
	}
	if seen := loadSeen(t, g); len(seen) != 10 {
		t.Errorf("seen has %d ids, want 3 stale + 7 refused", len(seen))
	}
	if c := recs[len(recs)-1]; c["kind"] != "cycle" || c["classified"] != 0.0 || c["api_errors"] != 0.0 {
		t.Errorf("cycle record = %v", c)
	}
}

func TestRunOnceStartRecord(t *testing.T) {
	srv := feedServer(t, selfTestBinance(), selfTestRSS())
	g := testGate(t, srv, &fakeClassifier{})
	g.StateDir = filepath.Join(g.StateDir, "new", "state")
	if err := g.Run(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	recs := readLog(t, g.StateDir)
	if s := recs[0]; s["kind"] != "start" || s["model"] != Model || s["cycle"] != 300.0 || s["ts"] != isoUTC(t0) {
		t.Errorf("start record = %v", s)
	}
	if recs[len(recs)-1]["kind"] != "cycle" {
		t.Errorf("last record = %v", recs[len(recs)-1])
	}
}

func TestRunLoopSleepsAndStops(t *testing.T) {
	srv := feedServer(t, selfTestBinance(), selfTestRSS())
	g := testGate(t, srv, &fakeClassifier{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var sleeps []time.Duration
	g.Sleep = func(ctx context.Context, d time.Duration) error {
		sleeps = append(sleeps, d)
		if len(sleeps) == 2 {
			cancel()
			return ctx.Err()
		}
		return nil
	}
	if err := g.Run(ctx, false); err != nil {
		t.Fatal(err)
	}
	if len(sleeps) != 2 {
		t.Fatalf("sleeps = %v", sleeps)
	}
	for _, d := range sleeps {
		if d < MinSleep || d > Cycle || d < Cycle-time.Minute {
			t.Errorf("sleep %v: want CYCLE minus the cycle's duration, at least 5s", d)
		}
	}
	if n := countKind(readLog(t, g.StateDir), "cycle"); n != 2 {
		t.Errorf("%d cycles, want 2", n)
	}
}

func TestPause(t *testing.T) {
	for elapsed, want := range map[time.Duration]time.Duration{
		0: 300 * time.Second, 10 * time.Second: 290 * time.Second,
		296 * time.Second: 5 * time.Second, 400 * time.Second: 5 * time.Second,
	} {
		if got := pause(elapsed); got != want {
			t.Errorf("pause(%v) = %v, want %v", elapsed, got, want)
		}
	}
}

func TestShutdownMidCycleLogsNothing(t *testing.T) {
	srv := feedServer(t, selfTestBinance(), selfTestRSS())
	ctx, cancel := context.WithCancel(context.Background())
	g := testGate(t, srv, funcClassifier(func(ctx context.Context, _ []Item) (map[int]Result, Meta, error) {
		cancel()
		return nil, Meta{}, ctx.Err()
	}))
	if err := g.Run(ctx, false); err != nil {
		t.Fatal(err)
	}
	recs := readLog(t, g.StateDir)
	if countKind(recs, "api_error")+countKind(recs, "cycle_error")+countKind(recs, "cycle") != 0 {
		t.Errorf("a shutdown wrote error or heartbeat records: %v", recs)
	}
}

func TestBadSeenIsACycleError(t *testing.T) {
	srv := feedServer(t, selfTestBinance(), selfTestRSS())
	g := testGate(t, srv, &fakeClassifier{})
	if err := os.WriteFile(filepath.Join(g.StateDir, "seen.json"), []byte("{oops"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := g.Run(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	recs := readLog(t, g.StateDir)
	if last := recs[len(recs)-1]; last["kind"] != "cycle_error" || !strings.Contains(last["error"].(string), "seen.json") {
		t.Errorf("last record = %v", last)
	}
}

func TestSplit(t *testing.T) {
	pub := func(d time.Duration) string { return isoUTC(t0.Add(-d)) }
	items := []Item{
		{ID: "a", Title: "first a", Published: pub(time.Hour)},
		{ID: "b", Title: "b", Published: pub(24 * time.Hour)},
		{ID: "s", Title: "seen", Published: pub(time.Hour)},
		{ID: "c", Title: "c", Published: pub(24*time.Hour + time.Second)},
		{ID: "a", Title: "second a", Published: pub(time.Hour)},
	}
	fresh, stale, err := split(items, []string{"s"}, t0)
	if err != nil {
		t.Fatal(err)
	}
	if len(fresh) != 2 || fresh[0].ID != "a" || fresh[0].Title != "first a" || fresh[1].ID != "b" {
		t.Errorf("fresh = %+v: want the first a and b (exactly 24h, not older)", fresh)
	}
	if len(stale) != 1 || stale[0].ID != "c" {
		t.Errorf("stale = %+v", stale)
	}
}

func TestCycleBatches(t *testing.T) {
	var rss strings.Builder
	rss.WriteString("<rss><channel>")
	for i := range 30 {
		fmt.Fprintf(&rss, "<item><title>t%d</title><guid>g%d</guid><pubDate>%s</pubDate></item>", i, i, t0.Format(time.RFC1123Z))
	}
	rss.WriteString("</channel></rss>")
	srv := feedServer(t, `{"data": {"catalogs": []}}`, rss.String())
	f := &fakeClassifier{}
	g := testGate(t, srv, f)
	if err := g.Cycle(context.Background()); err != nil {
		t.Fatal(err)
	}
	var sizes []int
	for _, c := range f.calls {
		sizes = append(sizes, len(c))
	}
	if !slices.Equal(sizes, []int{25, 25, 25, 15}) {
		t.Errorf("batch sizes %v, want 25,25,25,15 for 90 items", sizes)
	}
}

func TestSeenSavedOnlyWhenChanged(t *testing.T) {
	srv := feedServer(t, selfTestBinance(), selfTestRSS())
	g := testGate(t, srv, &fakeClassifier{})
	if err := g.Cycle(context.Background()); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(g.StateDir, "seen.json")
	before := loadSeen(t, g)
	if err := os.Remove(p); err != nil {
		t.Fatal(err)
	}
	// Only binance:a1 is still unseen, and the fake model skips it again.
	if err := g.Cycle(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(p); !errors.Is(err, os.ErrNotExist) {
		t.Error("a cycle that added nothing rewrote seen.json")
	}
	if !slices.Equal(g.seen, before) {
		t.Errorf("in-memory seen = %v, want %v", g.seen, before)
	}
}

func TestSeenKeepsTheLastSeenKeep(t *testing.T) {
	srv := feedServer(t, fmt.Sprintf(`{"data": {"catalogs": [{"catalogId": 49, "articles": [
		{"code": "new", "title": "t", "releaseDate": %d}]}]}}`, t0.UnixMilli()), "")
	g := testGate(t, srv, &fakeClassifier{})
	g.Feeds = nil
	for i := range SeenKeep {
		g.seen = append(g.seen, fmt.Sprintf("x:%d", i))
	}
	if err := g.Cycle(context.Background()); err != nil {
		t.Fatal(err)
	}
	seen := loadSeen(t, g)
	if len(seen) != SeenKeep || seen[0] != "x:1" || seen[SeenKeep-1] != "binance:new" {
		t.Errorf("seen.json has %d ids, first %q last %q", len(seen), seen[0], seen[len(seen)-1])
	}
	if _, err := os.Stat(filepath.Join(g.StateDir, "seen.json.tmp")); !errors.Is(err, os.ErrNotExist) {
		t.Error("temporary file left behind")
	}
}
