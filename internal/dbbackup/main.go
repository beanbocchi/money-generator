// Package dbbackup takes periodic consistent snapshots of the live trade database.
// (Was services/dbbackup/backup.sh, ADR-0008.)
//
// The sqlite file is the only record of which real positions are open. Losing it does not
// lose money directly, but it loses the bot's ability to reconcile with the exchange:
// freqtrade would start believing it holds nothing while the account holds real futures
// positions. That is worth a few hundred kilobytes a day.
//
// It snapshots with VACUUM INTO rather than a file copy. The bot writes in WAL mode, so a
// plain copy can catch a torn state: the main file and the -wal can disagree. VACUUM INTO
// reads the database under one read transaction, so the new file is a consistent
// snapshot of a live database without stopping the writer. The source is opened
// read-only, so this process never writes to it, not even a checkpoint.
package dbbackup

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"math/rand/v2"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/beanbocchi/money-generator/internal/clock"
	"github.com/beanbocchi/money-generator/internal/envfile"
	"github.com/beanbocchi/money-generator/internal/fsx"
	"github.com/beanbocchi/money-generator/internal/sqlitex"
)

// StampFormat names snapshot directories, e.g. 20261121T055530Z. Pruning sorts these
// lexically, which is why the stamp is ISO-ish.
const StampFormat = "20060102T150405Z"

// Config uses backup.sh's environment names and defaults.
type Config struct {
	DB       string        // DB: path of the live sqlite file (required)
	OutDir   string        // OUT_DIR, default /backups
	Keep     int           // KEEP, default 30
	Interval time.Duration // INTERVAL seconds, default 21600 (6h)
	Extra    []string      // EXTRA_FILES, whitespace-separated, e.g. the nfi profit-target cache
	RunOnce  bool          // RUN_ONCE=1
}

// ConfigFromEnv reads the environment.
func ConfigFromEnv() (Config, error) {
	c := Config{
		DB:      envfile.Or("DB", ""),
		OutDir:  envfile.Or("OUT_DIR", "/backups"),
		Extra:   strings.Fields(envfile.Or("EXTRA_FILES", "")),
		RunOnce: envfile.Or("RUN_ONCE", "0") == "1",
	}
	if c.DB == "" {
		return c, errors.New("set DB to the sqlite path")
	}
	var err error
	if c.Keep, err = envfile.Int("KEEP", 30); err != nil || c.Keep < 1 {
		return c, fmt.Errorf("KEEP must be a positive integer: %v", err)
	}
	if c.Interval, err = envfile.Seconds("INTERVAL", 21600); err != nil || c.Interval < time.Second {
		return c, fmt.Errorf("INTERVAL must be a positive number of seconds: %v", err)
	}
	return c, nil
}

// Main snapshots the trade database every INTERVAL until ctx is cancelled. With RUN_ONCE=1
// it takes one snapshot and exits non-zero if that failed.
func Main(ctx context.Context, args []string) int {
	log := slog.Default().With("svc", "dbbackup")
	if len(args) > 0 {
		log.Error("dbbackup takes no arguments; configure it with environment variables")
		return 2
	}
	cfg, err := ConfigFromEnv()
	if err != nil {
		log.Error("bad configuration", "err", err)
		return 2
	}
	if err := (Backuper{Config: cfg, Log: log, Now: time.Now}).Run(ctx); err != nil {
		return 1
	}
	return 0
}

// Backuper takes snapshots. Now is injectable so tests can force two runs into the same
// second.
type Backuper struct {
	Config
	Log *slog.Logger
	Now func() time.Time
}

// Run loops until ctx is done (or once). A failed snapshot is logged and retried next
// interval; in RunOnce mode its error is returned.
func (b Backuper) Run(ctx context.Context) error {
	b.Log.Info("started", "db", b.DB, "out", b.OutDir, "keep", b.Keep, "interval", b.Interval)
	for {
		_, err := b.Snapshot(ctx)
		if b.RunOnce {
			return err
		}
		if clock.Sleep(ctx, b.Interval) != nil {
			return nil
		}
	}
}

// Snapshot writes one verified snapshot directory and prunes old ones. It returns the
// directory it wrote.
func (b Backuper) Snapshot(ctx context.Context) (string, error) {
	stamp := b.Now().UTC().Format(StampFormat)

	// Write to a private temp directory and rename into place only once verified.
	//
	// Not cosmetic. The first version wrote straight into the final directory and
	// removed it on failure, and a failing run landing in the SAME SECOND as a previous
	// good snapshot resolved to that existing directory, so the cleanup deleted a good
	// backup. A backup tool whose error path destroys backups is worse than none.
	if err := os.MkdirAll(b.OutDir, 0o755); err != nil {
		b.Log.Error("cannot create output directory", "dir", b.OutDir, "err", err)
		return "", err
	}
	tmp, err := os.MkdirTemp(b.OutDir, ".tmp-"+stamp+"-")
	if err != nil {
		b.Log.Error("cannot create temp directory", "err", err)
		return "", err
	}
	// After a successful rename tmp no longer exists and this is a no-op.
	defer os.RemoveAll(tmp)
	// MkdirTemp makes it 0700; snapshots were 0755 (mkdir under umask 022).
	if err := os.Chmod(tmp, 0o755); err != nil {
		b.Log.Error("cannot set temp directory mode", "err", err)
		return "", err
	}
	dst := filepath.Join(tmp, filepath.Base(b.DB))

	if err := copyDB(ctx, b.DB, dst); err != nil {
		b.Log.Error("BACKUP FAILED", "db", b.DB, "err", err)
		return "", err
	}
	// Verify the snapshot opens and its schema is intact before trusting it.
	trades, open, err := verify(ctx, dst)
	if err != nil {
		b.Log.Error("BACKUP FAILED: snapshot did not verify, discarding", "db", b.DB, "err", err)
		return "", err
	}
	b.copyExtras(tmp)

	out, err := place(tmp, filepath.Join(b.OutDir, stamp))
	if err != nil {
		b.Log.Error("could not move snapshot into place, discarding", "err", err)
		return "", err
	}
	_, size := fsx.DirUsage(out)
	b.Log.Info("wrote "+out, "bytes", size, "trades", trades, "open", open)
	b.prune()
	return out, nil
}

