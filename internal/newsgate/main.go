package newsgate

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/beanbocchi/money-generator/internal/envfile"
	"github.com/beanbocchi/money-generator/internal/fsx"
	"github.com/beanbocchi/money-generator/internal/repo"
)

const usage = `usage: mg newsgate run [--once]   run the gate (state in $NEWSGATE_STATE, default /state)
       mg newsgate windows        list would-block windows, open or closed
       mg newsgate logs [N]       print the last N log lines (default 30)

run needs NEWSGATE_PROXY_KEY and NEWSGATE_BASE_URL in the environment. windows and logs
read $NEWSGATE_STATE, else services/newsgate/state under the repo ($MG_ROOT, or the
parent of the directory holding the binary).
`

// stateMarker is the state directory relative to the repo root.
const stateMarker = "services/newsgate/state"

// env is what the command writes to and its clock, so tests can capture both.
type env struct {
	stdout, stderr io.Writer
	now            func() time.Time
}

// Main runs the shadow news gate (was services/newsgate/newsgate.py and bin/newsgate.sh).
func Main(ctx context.Context, args []string) int {
	return run(ctx, args, env{stdout: os.Stdout, stderr: os.Stderr, now: time.Now})
}

func run(ctx context.Context, args []string, e env) int {
	if len(args) == 0 {
		fmt.Fprint(e.stderr, usage)
		return 2
	}
	var err error
	switch args[0] {
	case "run":
		err = runGate(ctx, args[1:], e)
	case "windows":
		err = withLog(func(path string) error { return windows(path, e.stdout, e.now()) })
	case "logs":
		n := 30
		if len(args) > 1 {
			if n, err = strconv.Atoi(args[1]); err != nil || n < 0 {
				fmt.Fprintf(e.stderr, "newsgate logs: N must be a non-negative integer, got %q\n", args[1])
				return 2
			}
		}
		err = withLog(func(path string) error {
			s, err := fsx.TailLines(path, n)
			if err == nil {
				_, err = io.WriteString(e.stdout, s)
			}
			return err
		})
	case "-h", "--help", "help":
		fmt.Fprint(e.stdout, usage)
		return 0
	default:
		fmt.Fprintf(e.stderr, "newsgate: unknown subcommand %q\n\n%s", args[0], usage)
		return 2
	}
	if err != nil {
		fmt.Fprintf(e.stderr, "newsgate: %v\n", err)
		return 1
	}
	return 0
}

func runGate(ctx context.Context, args []string, e env) error {
	fset := flag.NewFlagSet("newsgate run", flag.ContinueOnError)
	fset.SetOutput(e.stderr)
	once := fset.Bool("once", false, "run one cycle, then exit")
	if err := fset.Parse(args); err != nil {
		return err
	}
	key, base := os.Getenv("NEWSGATE_PROXY_KEY"), os.Getenv("NEWSGATE_BASE_URL")
	if key == "" {
		return errors.New("NEWSGATE_PROXY_KEY is not set")
	}
	if base == "" {
		return errors.New("NEWSGATE_BASE_URL is not set")
	}
	return New(envfile.Or("NEWSGATE_STATE", "/state"), NewOpenAI(base, key)).Run(ctx, *once)
}

// withLog calls f with the log path for the host-side subcommands.
func withLog(f func(path string) error) error {
	dir := os.Getenv("NEWSGATE_STATE")
	if dir == "" {
		root, err := repo.Root(stateMarker)
		if err != nil {
			return err
		}
		dir = filepath.Join(root, stateMarker)
	}
	return f(filepath.Join(dir, "log.jsonl"))
}

// windows prints every window in the log, one line each, as bin/newsgate.sh did.
func windows(path string, w io.Writer, now time.Time) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(nil, maxBody)
	for sc.Scan() {
		var r struct {
			Window    *Window `json:"window"`
			Severity  int     `json:"severity"`
			EventType string  `json:"event_type"`
			Title     string  `json:"title"`
		}
		if err := json.Unmarshal(sc.Bytes(), &r); err != nil {
			return fmt.Errorf("log line %q: %w", truncateRunes(sc.Text(), 80), err)
		}
		if r.Window == nil {
			continue
		}
		until, err := time.Parse(time.RFC3339, r.Window.Until)
		if err != nil {
			return fmt.Errorf("window until: %w", err)
		}
		state := "closed"
		if until.After(now) {
			state = "OPEN  "
		}
		if _, err := fmt.Fprintf(w, "%s %s %-12s sev%d %-17s %s\n", state, r.Window.From,
			strings.Join(r.Window.Targets, ","), r.Severity, r.EventType, truncateRunes(r.Title, 70)); err != nil {
			return err
		}
	}
	return sc.Err()
}
