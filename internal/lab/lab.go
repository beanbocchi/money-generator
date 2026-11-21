// Package lab runs the backtest rig: one backtest, a candle download, a serial matrix
// of backtests, and the ADR-0026 trend baseline. Each command builds its docker
// argv as data and hands it to a runx.Runner.
//
// The lab never writes to user_data/. The live bot's strategies are mounted read-only,
// and data and results live under lab/.
package lab

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/beanbocchi/money-generator/internal/repo"
	"github.com/beanbocchi/money-generator/internal/runx"
)

const defaultImage = "freqtradeorg/freqtrade:stable"

// Config is everything a lab command reads from outside the process.
type Config struct {
	Root string // repo root; relative paths below resolve against it
	// Getenv is the environment lookup. It is not envfile.Or because a queue row
	// overlays its columns on it (withEnv).
	Getenv func(string) string
	UID    int // passed to docker run -u
	GID    int
	Stdout io.Writer // the child's output goes here too; a queue run points both at lab/logs/NAME.log
	Stderr io.Writer
	// Runner returns a Runner whose child writes to stdout and stderr.
	Runner func(stdout, stderr io.Writer) runx.Runner
	Now    func() time.Time
}

// env returns the variable key, or def when it is unset or empty. That is the
// ${KEY:-def} rule the bash scripts used.
func (c Config) env(key, def string) string {
	if v := c.Getenv(key); v != "" {
		return v
	}
	return def
}

// path resolves a repo-relative path. The scripts cd'd to the repo root first.
func (c Config) path(p string) string {
	if filepath.IsAbs(p) {
		return p
	}
	return filepath.Join(c.Root, p)
}

// mount is "$PWD/$rel", built the way the scripts built it.
func (c Config) mount(rel string) string { return c.Root + "/" + rel }

func (c Config) user() string { return strconv.Itoa(c.UID) + ":" + strconv.Itoa(c.GID) }

// withEnv returns a copy of c whose Getenv sees over first.
func (c Config) withEnv(over map[string]string) Config {
	base := c.Getenv
	c.Getenv = func(k string) string {
		if v, ok := over[k]; ok {
			return v
		}
		return base(k)
	}
	return c
}

// run runs argv with its output going to c.Stdout and c.Stderr. runx ends its error
// with the child's stderr, which has already been streamed (hours of freqtrade log),
// so the error keeps only the command and its exit status.
func (c Config) run(ctx context.Context, argv []string) error {
	_, err := c.Runner(c.Stdout, c.Stderr).Run(ctx, argv[0], argv[1:]...)
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return fmt.Errorf("%s: %w", strings.Join(argv, " "), ee)
	}
	return err
}

// defaultConfig is the real machine: process environment, the invoking user's ids and
// a runner that streams the child's output. When stdout and stderr are one writer
// (a queue run's log), Combined gives the child one pipe, as 2>&1 did.
func defaultConfig() (Config, error) {
	root, err := repo.Root("lab")
	if err != nil {
		return Config{}, err
	}
	return Config{
		Root:   root,
		Getenv: os.Getenv,
		UID:    os.Getuid(),
		GID:    os.Getgid(),
		Stdout: os.Stdout,
		Stderr: os.Stderr,
		Runner: func(stdout, stderr io.Writer) runx.Runner {
			return runx.Exec{Dir: root, Stdout: stdout, Stderr: stderr, Combined: stdout == stderr, Grace: 30 * time.Second}
		},
		Now: time.Now,
	}, nil
}

func isHelp(args []string) bool {
	return len(args) > 0 && (args[0] == "-h" || args[0] == "--help" || args[0] == "help")
}

func arg(args []string, i int) string {
	if i < len(args) {
		return args[i]
	}
	return ""
}

func mkdirs(c Config, dirs ...string) error {
	for _, d := range dirs {
		if err := os.MkdirAll(c.path(d), 0o755); err != nil {
			return fmt.Errorf("create %s: %w", d, err)
		}
	}
	return nil
}

// report prints err the way the CLI does and returns the exit status.
func report(w io.Writer, cmd string, err error) int {
	if err != nil {
		fmt.Fprintf(w, "mg %s: %v\n", cmd, err)
	}
	return runx.ExitCode(err)
}