// copyDB writes a snapshot of src (opened read-only, so a missing file is an error, not a
// new empty database) into the new file dst. VACUUM INTO runs in a single read
// transaction, so it sees one committed state while the bot keeps writing to its WAL.
func copyDB(ctx context.Context, src, dst string) error {
	db, err := sqlitex.OpenReadOnly(src)
	if err != nil {
		return err
	}
	defer db.Close()
	if _, err := db.ExecContext(ctx, "VACUUM INTO ?", dst); err != nil {
		return fmt.Errorf("vacuum into: %w", err)
	}
	// VACUUM INTO writes a rollback-journal file. Put the copy back in WAL mode, as the
	// live database (and the backup API's copies) were, so a restored snapshot runs in
	// the same mode. Closing the only connection checkpoints and removes the -wal.
	cp, err := sqlitex.Open(dst)
	if err != nil {
		return err
	}
	defer cp.Close()
	if _, err := cp.ExecContext(ctx, "pragma journal_mode=wal"); err != nil {
		return fmt.Errorf("journal_mode on copy: %w", err)
	}
	return cp.Close()
}

// verify runs integrity_check on the copy and counts its trades, reading the columns a
// restore depends on.
func verify(ctx context.Context, path string) (trades, open int, err error) {
	db, err := sqlitex.OpenReadOnly(path)
	if err != nil {
		return 0, 0, err
	}
	defer db.Close()
	var res string
	if err := db.QueryRowContext(ctx, "pragma integrity_check").Scan(&res); err != nil {
		return 0, 0, fmt.Errorf("integrity_check: %w", err)
	}
	if res != "ok" {
		return 0, 0, fmt.Errorf("integrity_check: %s", res)
	}
	var ids, pairs int
	err = db.QueryRowContext(ctx, "select count(*), coalesce(sum(is_open=1), 0), count(id), count(pair) from trades").
		Scan(&trades, &open, &ids, &pairs)
	if err != nil {
		return 0, 0, fmt.Errorf("read trades: %w", err)
	}
	return trades, open, nil
}

// copyExtras carries EXTRA_FILES along, preserving mode and times like cp -p. A missing
// file is skipped silently, as before; a failed copy is logged but does not fail the
// snapshot, since the database is what matters.
func (b Backuper) copyExtras(dir string) {
	for _, f := range b.Extra {
		st, err := os.Stat(f)
		if err != nil || !st.Mode().IsRegular() {
			continue
		}
		dst := filepath.Join(dir, filepath.Base(f))
		data, err := os.ReadFile(f)
		if err == nil {
			err = os.WriteFile(dst, data, st.Mode().Perm())
		}
		if err == nil {
			err = os.Chtimes(dst, st.ModTime(), st.ModTime())
		}
		if err != nil {
			b.Log.Warn("could not copy extra file", "file", f, "err", err)
		}
	}
}

// place renames tmp to out, never clobbering an existing snapshot: if this second
// already has one, it keeps both by naming the new one 20261121T055530-12345Z.
func place(tmp, out string) (string, error) {
	for {
		if _, err := os.Lstat(out); errors.Is(err, fs.ErrNotExist) {
			break
		} else if err != nil {
			return "", err
		}
		out = strings.TrimSuffix(out, "Z") + "-" + strconv.Itoa(rand.IntN(32768)) + "Z"
	}
	return out, os.Rename(tmp, out)
}

// prune removes the oldest directories in OutDir, keeping Keep. Like the find|sort in
// backup.sh it counts every directory, so a stale .tmp-* left by a crash sorts first and
// goes first.
func (b Backuper) prune() {
	ents, err := os.ReadDir(b.OutDir)
	if err != nil {
		b.Log.Warn("cannot list snapshots for pruning", "err", err)
		return
	}
	var dirs []string
	for _, e := range ents {
		if e.IsDir() {
			dirs = append(dirs, e.Name())
		}
	}
	sort.Strings(dirs)
	for i := 0; i < len(dirs)-b.Keep; i++ {
		old := filepath.Join(b.OutDir, dirs[i])
		b.Log.Info("pruning " + old)
		if err := os.RemoveAll(old); err != nil {
			b.Log.Warn("prune failed", "dir", old, "err", err)
		}
	}
}
