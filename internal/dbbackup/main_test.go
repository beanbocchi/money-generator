package dbbackup

// Ports tests/test_dbbackup.sh and adds the evidence that the online backup is
// consistent while another connection (in this process or another one) writes the WAL.

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/beanbocchi/money-generator/internal/sqlitex"
)

// liveDB builds a WAL-mode database with three trades and returns the writer, still open.
// Autocheckpoint is off, so every row lives only in the -wal file: exactly what a cp of
// the main file gets wrong.
func liveDB(t *testing.T, dir string) (string, *sql.DB) {
	t.Helper()
	path := filepath.Join(dir, "live.sqlite")
	w, err := sqlitex.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	w.SetMaxOpenConns(1)
	t.Cleanup(func() { w.Close() })
	mustExec(t, w, "pragma journal_mode=wal", "pragma wal_autocheckpoint=0",
		"create table trades (id integer primary key, pair text, is_open int)",
		"create table counter (n int)", "insert into counter values (0)",
		`insert into trades values (1,'ZEC/USDT:USDT',0),(2,'APE/USDT:USDT',1),(3,'UAI/USDT:USDT',1)`)
	return path, w
}

func mustExec(t *testing.T, db *sql.DB, stmts ...string) {
	t.Helper()
	for _, s := range stmts {
		if _, err := db.Exec(s); err != nil {
			t.Fatalf("%s: %v", s, err)
		}
	}
}

func queryInt(t *testing.T, path, q string) int {
	t.Helper()
	db, err := sqlitex.OpenReadOnly(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var n int
	if err := db.QueryRow(q).Scan(&n); err != nil {
		t.Fatalf("%s on %s: %v", q, path, err)
	}
	return n
}

type logBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *logBuf) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *logBuf) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

func newBackuper(db, out string, now func() time.Time) (Backuper, *logBuf) {
	var lb logBuf
	if now == nil {
		now = time.Now
	}
	return Backuper{
		Config: Config{DB: db, OutDir: out, Keep: 30, Interval: time.Hour, RunOnce: true},
		Log:    slog.New(slog.NewTextHandler(&lb, nil)),
		Now:    now,
	}, &lb
}

func dirs(t *testing.T, dir string) []string {
	t.Helper()
	ents, err := os.ReadDir(dir)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
	var out []string
	for _, e := range ents {
		if e.IsDir() {
			out = append(out, e.Name())
		}
	}
	sort.Strings(out)
	return out
}

