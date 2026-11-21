// Package runx runs external programs (docker, git) behind an interface, so callers can
// be tested with a fake instead of a real daemon or repository.
package runx

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"syscall"
	"time"
)

// Runner runs a program and returns what it captured (see Exec).
type Runner interface {
	Run(ctx context.Context, name string, args ...string) (string, error)
}

// Exec is the real Runner.
//
// By default it returns stdout and keeps stderr for the error message. Combined
// returns stdout and stderr interleaved, as `2>&1` would: freqtrade and the watchdog
// log to stderr. Stdout and Stderr, when set, also receive the output as it is
// written, so a two-hour backtest shows progress instead of nothing until it ends.
// They are written from two goroutines unless Combined (which uses Stdout for both), so
// they must be safe for concurrent use; *os.File is.
//
// Grace > 0 stops the child on cancel with SIGTERM and only SIGKILLs it Grace later.
// `docker run` forwards SIGTERM to its container; a SIGKILLed docker client leaves the
// container running and holding its memory.
type Exec struct {
	Dir      string
	Combined bool
	Stdout   io.Writer
	Stderr   io.Writer
	Grace    time.Duration
}

func (e Exec) Run(ctx context.Context, name string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = e.Dir
	if e.Grace > 0 {
		cmd.Cancel = func() error { return cmd.Process.Signal(syscall.SIGTERM) }
		cmd.WaitDelay = e.Grace
	}
	var out, errb bytes.Buffer
	cmd.Stdout = tee(&out, e.Stdout)
	if e.Combined {
		// The same writer value for both makes os/exec give the child one pipe for fd 1
		// and fd 2, so output interleaves exactly as 2>&1 and is copied by one goroutine.
		cmd.Stderr = cmd.Stdout
	} else {
		cmd.Stderr = tee(&errb, e.Stderr)
	}
	if err := cmd.Run(); err != nil {
		// The tail is enough to say why it failed; a long run's whole log is not.
		msg := lastLines(errb.String(), 5)
		if e.Combined {
			msg = lastLines(out.String(), 5)
		}
		return out.String(), fmt.Errorf("%s %s: %w: %s", name, strings.Join(args, " "), err, msg)
	}
	return out.String(), nil
}

// ExitCode is the child's exit status inside err, 1 for any other error, 0 for nil.
func ExitCode(err error) int {
	if err == nil {
		return 0
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) && ee.ExitCode() > 0 {
		return ee.ExitCode()
	}
	return 1
}

func tee(buf *bytes.Buffer, w io.Writer) io.Writer {
	if w == nil {
		return buf
	}
	return io.MultiWriter(buf, w)
}

func lastLines(s string, n int) string {
	ls := strings.Split(strings.TrimSpace(s), "\n")
	if len(ls) > n {
		ls = ls[len(ls)-n:]
	}
	return strings.Join(ls, "\n")
}