func TestSnapshotConsistentAndComplete(t *testing.T) { // cases 1 and 2
	dir := t.TempDir()
	db, w := liveDB(t, dir)
	extra := filepath.Join(dir, "nfi-profit_max.json")
	if err := os.WriteFile(extra, []byte("profit-cache\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	// A writer mid-transaction: its row must not appear in the snapshot.
	tx, err := w.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`insert into trades values (4,'UNCOMMITTED',1)`); err != nil {
		t.Fatal(err)
	}

	b, logs := newBackuper(db, filepath.Join(dir, "out"), func() time.Time {
		return time.Date(2026, 11, 21, 5, 55, 30, 0, time.FixedZone("ICT", 7*3600))
	})
	b.Extra = []string{extra, filepath.Join(dir, "missing.json")}
	snap, err := b.Snapshot(context.Background())
	if err != nil {
		t.Fatalf("snapshot: %v\n%s", err, logs)
	}
	if filepath.Base(snap) != "20261120T225530Z" {
		t.Errorf("snapshot dir %q, want the UTC stamp 20261120T225530Z", filepath.Base(snap))
	}
	copyPath := filepath.Join(snap, "live.sqlite")
	if n := queryInt(t, copyPath, "select count(*) from trades"); n != 3 {
		t.Errorf("snapshot has %d rows, want the 3 committed ones", n)
	}
	if n := queryInt(t, copyPath, "select count(*) from trades where is_open=1"); n != 2 {
		t.Errorf("snapshot has %d open trades, want 2", n)
	}
	if _, _, err := verify(context.Background(), copyPath); err != nil {
		t.Errorf("integrity_check on the snapshot: %v", err)
	}
	if raw, err := os.ReadFile(copyPath); err != nil || len(raw) < 20 || raw[18] != 2 || raw[19] != 2 {
		t.Errorf("snapshot is not in WAL mode like the live database and earlier snapshots: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(snap, "nfi-profit_max.json"))
	if err != nil || string(got) != "profit-cache\n" {
		t.Errorf("extra file not carried along: %q %v", got, err)
	}
	if st, err := os.Stat(filepath.Join(snap, "nfi-profit_max.json")); err != nil || st.Mode().Perm() != 0o640 {
		t.Errorf("extra file mode not preserved: %v %v", st, err)
	}
	if !strings.Contains(logs.String(), "wrote ") || !strings.Contains(logs.String(), "trades=3 open=2") {
		t.Errorf("did not log the write with counts:\n%s", logs)
	}
	if st, err := os.Stat(snap); err != nil || st.Mode().Perm() != 0o755 {
		t.Errorf("snapshot directory mode %v, want 0755 like before: %v", st.Mode(), err)
	}
	if left := dirs(t, b.OutDir); len(left) != 1 {
		t.Errorf("temp directory left behind: %v", left)
	}

	// The control: the main file alone, which is what cp without the -wal would capture,
	// does not even contain the table.
	main := filepath.Join(dir, "maincopy.sqlite")
	raw, err := os.ReadFile(db)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(main, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := verify(context.Background(), main); err == nil {
		t.Error("control failed: the main file alone already held the trades, so this test proves nothing about the WAL")
	}
}

func TestSnapshotNeverWritesTheSource(t *testing.T) {
	dir := t.TempDir()
	db, _ := liveDB(t, dir)
	sum := func(p string) [32]byte {
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		return sha256.Sum256(b)
	}
	stat := func(p string) time.Time {
		st, err := os.Stat(p)
		if err != nil {
			t.Fatal(err)
		}
		return st.ModTime()
	}
	mainSum, walSum, mainM, walM := sum(db), sum(db+"-wal"), stat(db), stat(db+"-wal")
	b, logs := newBackuper(db, filepath.Join(dir, "out"), nil)
	if _, err := b.Snapshot(context.Background()); err != nil {
		t.Fatalf("%v\n%s", err, logs)
	}
	if sum(db) != mainSum || stat(db) != mainM {
		t.Error("the main database file changed")
	}
	if sum(db+"-wal") != walSum || stat(db+"-wal") != walM {
		t.Error("the -wal changed (a checkpoint ran through the backup connection)")
	}
}

// The compose service mounts user_data read-only. A source directory and files that
// cannot be written must still back up, with the bot's writer connection open.
func TestSnapshotFromReadOnlySource(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores file permissions")
	}
	dir := t.TempDir()
	src := filepath.Join(dir, "src")
	if err := os.Mkdir(src, 0o755); err != nil {
		t.Fatal(err)
	}
	db, w := liveDB(t, src)
	for _, p := range []string{db, db + "-wal", db + "-shm"} {
		if err := os.Chmod(p, 0o444); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Chmod(src, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(src, 0o755) })
	mustExec(t, w, `insert into trades values (5,'AFTER/USDT:USDT',1)`) // writer still works

	b, logs := newBackuper(db, filepath.Join(dir, "out"), nil)
	snap, err := b.Snapshot(context.Background())
	if err != nil {
		t.Fatalf("snapshot from a read-only source: %v\n%s", err, logs)
	}
	if n := queryInt(t, filepath.Join(snap, "live.sqlite"), "select count(*) from trades"); n != 4 {
		t.Errorf("snapshot has %d rows, want 4", n)
	}
}

// checkInvariant asserts a snapshot is internally consistent: every trade insert and its
// counter update were committed together, so a torn copy would disagree.
func checkInvariant(t *testing.T, snap string) int {
	t.Helper()
	p := filepath.Join(snap, "live.sqlite")
	if _, _, err := verify(context.Background(), p); err != nil {
		t.Fatalf("%s: %v", snap, err)
	}
	trades := queryInt(t, p, "select count(*) from trades")
	n := queryInt(t, p, "select n from counter")
	if trades != n+3 {
		t.Fatalf("%s is torn: %d trades but counter says %d (+3 seed rows)", snap, trades, n)
	}
	return n
}

func writeTx(db *sql.DB, i int) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec("insert into trades (pair, is_open) values (?, 1)", fmt.Sprintf("P%d/USDT:USDT", i)); err != nil {
		return err
	}
	if _, err := tx.Exec("update counter set n = n + 1"); err != nil {
		return err
	}
	return tx.Commit()
}

func TestSnapshotConsistentUnderConcurrentWriter(t *testing.T) {
	dir := t.TempDir()
	db, w := liveDB(t, dir)
	mustExec(t, w, "pragma wal_autocheckpoint=50", "pragma busy_timeout=5000") // let checkpoints race too
	var stop atomic.Bool
	var wrote atomic.Int64
	var wg sync.WaitGroup
	wg.Go(func() {
		for i := 0; !stop.Load(); i++ {
			if err := writeTx(w, i); err != nil {
				t.Errorf("writer: %v", err)
				return
			}
			wrote.Add(1)
		}
	})
	b, logs := newBackuper(db, filepath.Join(dir, "out"), nil)
	b.Keep = 1000
	seen := map[int]bool{}
	for range 25 {
		snap, err := b.Snapshot(context.Background())
		if err != nil {
			stop.Store(true)
			wg.Wait()
			t.Fatalf("snapshot during writes: %v\n%s", err, logs)
		}
		seen[checkInvariant(t, snap)] = true
	}
	stop.Store(true)
	wg.Wait()
	if len(seen) < 2 || wrote.Load() == 0 {
		t.Errorf("writer did not overlap the snapshots (wrote %d, distinct states %d)", wrote.Load(), len(seen))
	}
}

// TestHelperWriter is the writer process for TestSnapshotConsistentWithWriterProcess.
func TestHelperWriter(t *testing.T) {
	path := os.Getenv("MG_DBBACKUP_HELPER_DB")
	if path == "" {
		t.Skip("helper process only")
	}
	w, err := sqlitex.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	w.SetMaxOpenConns(1)
	mustExec(t, w, "pragma busy_timeout=5000", "pragma wal_autocheckpoint=50")
	deadline := time.Now().Add(30 * time.Second)
	for i := 0; time.Now().Before(deadline); i++ {
		if _, err := os.Stat(path + ".stop"); err == nil {
			return
		}
		if err := writeTx(w, i); err != nil {
			t.Fatal(err)
		}
	}
}

func TestSnapshotConsistentWithWriterProcess(t *testing.T) {
	if testing.Short() {
		t.Skip("spawns a process")
	}
	dir := t.TempDir()
	db, w := liveDB(t, dir)
	w.Close()
	cmd := exec.Command(os.Args[0], "-test.run=^TestHelperWriter$", "-test.count=1")
	cmd.Env = append(os.Environ(), "MG_DBBACKUP_HELPER_DB="+db)
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	stopHelper := func() {
		os.WriteFile(db+".stop", nil, 0o600)
		if err := cmd.Wait(); err != nil {
			t.Errorf("writer process: %v\n%s", err, out.String())
		}
	}
	defer func() {
		if cmd.ProcessState == nil {
			stopHelper()
		}
	}()
	for start := time.Now(); queryInt(t, db, "select n from counter") == 0; {
		if time.Since(start) > 10*time.Second {
			t.Fatalf("writer process never wrote:\n%s", out.String())
		}
		time.Sleep(10 * time.Millisecond)
	}
	b, logs := newBackuper(db, filepath.Join(dir, "out"), nil)
	b.Keep = 1000
	seen := map[int]bool{}
	for range 25 {
		snap, err := b.Snapshot(context.Background())
		if err != nil {
			t.Fatalf("snapshot during writes: %v\n%s", err, logs)
		}
		seen[checkInvariant(t, snap)] = true
	}
	stopHelper()
	if len(seen) < 2 {
		t.Errorf("writer process did not overlap the snapshots (%d distinct states)", len(seen))
	}
}

func TestPruneKeepsNewest(t *testing.T) { // case 3
	dir := t.TempDir()
	db, _ := liveDB(t, dir)
	out := filepath.Join(dir, "out")
	b, _ := newBackuper(db, out, nil)
	if _, err := b.Snapshot(context.Background()); err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= 5; i++ {
		if err := os.MkdirAll(filepath.Join(out, fmt.Sprintf("2020010%dT000000Z", i)), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	// A stray file is not a snapshot and must be neither counted nor removed.
	if err := os.WriteFile(filepath.Join(out, "README"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	b.Keep = 3
	if _, err := b.Snapshot(context.Background()); err != nil {
		t.Fatal(err)
	}
	// 6 dirs existed (the first snapshot + 5 stubs); this run adds a 7th and prunes 4,
	// so 20200101..04 go and 20200105 is the oldest survivor.
	left := dirs(t, out)
	if len(left) != 3 || left[0] != "20200105T000000Z" {
		t.Errorf("left %v, want 3 with 20200105T000000Z oldest", left)
	}
	if _, err := os.Stat(filepath.Join(out, "README")); err != nil {
		t.Errorf("pruned a non-directory: %v", err)
	}
}

func TestSameSecondSnapshotsBothSurvive(t *testing.T) { // case 3b
	dir := t.TempDir()
	db, _ := liveDB(t, dir)
	fixed := func() time.Time { return time.Date(2026, 11, 21, 5, 55, 30, 0, time.UTC) }
	b, _ := newBackuper(db, filepath.Join(dir, "out2"), fixed)
	s1, err1 := b.Snapshot(context.Background())
	s2, err2 := b.Snapshot(context.Background())
	if err1 != nil || err2 != nil {
		t.Fatal(err1, err2)
	}
	left := dirs(t, b.OutDir)
	if len(left) != 2 || s1 == s2 {
		t.Fatalf("same-second collision destroyed one: %v", left)
	}
	if filepath.Base(s1) != "20261121T055530Z" || !strings.HasPrefix(filepath.Base(s2), "20261121T055530-") ||
		!strings.HasSuffix(s2, "Z") {
		t.Errorf("names %s, %s", s1, s2)
	}
	for _, s := range []string{s1, s2} {
		if n := queryInt(t, filepath.Join(s, "live.sqlite"), "select count(*) from trades"); n != 3 {
			t.Errorf("%s has %d trades", s, n)
		}
	}
}

func TestMissingDatabaseFailsLoudly(t *testing.T) { // case 4
	dir := t.TempDir()
	db, _ := liveDB(t, dir)
	out := filepath.Join(dir, "out")
	fixed := func() time.Time { return time.Date(2026, 11, 21, 5, 55, 30, 0, time.UTC) }
	good, _ := newBackuper(db, out, fixed)
	if _, err := good.Snapshot(context.Background()); err != nil {
		t.Fatal(err)
	}
	before := dirs(t, out)
	// Same second as the good snapshot: the original bug deleted it here.
	bad, logs := newBackuper(filepath.Join(dir, "nope.sqlite"), out, fixed)
	if err := bad.Run(context.Background()); err == nil {
		t.Error("RunOnce reported success for a missing database")
	}
	if !strings.Contains(logs.String(), "BACKUP FAILED") {
		t.Errorf("silent on a missing database:\n%s", logs)
	}
	if after := dirs(t, out); strings.Join(after, ",") != strings.Join(before, ",") {
		t.Errorf("directories changed from %v to %v", before, after)
	}
	if n := queryInt(t, filepath.Join(out, "20261121T055530Z", "live.sqlite"), "select count(*) from trades"); n != 3 {
		t.Errorf("the good same-second snapshot was damaged: %d trades", n)
	}
	if _, err := os.Stat(filepath.Join(dir, "nope.sqlite")); !errors.Is(err, os.ErrNotExist) {
		t.Error("backup created the missing source database")
	}
}

func TestNotATradeDatabaseFails(t *testing.T) {
	dir := t.TempDir()
	for name, setup := range map[string]func(string) error{
		"garbage": func(p string) error { return os.WriteFile(p, bytes.Repeat([]byte("x"), 8192), 0o600) },
		"no trades table": func(p string) error {
			db, err := sqlitex.Open(p)
			if err != nil {
				return err
			}
			defer db.Close()
			_, err = db.Exec("create table other (a int)")
			return err
		},
	} {
		p := filepath.Join(dir, strings.ReplaceAll(name, " ", "_")+".sqlite")
		if err := setup(p); err != nil {
			t.Fatal(err)
		}
		b, logs := newBackuper(p, filepath.Join(dir, "out-"+filepath.Base(p)), nil)
		if _, err := b.Snapshot(context.Background()); err == nil {
			t.Errorf("%s: snapshot succeeded", name)
		}
		if !strings.Contains(logs.String(), "BACKUP FAILED") {
			t.Errorf("%s: no failure logged:\n%s", name, logs)
		}
		if left := dirs(t, b.OutDir); len(left) != 0 {
			t.Errorf("%s: left %v", name, left)
		}
	}
}

func TestVerifyRejectsCorruption(t *testing.T) {
	p := filepath.Join(t.TempDir(), "c.sqlite")
	db, err := sqlitex.Open(p)
	if err != nil {
		t.Fatal(err)
	}
	mustExec(t, db, "create table trades (id integer primary key, pair text, is_open int)",
		"create index ix on trades(pair)",
		"with recursive s(i) as (select 1 union all select i+1 from s where i < 2000) "+
			"insert into trades select i, 'PAIR'||i, i%2 from s")
	db.Close()
	if _, _, err := verify(context.Background(), p); err != nil {
		t.Fatalf("clean db failed verify: %v", err)
	}
	raw, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	for i := len(raw) - 3*4096; i < len(raw)-4096; i++ { // scribble over an interior page
		raw[i] = 0xA5
	}
	if err := os.WriteFile(p, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := verify(context.Background(), p); err == nil {
		t.Error("verify accepted a corrupted database")
	}
}

func TestConfigFromEnv(t *testing.T) {
	for _, k := range []string{"OUT_DIR", "KEEP", "INTERVAL", "RUN_ONCE"} {
		t.Setenv(k, "")
	}
	t.Setenv("DB", "/data/x.sqlite")
	t.Setenv("EXTRA_FILES", " /data/a.json  /data/b.json ")
	c, err := ConfigFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if c.OutDir != "/backups" || c.Keep != 30 || c.Interval != 6*time.Hour || c.RunOnce ||
		strings.Join(c.Extra, "|") != "/data/a.json|/data/b.json" {
		t.Errorf("defaults: %+v", c)
	}
	for _, kv := range [][2]string{{"KEEP", "0"}, {"INTERVAL", "x"}, {"INTERVAL", "0"}, {"DB", ""}} {
		t.Run(kv[0]+"="+kv[1], func(t *testing.T) {
			t.Setenv(kv[0], kv[1])
			if _, err := ConfigFromEnv(); err == nil {
				t.Errorf("%s=%q accepted", kv[0], kv[1])
			}
		})
	}
}

func TestRunStopsOnCancel(t *testing.T) {
	dir := t.TempDir()
	db, _ := liveDB(t, dir)
	b, _ := newBackuper(db, filepath.Join(dir, "out"), nil)
	b.RunOnce = false
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error)
	go func() { done <- b.Run(ctx) }()
	for start := time.Now(); len(dirs(t, b.OutDir)) == 0 || strings.HasPrefix(dirs(t, b.OutDir)[0], ".tmp"); {
		if time.Since(start) > 10*time.Second {
			t.Fatal("first snapshot never appeared")
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("Run: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run ignored cancellation")
	}
}

func TestMainNeedsDB(t *testing.T) {
	t.Setenv("DB", "")
	if rc := Main(context.Background(), nil); rc != 2 {
		t.Errorf("Main without DB returned %d", rc)
	}
}
